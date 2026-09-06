// Markdown rendering, and in particular the trust boundary.
//
// A repo imported from a forge is not the site owner's content: its README and its provider
// release notes are written by whoever can push to it, and the site writes the rendered HTML
// straight into the page. Most of what follows is a regression guard on "that content cannot
// execute" — the same guard tests/unit/markdown.test.ts and tests/e2e/releases.spec.ts hold on
// the TypeScript build.
package markdown

import (
	"strings"
	"sync"
	"testing"

	"frznforge/internal/model"
)

func trusted(src string) string   { return Render(src, Options{Trusted: true, Mermaid: true}) }
func untrusted(src string) string { return Render(src, Options{Mermaid: true}) }

// mustContain / mustNotContain keep the failure messages readable: a rendered document is long
// enough that "expected true, got false" is useless on its own.
func mustContain(t *testing.T, html, want string) {
	t.Helper()
	if !strings.Contains(html, want) {
		t.Errorf("missing %q in:\n%s", want, html)
	}
}

func mustNotContain(t *testing.T, html, unwanted string) {
	t.Helper()
	if strings.Contains(html, unwanted) {
		t.Errorf("found %q in:\n%s", unwanted, html)
	}
}

/* ---- trusted ------------------------------------------------------------- */

func TestTrustedPassesOwnerHTMLThrough(t *testing.T) {
	html := trusted("<div class=\"note\">hi</div>\n\nplain **text**\n")
	mustContain(t, html, `<div class="note">hi</div>`)
	mustContain(t, html, "<strong>text</strong>")
}

// A real README shape: raw HTML badges above the prose. The owner's own repos lean on this, so
// it is the case that would break loudest if raw HTML stopped passing through.
func TestTrustedRendersARealReadme(t *testing.T) {
	readme := strings.Join([]string{
		"# frznforge",
		"",
		`<p align="center">`,
		`  <img src="docs/logo.svg" alt="frznforge" width="120">`,
		`</p>`,
		"",
		"A static forge for repositories you already have. See the [docs](./docs/).",
		"",
		"## Install",
		"",
		"```bash",
		"npm install",
		"```",
		"",
		"| flag | meaning |",
		"| ---- | ------- |",
		"| `--no-ingest` | reuse the last artifact |",
	}, "\n") + "\n"

	html := trusted(readme)
	mustContain(t, html, `<p align="center">`)
	mustContain(t, html, `<img src="docs/logo.svg" alt="frznforge" width="120">`)
	mustContain(t, html, "<h2>frznforge</h2>")
	mustContain(t, html, "<h3>Install</h3>")
	mustContain(t, html, `<pre tabindex="0"><code class="language-bash">`)
	mustContain(t, html, "<table>")
	mustContain(t, html, `<a href="./docs/">docs</a>`)
}

/* ---- heading levels ------------------------------------------------------ */

// Rendered markdown never owns a page here: a README sits in a card whose head is already an
// <h2>, on a page whose <h1> is the repo. Letting user markdown emit <h1> gave those pages two
// <h1>s, and a README that is a lone title with no `##` in it — the most common README shape
// there is — stepped h1 → h3 straight into the About sidebar and failed heading-order.
func TestHeadingsAreDemotedOneLevel(t *testing.T) {
	html := trusted("# One\n\n## Two\n\n### Three\n\n#### Four\n\n##### Five\n")
	for _, want := range []string{"<h2>One</h2>", "<h3>Two</h3>", "<h4>Three</h4>", "<h5>Four</h5>", "<h6>Five</h6>"} {
		mustContain(t, html, want)
	}
	mustNotContain(t, html, "<h1")
}

func TestHeadingsClampAtH6(t *testing.T) {
	html := trusted("###### Six\n")
	mustContain(t, html, "<h6>Six</h6>")
	mustNotContain(t, html, "<h7")
}

func TestSetextHeadingsAreDemotedToo(t *testing.T) {
	html := trusted("Title\n=====\n\nSub\n---\n")
	mustContain(t, html, "<h2>Title</h2>")
	mustContain(t, html, "<h3>Sub</h3>")
}

func TestUntrustedHeadingsAreDemoted(t *testing.T) {
	mustContain(t, untrusted("# Imported\n"), "<h2>Imported</h2>")
}

/* ---- code blocks --------------------------------------------------------- */

// .hf-md pre is overflow-x: auto, so a long line makes it a scrollable region — and a
// scrollable region with nothing focusable in it cannot be scrolled without a mouse in Firefox
// or Safari (axe scrollable-region-focusable, WCAG 2.1.1).
func TestCodeBlocksAreKeyboardFocusable(t *testing.T) {
	html := trusted("```bash\nnpm test\n```\n")
	mustContain(t, html, `<pre tabindex="0"><code class="language-bash">`)
	mustNotContain(t, html, "<pre>")

	indented := untrusted("    indented code\n")
	mustContain(t, indented, `<pre tabindex="0"><code>`)
	mustNotContain(t, indented, "<pre>")
}

func TestCodeBlockContentIsEscaped(t *testing.T) {
	html := untrusted("```html\n<script>alert(1)</script>\n```\n")
	mustNotContain(t, html, "<script>")
	mustContain(t, html, "&lt;script&gt;alert(1)&lt;/script&gt;")
}

/* ---- mermaid ------------------------------------------------------------- */

const mermaidFence = "```mermaid\ngraph TD;\n  A-->B;\n```\n"

func TestMermaidContainerOnTrustedContent(t *testing.T) {
	html := trusted(mermaidFence)
	mustContain(t, html, `<pre class="hf-mermaid" tabindex="0"><code class="language-mermaid">`)
	mustContain(t, html, "A--&gt;B;")
	if !ContainsMermaid(html) {
		t.Error("ContainsMermaid said no")
	}
}

// The container's text IS the diagram source, escaped, so a reader without JavaScript gets an
// honest code block and nothing in the fence can execute at build time.
func TestMermaidSourceIsEscaped(t *testing.T) {
	html := trusted("```mermaid\ngraph TD;\n  A[\"</code></pre><script>x</script>\"]\n```\n")
	mustNotContain(t, html, "<script>")
	mustContain(t, html, "&lt;script&gt;")
	mustNotContain(t, html, "</code></pre><script")
}

// Importing a repo is the owner choosing to publish its content, so its diagrams render like
// everything else — without weakening anything else about the untrusted engine.
func TestMermaidRendersForUntrustedContentToo(t *testing.T) {
	html := untrusted(mermaidFence + "\n<script>alert(1)</script>\n")
	mustContain(t, html, `<pre class="hf-mermaid" tabindex="0"><code class="language-mermaid">`)
	mustContain(t, html, "A--&gt;B;")
	mustNotContain(t, html, "<script>")
	if !ContainsMermaid(html) {
		t.Error("ContainsMermaid said no")
	}
}

func TestMermaidGateOff(t *testing.T) {
	html := Render(mermaidFence, Options{Trusted: true})
	mustNotContain(t, html, "hf-mermaid")
	mustContain(t, html, `<code class="language-mermaid">`)
	if ContainsMermaid(html) {
		t.Error("ContainsMermaid said yes with the gate off")
	}
}

func TestMermaidMatchesTheInfoStringsFirstWordOnly(t *testing.T) {
	if !ContainsMermaid(trusted("```mermaid theme=neutral\ngraph TD;\n```\n")) {
		t.Error("an info string with options should still be a diagram")
	}
	if !ContainsMermaid(trusted("```mermaid\tthemeVariables\ngraph TD;\n```\n")) {
		t.Error("a tab-separated info string should still be a diagram")
	}
	if ContainsMermaid(trusted("```js\nmermaid.render()\n```\n")) {
		t.Error("a js fence mentioning mermaid is not a diagram")
	}
	mustContain(t, trusted("```bash\necho hi\n```\n"), `<pre tabindex="0">`)
}

// A fence whose *content* names the container class stays escaped and undetected: its quotes
// come out as &quot;.
func TestMermaidCannotBeFakedFromEscapedText(t *testing.T) {
	if ContainsMermaid(trusted("```txt\n<pre class=\"hf-mermaid\">\n```\n")) {
		t.Error("escaped fence text faked a mermaid container")
	}
}

// The TypeScript carried the mermaid gate in a module-level boolean, safe only because marked
// parses synchronously. This one travels in the parse context, so two renders cannot see each
// other's setting — which is what makes concurrent page rendering safe later.
func TestConcurrentRendersDoNotShareTheMermaidGate(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				if !ContainsMermaid(Render(mermaidFence, Options{Trusted: true, Mermaid: true})) {
					t.Error("gate-on render lost its container")
				}
				return
			}
			if ContainsMermaid(Render(mermaidFence, Options{Trusted: true})) {
				t.Error("gate-off render grew a container")
			}
		}(i)
	}
	wg.Wait()
}

/* ---- untrusted ----------------------------------------------------------- */

// The payload shape tests/e2e/releases.spec.ts asserts against: script tags, event handlers and
// javascript: URLs never reach the page, while the legitimate part of the notes still renders.
func TestUntrustedDropsRawHTML(t *testing.T) {
	html := untrusted(strings.Join([]string{
		"# Release 1.0",
		"",
		"<script>window.__PWNED = 1</script>",
		"",
		`inline <img src=x onerror="window.__PWNED2 = 1"> tag`,
		"",
		`<iframe src="https://evil.test"></iframe>`,
		"",
		"Not for production use.",
	}, "\n") + "\n")

	for _, unwanted := range []string{"<script", "onerror", "<iframe", "<img", "__PWNED"} {
		mustNotContain(t, html, unwanted)
	}
	// goldmark's own answer to raw HTML with Unsafe off is a placeholder comment; we drop it.
	mustNotContain(t, html, "raw HTML omitted")
	mustContain(t, html, "<h2>Release 1.0</h2>")
	mustContain(t, html, "inline")
	mustContain(t, html, "Not for production use.")
}

func TestUntrustedNeutralisesScriptBearingURLs(t *testing.T) {
	html := untrusted(strings.Join([]string{
		"[click](javascript:alert(1))",
		"",
		"[click](JaVaScRiPt&#58;alert(1))",
		"",
		"![x](data:text/html;base64,PHNjcmlwdD4=)",
		"",
		"[ok](https://example.test/page) and [rel](./docs/a.md) and [frag](#top)",
	}, "\n") + "\n")

	if strings.Contains(strings.ToLower(html), `href="javascript`) {
		t.Errorf("javascript: href survived:\n%s", html)
	}
	if strings.Contains(strings.ToLower(html), `src="data:`) {
		t.Errorf("data: src survived:\n%s", html)
	}
	mustContain(t, html, `href="https://example.test/page"`)
	mustContain(t, html, `href="./docs/a.md"`)
	mustContain(t, html, `href="#top"`)
}

// <javascript:alert(1)> is a valid CommonMark autolink. marked routed autolinks through the
// same link renderer the sanitiser overrode; goldmark gives them their own node, so they need
// handling of their own or they become the one URL that never gets filtered.
func TestUntrustedNeutralisesAutolinks(t *testing.T) {
	html := untrusted("<javascript:alert(1)>\n\n<https://ok.test/a>\n\n<mailto:a@b.test>\n")
	if strings.Contains(strings.ToLower(html), "href=\"javascript") {
		t.Errorf("javascript: autolink survived:\n%s", html)
	}
	mustContain(t, html, `<a href="#">javascript:alert(1)</a>`)
	mustContain(t, html, `href="https://ok.test/a"`)
	mustContain(t, html, "a@b.test")
}

func TestUntrustedEscapesHTMLMetacharactersInText(t *testing.T) {
	mustNotContain(t, untrusted("a < b & c > d\n"), "a < b")
	mustContain(t, untrusted("`<script>`\n"), "&lt;script&gt;")
}

/* ---- GFM ----------------------------------------------------------------- */

func TestGFMExtensions(t *testing.T) {
	html := trusted(strings.Join([]string{
		"| a | b |",
		"| - | - |",
		"| 1 | 2 |",
		"",
		"~~gone~~",
		"",
		"https://plain.test/x",
		"",
		"- [x] done",
		"- [ ] todo",
	}, "\n") + "\n")

	mustContain(t, html, "<table>")
	mustContain(t, html, "<th>a</th>")
	mustContain(t, html, "<del>gone</del>")
	mustContain(t, html, `<a href="https://plain.test/x">`)
	mustContain(t, html, `type="checkbox"`)
	mustContain(t, html, `checked=""`)
}

/* ---- SafeURL ------------------------------------------------------------- */

func TestSafeURLAllowsSafeAndRelativeTargets(t *testing.T) {
	for _, url := range []string{"https://a.test/x", "http://a.test", "mailto:a@b.test", "/abs", "./rel", "#frag", "a/b.png"} {
		if got := SafeURL(url); got != url {
			t.Errorf("SafeURL(%q) = %q, want it unchanged", url, got)
		}
	}
}

func TestSafeURLRejectsExecutableSchemesHoweverSpelled(t *testing.T) {
	for _, url := range []string{
		"javascript:alert(1)",
		"JAVASCRIPT:alert(1)",
		"java\tscript:alert(1)",
		" javascript:alert(1)",
		"&#106;avascript:alert(1)",
		"&#x6a;avascript:alert(1)",
		"java&#9;script:alert(1)",
		"java&Tab;script:alert(1)",
		"java script:alert(1)",
		"vbscript:msgbox(1)",
		"data:text/html,<script>x</script>",
		"file:///etc/passwd",
	} {
		if got := SafeURL(url); got != "#" {
			t.Errorf("SafeURL(%q) = %q, want %q", url, got, "#")
		}
	}
}

/* ---- helpers ------------------------------------------------------------- */

func TestIsTrustedSource(t *testing.T) {
	if !IsTrustedSource(model.RepoSource{Type: "local"}) {
		t.Error("a local repo is the owner's own content")
	}
	for _, typ := range []string{"github", "gitlab", "gitea", "forgejo"} {
		if IsTrustedSource(model.RepoSource{Type: typ}) {
			t.Errorf("%s repos are not trusted", typ)
		}
	}
}

func TestIsMarkdownPath(t *testing.T) {
	for _, p := range []string{"docs/a.md", "README", "readme", "a.MARKDOWN", "x/y.mdown", "z.mkd"} {
		if !IsMarkdownPath(p) {
			t.Errorf("IsMarkdownPath(%q) = false", p)
		}
	}
	for _, p := range []string{"src/a.ts", "readme.txt", "mdown", "a.md.bak"} {
		if IsMarkdownPath(p) {
			t.Errorf("IsMarkdownPath(%q) = true", p)
		}
	}
}
