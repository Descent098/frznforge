// Package markdown renders markdown strings — READMEs, release notes, note bodies, the
// profile page — to HTML at build time. It is the Go port of src/lib/markdown.ts, on goldmark
// instead of marked.
//
// Two trust levels, because since schema v3 not all of this content is the site owner's:
//
//   - trusted — the owner's own writing: content/profile.md, and repos configured as
//     type: "local". Raw HTML is passed through, same as it always was.
//   - untrusted — anything that came off a forge: an imported repo's README and its provider
//     release notes. docs/user/importing.md explicitly invites importing repos you do not
//     control, so anyone who can push a README or publish a release there would otherwise get
//     arbitrary <script> onto this site's origin (the output is written into the page as raw
//     HTML). For that content raw HTML blocks and inline tags are dropped, and link, image and
//     autolink URLs are restricted to http/https/mailto and relative targets.
//
// Dropping raw HTML rather than allow-listing it is deliberate: a partial HTML sanitiser is a
// liability, and the alternative here would mean writing one.
//
// Every frznforge behaviour in here is a goldmark AST transformer or a node-renderer override.
// None of it is a pass over the rendered string — including the tabindex on code blocks, which
// the TypeScript did with a regexp over the output. A string pass over generated HTML cannot
// tell markup it produced from markup an author wrote, and it re-parses work the renderer has
// already done; a renderer override touches exactly the nodes it means to.
//
// Relative links and images are left as-is, as in the TypeScript.
package markdown

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"

	"frznforge/internal/model"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Options selects the trust level and the mermaid gate for one render.
//
// Both fields are opt-IN, which inverts the TypeScript's defaults on purpose. renderMarkdown()
// defaulted `trusted` to true so that call sites written before the trust boundary existed kept
// working; in Go there are no such call sites, and a forgotten field must fail closed — an
// unset Trusted renders imported content safely, where a defaulted-true one would publish
// somebody else's <script>. Mermaid is likewise explicit: it is a config flag
// (config.MarkdownConfig.Mermaid), so the caller already holds the value and passing it is no
// burden.
type Options struct {
	// Trusted marks content the site owner wrote. Use IsTrustedSource for repo content.
	Trusted bool
	// Mermaid renders ```mermaid fences as diagram containers. Pass config's markdown.mermaid.
	Mermaid bool
}

// Render turns markdown into HTML under the given trust level.
func Render(src string, opts Options) string {
	engine := untrustedEngine
	if opts.Trusted {
		engine = trustedEngine
	}
	pc := parser.NewContext()
	if opts.Mermaid {
		pc.Set(mermaidEnabledKey, true)
	}
	var buf bytes.Buffer
	// Convert only fails when the writer does, and a bytes.Buffer does not. Whatever was
	// written before an error is still the best available answer, so there is nothing to
	// return but the buffer either way.
	_ = engine.Convert([]byte(src), &buf, parser.WithContext(pc))
	return buf.String()
}

// ContainsMermaid reports whether rendered HTML holds at least one mermaid container — the
// signal a page uses to include web/js/mermaid.js at all, so a diagram-free page loads none of
// it (tests/e2e/mermaid.spec.ts measures exactly that).
//
// It reads the rendered HTML rather than the markdown source because the answer must be "did a
// container actually get emitted", not "might one have been": the mermaid gate, the trust mode
// and the info-string rules all live in the render. Escaped fences cannot fake a match — their
// quotes come out as &quot;.
func ContainsMermaid(html string) bool {
	return strings.Contains(html, `class="hf-mermaid"`)
}

// IsTrustedSource reports whether a repo's markdown is the site owner's own. Only type "local"
// is; every other source is a repo pulled off someone else's forge.
//
// It delegates to model.RepoSource.IsRemote so that "which repos are ours" is decided in one
// place — a second copy of that comparison is how the artifact and the renderer drift apart.
func IsTrustedSource(source model.RepoSource) bool {
	return !source.IsRemote()
}

var markdownPathRe = regexp.MustCompile(`(?i)(\.(md|markdown|mdown|mkd)$)|(^|/)readme$`)

// IsMarkdownPath is the heuristic for "render this file as markdown": the markdown extensions,
// plus an extensionless README.
func IsMarkdownPath(path string) bool {
	return markdownPathRe.MatchString(path)
}

/* ---- URL sanitising ------------------------------------------------------ */

// safeSchemes are the URL schemes a link or image may name; everything else becomes an inert
// "#".
var safeSchemes = map[string]bool{"http": true, "https": true, "mailto": true}

var entityRe = regexp.MustCompile(`(?i)&(#x[0-9a-f]+|#\d+|[a-z]+);?`)

// namedEntities is deliberately tiny: only the names that can smuggle a scheme past the check
// below. This decodes URLs to INSPECT them and never to build output, so an unknown name is
// safely left alone.
var namedEntities = map[string]string{
	"amp": "&", "colon": ":", "tab": "\t", "newline": "\n", "lt": "<", "gt": ">",
}

// decodeEntities resolves the entity escapes a payload uses to hide a scheme
// (`&#106;avascript:`).
func decodeEntities(value string) string {
	return entityRe.ReplaceAllStringFunc(value, func(whole string) string {
		body := strings.TrimSuffix(strings.TrimPrefix(whole, "&"), ";")
		if strings.HasPrefix(body, "#") {
			var (
				code int64
				err  error
			)
			if len(body) > 1 && (body[1] == 'x' || body[1] == 'X') {
				code, err = strconv.ParseInt(body[2:], 16, 64)
			} else {
				code, err = strconv.ParseInt(body[1:], 10, 64)
			}
			if err != nil || code < 0 || code > 0x10FFFF {
				return whole
			}
			// Go turns a lone surrogate into U+FFFD where JavaScript keeps the surrogate.
			// Neither is a URL scheme character, so the probe below reads the same either way.
			return string(rune(code))
		}
		if r, ok := namedEntities[strings.ToLower(body)]; ok {
			return r
		}
		return whole
	})
}

// blankRe is JavaScript's \s plus the C0 controls and DEL: Go's own \s is only [\t\n\f\r ],
// and `java&#12288;script:` would survive a narrower strip.
var blankRe = regexp.MustCompile(`[\s\p{Zs}\x{2028}\x{2029}\x{FEFF}\x{0}-\x{1F}\x{7F}]+`)

var schemeRe = regexp.MustCompile(`^([a-z][a-z0-9+.\-]*):`)

// SafeURL returns href unchanged when it is safe to navigate to, and "#" when it is not.
//
// A URL with no scheme (relative, or a #fragment) is fine; a URL that names one must name a
// scheme on the allow-list, which rules out javascript:, data: and vbscript:. Entity escapes,
// whitespace and control characters are stripped before the scheme is read, because
// `java&#9;script:` still navigates.
func SafeURL(href string) string {
	probe := strings.ToLower(blankRe.ReplaceAllString(decodeEntities(href), ""))
	m := schemeRe.FindStringSubmatch(probe)
	if m == nil {
		return href
	}
	if safeSchemes[m[1]] {
		return href
	}
	return "#"
}

/* ---- engines ------------------------------------------------------------- */

// mermaidEnabledKey carries the per-render mermaid gate into the AST transformer.
//
// The TypeScript used a module-level boolean, safe only because marked parses synchronously.
// goldmark hands every transformer the parse context, so the flag can travel with the render it
// belongs to and two concurrent renders cannot see each other's setting.
var mermaidEnabledKey = parser.NewContextKey()

// mermaidAttr marks a fenced block the mermaid transformer claimed, so the code renderer knows
// which container to emit without re-reading the info string.
const mermaidAttr = "hfMermaid"

var (
	trustedEngine   = newEngine(true)
	untrustedEngine = newEngine(false)
)

// newEngine builds one goldmark for a trust level. Two engines, not four: the mermaid gate is a
// parse-context value, so it does not need its own instance. goldmark instances are immutable
// once built and safe to share across renders.
func newEngine(trusted bool) goldmark.Markdown {
	transformers := []util.PrioritizedValue{
		util.Prioritized(headingDemoter{}, 100),
		util.Prioritized(mermaidMarker{}, 200),
	}
	rendererOpts := []renderer.Option{
		renderer.WithNodeRenderers(util.Prioritized(codeBlockRenderer{}, 100)),
	}
	if trusted {
		rendererOpts = append(rendererOpts, html.WithUnsafe())
	} else {
		transformers = append(transformers, util.Prioritized(urlSanitizer{}, 300))
		rendererOpts = append(rendererOpts,
			renderer.WithNodeRenderers(util.Prioritized(rawHTMLDropper{}, 100)))
	}
	return goldmark.New(
		// CommonMark + GFM: tables, strikethrough, autolinks, task lists — marked's `gfm: true`.
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(parser.WithASTTransformers(transformers...)),
		goldmark.WithRendererOptions(rendererOpts...),
	)
}

/* ---- heading demotion ---------------------------------------------------- */

// headingDemoter drops every heading one level: `#` becomes <h2>, `##` becomes <h3>, and
// `######` stays <h6> because there is nowhere lower to go.
//
// Rendered markdown never owns a page here. A README is inside a card whose own head is an
// <h2>README</h2>, on a page whose <h1> is the repo; a note's body sits under the note title
// and the file name; release notes sit under the release. Letting user markdown emit an <h1>
// gave those pages two <h1>s and — when a README was a lone title with no `##` in it, which is
// the most common README there is — a document that stepped h1 → h3 straight into the About
// sidebar and failed heading-order.
//
// The visual result is unchanged: .hf-md's heading sizes are shifted by the same one level in
// web/css/global.css, so a `#` still renders as a title. Setext headings go through the same
// node, so `Title\n=====` is demoted too.
type headingDemoter struct{}

func (headingDemoter) Transform(doc *ast.Document, _ text.Reader, _ parser.Context) {
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if h, ok := n.(*ast.Heading); ok && h.Level < 6 {
			h.Level++
		}
		return ast.WalkContinue, nil
	})
}

/* ---- mermaid fences ------------------------------------------------------ */

// mermaidMarker claims ```mermaid fences so the code renderer emits an hf-mermaid container
// that the client-side renderer (web/js/mermaid.js) turns into an SVG. The <pre><code> IS the
// no-JS fallback — a reader without JavaScript gets the diagram source as an honest code block,
// and an invalid diagram stays one.
//
// Applies in BOTH trust modes, by the owner's decision: importing a repo is choosing to publish
// its content, so its diagrams render like everything else — the owner is the one who decides
// what to import (the same stance the big forges take, rendering mermaid in every README). This
// does not weaken the untrusted engine's guarantees: raw HTML is still dropped and URLs are
// still filtered, the fence text is escaped here at build time, and at view time mermaid runs
// with securityLevel: 'strict', its own sanitiser, in the visitor's browser.
type mermaidMarker struct{}

func (mermaidMarker) Transform(doc *ast.Document, reader text.Reader, pc parser.Context) {
	if enabled, _ := pc.Get(mermaidEnabledKey).(bool); !enabled {
		return
	}
	source := reader.Source()
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if fcb, ok := n.(*ast.FencedCodeBlock); ok && isMermaidInfo(fcb, source) {
			fcb.SetAttributeString(mermaidAttr, true)
		}
		return ast.WalkContinue, nil
	})
}

// isMermaidInfo matches the info string's FIRST word only, so ```mermaid theme=neutral is a
// diagram and ```js holding the word mermaid is not. Split on any whitespace rather than
// goldmark's Language(), which stops at a space and would hand back "mermaid\ttheme=neutral".
func isMermaidInfo(n *ast.FencedCodeBlock, source []byte) bool {
	if n.Info == nil {
		return false
	}
	fields := strings.Fields(string(n.Info.Segment.Value(source)))
	return len(fields) > 0 && strings.EqualFold(fields[0], "mermaid")
}

/* ---- code blocks --------------------------------------------------------- */

// codeBlockRenderer renders fenced and indented code blocks, and is where both the mermaid
// container and the code block tabindex come from.
//
// The tabindex: .hf-md pre is overflow-x: auto, so a long line makes it a scrollable region —
// and a scrollable region with no focusable content and no tabindex cannot be scrolled at all
// without a mouse in Firefox or Safari (Chromium ships focusable scrollers; the other two do
// not). Highlighted blocks carry tabindex="0" from internal/highlight; markdown's did not,
// which left every fenced block in a README, a note preview and a release body stranded
// (axe scrollable-region-focusable, WCAG 2.1.1).
//
// The TypeScript added it by rewriting `<pre>` to `<pre tabindex="0">` in the output string,
// which also caught a bare <pre> an author wrote by hand in trusted raw HTML. This does not:
// raw HTML is passed through as written. That is the intended reading of the rule — the
// tabindex belongs to blocks this renderer produces, and a trusted author writing their own
// <pre> owns its attributes.
type codeBlockRenderer struct{}

func (codeBlockRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindCodeBlock, renderIndentedCodeBlock)
	reg.Register(ast.KindFencedCodeBlock, renderFencedCodeBlock)
}

func renderIndentedCodeBlock(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		_, _ = w.WriteString(`<pre tabindex="0"><code>`)
		writeCodeLines(w, source, n)
	} else {
		_, _ = w.WriteString("</code></pre>\n")
	}
	return ast.WalkContinue, nil
}

func renderFencedCodeBlock(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	n := node.(*ast.FencedCodeBlock)
	if _, isMermaid := n.AttributeString(mermaidAttr); isMermaid {
		if entering {
			_, _ = w.WriteString(`<pre class="hf-mermaid" tabindex="0"><code class="language-mermaid">`)
			writeCodeLines(w, source, n)
		} else {
			_, _ = w.WriteString("</code></pre>\n")
		}
		return ast.WalkContinue, nil
	}
	if entering {
		_, _ = w.WriteString(`<pre tabindex="0"><code`)
		if language := n.Language(source); language != nil {
			_, _ = w.WriteString(` class="language-`)
			// html.DefaultWriter, not util.EscapeHTML: goldmark's own code-block renderer uses
			// it here, and it resolves an entity reference in the info string before escaping
			// the result. EscapeHTML alone escaped the ampersand instead, so ```&lt;x came out
			// as class="language-&amp;lt;x" — inert either way, but neither goldmark's answer
			// nor marked's.
			html.DefaultWriter.Write(w, language)
			_, _ = w.WriteString(`"`)
		}
		_ = w.WriteByte('>')
		writeCodeLines(w, source, n)
	} else {
		_, _ = w.WriteString("</code></pre>\n")
	}
	return ast.WalkContinue, nil
}

// writeCodeLines writes a block's source lines escaped — &, <, > and " — so nothing inside a
// fence can become markup. That is what keeps a fence holding `</code></pre><script>` an
// honest code block, and what stops one holding the text `<pre class="hf-mermaid">` from
// fooling ContainsMermaid.
func writeCodeLines(w util.BufWriter, source []byte, n ast.Node) {
	lines := n.Lines()
	for i := 0; i < lines.Len(); i++ {
		line := lines.At(i)
		_, _ = w.Write(util.EscapeHTML(line.Value(source)))
	}
}

/* ---- untrusted content --------------------------------------------------- */

// rawHTMLDropper renders raw HTML — block (<script>…) and inline (<img onerror=…>) — as
// nothing at all.
//
// goldmark's own answer when Unsafe is off is to write `<!-- raw HTML omitted -->` in its
// place, which would put a marked-up breadcrumb of the attacker's payload into every imported
// README. Dropping it silently matches the TypeScript renderer, which returned ”.
type rawHTMLDropper struct{}

func (rawHTMLDropper) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindHTMLBlock, dropNode)
	reg.Register(ast.KindRawHTML, dropNode)
}

func dropNode(util.BufWriter, []byte, ast.Node, bool) (ast.WalkStatus, error) {
	return ast.WalkSkipChildren, nil
}

// urlSanitizer rewrites every navigable URL in untrusted content through SafeURL.
//
// Links and images are rewritten in place, so goldmark's own renderers still do the escaping of
// titles and alt text. Autolinks cannot be rewritten in place — an ast.AutoLink reads its URL
// straight out of the source — so an unsafe one is replaced with a real link to "#" carrying
// the same visible text. Autolinks matter here because `<javascript:alert(1)>` is a valid
// CommonMark autolink and would otherwise be the one URL that never passed the filter.
type urlSanitizer struct{}

func (urlSanitizer) Transform(doc *ast.Document, reader text.Reader, _ parser.Context) {
	source := reader.Source()
	var unsafeLinks []*ast.AutoLink
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch v := n.(type) {
		case *ast.Link:
			v.Destination = []byte(SafeURL(string(v.Destination)))
		case *ast.Image:
			v.Destination = []byte(SafeURL(string(v.Destination)))
		case *ast.AutoLink:
			if url := string(v.URL(source)); SafeURL(url) != url {
				// Collected, not replaced: swapping a node out from under the walk is how a
				// sibling gets skipped.
				unsafeLinks = append(unsafeLinks, v)
			}
		}
		return ast.WalkContinue, nil
	})
	for _, a := range unsafeLinks {
		parent := a.Parent()
		if parent == nil {
			continue
		}
		link := ast.NewLink()
		link.Destination = []byte("#")
		link.AppendChild(link, ast.NewString(a.Label(source)))
		parent.ReplaceChild(parent, a, link)
	}
}
