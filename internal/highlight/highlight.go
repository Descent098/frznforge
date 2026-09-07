// Package highlight turns source code into highlighted HTML at build time. It is the Go port
// of src/lib/highlight.ts, on chroma instead of Shiki.
//
// The owner's binding decision for 0.4.0: chroma emits TOKEN CLASSES, not inline styles, and
// the colours for those classes are hand-written into web/css/repo.css under the same
// [data-theme] attribute the rest of the site uses. Shiki shipped two whole themes as CSS
// variables on every span, which meant every code byte carried its own palette; a class per
// token and one stylesheet is smaller output, one place to fix a contrast failure, and it makes
// the light/dark switch the same mechanism as everything else on the page.
//
// The markup this emits is the markup Shiki emitted, because it is load-bearing beyond the
// colours: one <span class="line" id="Ln"> per line, so #L12 anchors work with pure CSS
// :target, and so .hf-code's counter-based gutter numbers every line exactly once.
//
// There is no memo here. src/lib/highlight-cache.ts exists because Shiki was 84% of the render;
// chroma is a different order of tool, and whether a cache is worth its complexity is a
// measurement to be taken against this code, not an assumption to be ported into it.
// Highlight is a pure function so that measurement is possible at all.
package highlight

import (
	"log/slog"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

// LanguageToChroma maps the language names the ingest language map emits
// (src/lib/ingest/languages.ts, the `name` of every LANGS entry) to a chroma lexer name.
//
// EVERY name ingest can emit is a key here, including the ones chroma has no lexer for: those
// map to "" and fall through to a filename match. A missing key is a test failure
// (TestLanguageMapCoversIngest), not a silently uncoloured file — which is exactly the failure
// mode the Shiki table had. LANGUAGE_TO_SHIKI covered 51 of the 85 names, spelled one of them
// 'Ini' where ingest emits 'INI' (so .ini files were never coloured), and carried five keys —
// TSX, JSX, JSON with Comments, Ini, Diff — that ingest cannot produce at all.
//
// Values are chroma lexer names as chroma spells them; TestLanguageMapResolves asserts each one
// resolves to a lexer that actually claims that name or alias, so a typo cannot quietly degrade
// to chroma's filename guessing.
var LanguageToChroma = map[string]string{
	"Ada":      "Ada",
	"AsciiDoc": "",    // chroma has no AsciiDoc lexer
	"Assembly": "GAS", // ingest's .asm and .s; GAS fits .s and is tolerable for .asm
	// No Astro lexer. HTML colours the markup and the <script>/<style> blocks; the `---`
	// frontmatter reads as text. Better than nothing, and honest about being an approximation.
	"Astro":            "HTML",
	"Batchfile":        "Batchfile",
	"C":                "C",
	"C#":               "C#",
	"C++":              "C++",
	"CMake":            "CMake",
	"CSS":              "CSS",
	"CSV":              "CSV",
	"Clojure":          "Clojure",
	"CoffeeScript":     "CoffeeScript",
	"Common Lisp":      "Common Lisp",
	"Crystal":          "Crystal",
	"D":                "D",
	"Dart":             "Dart",
	"Dockerfile":       "Docker",
	"EditorConfig":     "INI", // .editorconfig is INI syntax
	"Elixir":           "Elixir",
	"Elm":              "Elm",
	"Emacs Lisp":       "EmacsLisp",
	"Erlang":           "Erlang",
	"F#":               "FSharp",
	"Fortran":          "Fortran",
	"Git Attributes":   "",    // chroma has no gitattributes lexer
	"Git Config":       "INI", // .gitconfig/.gitmodules are INI syntax
	"Gleam":            "Gleam",
	"Go":               "Go",
	"GraphQL":          "GraphQL",
	"Groovy":           "Groovy",
	"HCL":              "HCL",
	"HTML":             "HTML",
	"Handlebars":       "Handlebars",
	"Haskell":          "Haskell",
	"INI":              "INI",
	"Ignore List":      "", // chroma has no gitignore lexer
	"JSON":             "JSON",
	"Java":             "Java",
	"JavaScript":       "JavaScript",
	"Julia":            "Julia",
	"Jupyter Notebook": "JSON", // an .ipynb on disk IS JSON
	"Kotlin":           "Kotlin",
	// No Less lexer. The CSS one mis-reads Less's @variables badly enough that plain text is
	// the more honest render.
	"Less": "",
	// package-lock.json, cargo.lock, go.sum and flake.lock share no syntax, so there is nothing
	// to name here — the filename fallback still finds JSON for the ones that are JSON.
	"Lockfile":         "",
	"Lua":              "Lua",
	"MDX":              "markdown", // approximation: no MDX lexer, and MDX is markdown plus JSX
	"Makefile":         "Makefile",
	"Markdown":         "markdown",
	"Nim":              "Nim",
	"Nix":              "Nix",
	"OCaml":            "OCaml",
	"Objective-C":      "Objective-C",
	"PHP":              "PHP",
	"Pascal":           "ObjectPascal",
	"Perl":             "Perl",
	"PowerShell":       "PowerShell",
	"Protocol Buffer":  "Protocol Buffer",
	"Pug":              "", // chroma has no Pug/Jade lexer
	"Python":           "Python",
	"R":                "R",
	"Ruby":             "Ruby",
	"Rust":             "Rust",
	"SCSS":             "SCSS",
	"SQL":              "SQL",
	"SVG":              "XML", // an SVG is XML
	"Sass":             "Sass",
	"Scala":            "Scala",
	"Scheme":           "Scheme",
	"Shell":            "Bash",
	"Solidity":         "Solidity",
	"Svelte":           "Svelte",
	"Swift":            "Swift",
	"TOML":             "TOML",
	"TeX":              "TeX",
	"Text":             "", // plain by definition
	"TypeScript":       "TypeScript",
	"V":                "V",
	"Vim Script":       "VimL",
	"Vue":              "vue",
	"WebAssembly":      "", // chroma has no wat/wast lexer
	"XML":              "XML",
	"YAML":             "YAML",
	"Zig":              "Zig",
	"reStructuredText": "reStructuredText",
}

// LexerName is the chroma lexer for an artifact language name, falling back to the file name,
// and "" when neither answers — meaning "render it as plain text".
//
// The filename fallback is chroma's own glob matcher rather than the TypeScript's "treat the
// extension as a language id" trick, which only worked because Shiki registers most extensions
// as aliases. lexers.Match is the same idea done properly, and it also catches the files that
// have no extension at all.
func LexerName(language, filePath string) string {
	if name, ok := LanguageToChroma[language]; ok && name != "" {
		return name
	}
	if filePath != "" {
		if l := lexers.Match(path.Base(filePath)); l != nil {
			return l.Config().Name
		}
	}
	return ""
}

// CountLines counts lines the way editors do: a trailing newline does not open a new one.
//
// This rule is load-bearing in more than one place — the blob page's "N lines" label, its
// gutter, and the insights code-size series, whose schema comment cites the same rule — so all
// of them have to agree. Highlight drops the same trailing newline before it tokenises for
// exactly that reason.
//
// It normalises line endings first, for the same reason and by the same rule Highlight does: a
// lone CR is a line break to every editor, to CSS `white-space: pre`, and to Highlight's own
// normalizeEOL. Counting only "\n" made a CR-separated file render N line spans against a label
// that said 1, so the gutter numbered lines the label denied existed and every #L anchor past
// the first pointed at nothing. CRLF was never affected — it carries a "\n" of its own — which
// is why only the rarer classic-Mac ending exposed it.
func CountLines(code string) int {
	if len(code) == 0 {
		return 0
	}
	code = normalizeEOL(code)
	n := strings.Count(code, "\n") + 1
	if strings.HasSuffix(code, "\n") {
		return n - 1
	}
	return n
}

// classPrefix keeps chroma's token classes inside the site's hf- namespace, alongside every
// other class the build emits. The line wrapper deliberately does NOT take it: `.line` is the
// name .hf-code's gutter, :target rule and line-anchor CSS already use.
const classPrefix = "hf-"

// Highlight renders source to HTML:
//
//	<pre class="hf-chroma" tabindex="0"><code><span class="line" id="L1">…</span>
//	<span class="line" id="L2">…</span></code></pre>
//
// Token colours come from web/css/repo.css by class; see the file header for why.
//
// idPrefix namespaces the per-line ids. A page that highlights ONE file can leave it empty and
// get the documented `L12`. A page that highlights several — a multi-file note renders one card
// per file — MUST pass a per-file prefix: without it every card emitted id="L1"…"L30" again, so
// #L5 resolved to the first file only and the lines of every later file were permanently
// unlinkable. The note file view passes its section anchor, giving `f-netlify-toml-L5`.
//
// The tabindex is not decoration: .hf-code is overflow-x: auto, and a scrollable region with
// nothing focusable in it cannot be scrolled without a mouse in Firefox or Safari.
func Highlight(code, language, filePath, idPrefix string) string {
	src := normalizeEOL(code)
	// Drop the file's own trailing newline before tokenising. Splitting "a\nb\n" on newlines
	// yields a third, empty line, and a file that ends with a newline is nearly every file:
	// left in, the gutter would number a phantom line that the "N lines" label from CountLines
	// does not count.
	src = strings.TrimSuffix(src, "\n")
	lines := highlightLines(src, LexerName(language, filePath))

	var b strings.Builder
	b.Grow(len(src) + 64*len(lines) + 64)
	b.WriteString(`<pre class="hf-chroma" tabindex="0"><code>`)
	for i, line := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(`<span class="line" id="`)
		// Escaped like any other input. Callers pass a slugged section anchor, but that anchor
		// is derived from a path inside an imported repository, and this is an attribute sink in
		// the one package whose job is to make repository content inert. A hostile prefix now
		// yields a broken id instead of markup.
		writeEscaped(&b, idPrefix)
		b.WriteByte('L')
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(`">`)
		b.WriteString(line)
		b.WriteString(`</span>`)
	}
	b.WriteString(`</code></pre>`)
	return b.String()
}

// normalizeEOL folds CRLF and lone CR to LF.
//
// It runs before anything counts or splits lines, so that a repository committed from Windows
// gets the same gutter, the same line ids and the same "N lines" as one committed from Linux.
// chroma's own tokeniser normalises the same way (TokeniseOptions.EnsureLF); doing it here as
// well is what keeps the lexed path and the plain-text path identical.
func normalizeEOL(code string) string {
	if !strings.ContainsRune(code, '\r') {
		return code
	}
	return strings.ReplaceAll(strings.ReplaceAll(code, "\r\n", "\n"), "\r", "\n")
}

// highlightLines returns the inner HTML of each line, already escaped.
//
// The line count is pinned to the source's own: several chroma lexers set EnsureNL and append a
// newline to the text they are given, which would otherwise add an empty line the gutter counts
// and CountLines does not. Everything else about the token stream is chroma's.
func highlightLines(src, lexerName string) []string {
	want := strings.Count(src, "\n") + 1
	lexer := resolveLexer(lexerName)
	if lexer == nil {
		return plainLines(src)
	}
	// Tokenising is serialised per lexer.
	//
	// chroma's registry hands every caller the SAME lexer value, and building this site with pages
	// rendering in parallel produced spurious Error tokens around single characters, at random
	// positions, in large files — a page that still renders, with one letter quietly turned red. It
	// reproduced only under the race detector's scheduling and never in isolation, which is exactly
	// the kind of defect not to leave in place because it is hard to catch.
	//
	// The lock is per lexer NAME rather than global, so different languages still tokenise
	// concurrently, and it covers draining the iterator as well as creating it because chroma's
	// iterators are lazy — the work happens in Tokens(), not in Tokenise().
	mu := lexerLock(lexerName)
	// Logged around the wait, not just after it. This lock serialises every page in one language,
	// so a corpus that is 90% one language spends most of the render queued here — and "the build
	// is slow" and "the build is stuck" look identical from outside unless something records the
	// wait. The pair also bounds it: a "lock waiting" with no "lock held" is a build that stopped
	// on this mutex.
	//
	// Both records are debug and carry no formatting work of their own, which matters because
	// this is the hottest path in the build: with the default discard handler each is one
	// comparison.
	lockWait := time.Now()
	slog.Debug("lock waiting", "lock", "highlight.lexer", "name", lexerName)
	mu.Lock()
	waited := time.Since(lockWait)
	slog.Debug("lock held", "lock", "highlight.lexer", "name", lexerName, "waitedMs", waited.Milliseconds())
	defer func() {
		mu.Unlock()
		slog.Debug("lock released", "lock", "highlight.lexer", "name", lexerName)
	}()

	iterator, err := lexer.Tokenise(nil, src)
	if err != nil {
		// A tokeniser that cannot read its input still has to produce the file. Uncoloured
		// text is a degraded page; a missing one is a broken build.
		return plainLines(src)
	}

	lines := make([]string, 0, want)
	var cur strings.Builder
	for _, token := range iterator.Tokens() {
		class := tokenClass(token.Type)
		parts := strings.Split(token.Value, "\n")
		for i, part := range parts {
			if i > 0 {
				lines = append(lines, cur.String())
				cur.Reset()
			}
			if part == "" {
				continue
			}
			writeToken(&cur, class, part)
		}
	}
	lines = append(lines, cur.String())
	for len(lines) < want {
		lines = append(lines, "")
	}
	return lines[:want]
}

// lexerLocks guards chroma's shared lexers, one mutex per lexer name. See highlightLines.
var (
	lexerLocksMu sync.Mutex
	lexerLocks   = map[string]*sync.Mutex{}
)

func lexerLock(name string) *sync.Mutex {
	lexerLocksMu.Lock()
	defer lexerLocksMu.Unlock()
	mu, ok := lexerLocks[name]
	if !ok {
		mu = &sync.Mutex{}
		lexerLocks[name] = mu
	}
	return mu
}

func plainLines(src string) []string {
	parts := strings.Split(src, "\n")
	out := make([]string, len(parts))
	for i, p := range parts {
		var b strings.Builder
		writeToken(&b, "", p)
		out[i] = b.String()
	}
	return out
}

// resolveLexer returns a coalescing lexer for a chroma name, or nil for plain text. Coalescing
// merges runs of same-typed tokens, which is what keeps a line of ordinary text from becoming
// twenty adjacent identical spans.
func resolveLexer(name string) chroma.Lexer {
	if name == "" {
		return nil
	}
	lexer := lexers.Get(name)
	if lexer == nil {
		return nil
	}
	return chroma.Coalesce(lexer)
}

// tokenClass is chroma's own token-type-to-class rule: the exact type if it has a class, else
// the nearest ancestor type that does, else no class at all (plain Text). Reimplemented rather
// than borrowed from chroma's HTML formatter because that formatter cannot emit the per-line
// ids this markup is built on.
func tokenClass(t chroma.TokenType) string {
	for t != 0 {
		if class, ok := chroma.StandardTypes[t]; ok {
			if class == "" {
				return ""
			}
			return classPrefix + class
		}
		t = t.Parent()
	}
	if class := chroma.StandardTypes[t]; class != "" {
		return classPrefix + class
	}
	return ""
}

func writeToken(b *strings.Builder, class, textValue string) {
	if class == "" {
		writeEscaped(b, textValue)
		return
	}
	b.WriteString(`<span class="`)
	b.WriteString(class)
	b.WriteString(`">`)
	writeEscaped(b, textValue)
	b.WriteString(`</span>`)
}

// writeEscaped escapes the four characters that can end a text node or an attribute. Source
// code is the one input on this site guaranteed to contain them.
func writeEscaped(b *strings.Builder, s string) {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		default:
			b.WriteByte(s[i])
		}
	}
}
