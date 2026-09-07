// Highlighting: the language map's coverage, the markup the blob page and its line anchors
// depend on, and the trailing-newline rule the gutter and the "N lines" label share.
package highlight

import (
	"frznforge/internal/ingest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2/lexers"
)

/* ---- the language map ---------------------------------------------------- */

// ingestLanguageNames reads testdata/ingest-languages.txt: every language name the artifact can
// carry.
func ingestLanguageNames(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "ingest-languages.txt"))
	if err != nil {
		t.Fatalf("reading the ingest language list: %v", err)
	}
	var names []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		names = append(names, line)
	}
	if len(names) == 0 {
		t.Fatal("the ingest language list is empty")
	}
	return names
}

// TestLanguageMapCoversIngest is the test the Shiki table never had.
//
// LANGUAGE_TO_SHIKI was a partial list, so a language ingest could emit but the table did not
// name simply rendered uncoloured — a silent, invisible failure that survived 51 of 85 entries
// and one casing slip ('Ini' against ingest's 'INI'). Coverage is asserted in BOTH directions:
// a name with no entry is a gap, and an entry for a name ingest cannot emit is dead weight that
// will outlive whoever added it.
func TestLanguageMapCoversIngest(t *testing.T) {
	names := ingestLanguageNames(t)
	known := make(map[string]bool, len(names))
	for _, name := range names {
		known[name] = true
		if _, ok := LanguageToChroma[name]; !ok {
			t.Errorf("ingest can emit %q and LanguageToChroma has no entry for it — map it to a "+
				"chroma lexer, or to \"\" with a comment saying why it has none", name)
		}
	}
	for name := range LanguageToChroma {
		if !known[name] {
			t.Errorf("LanguageToChroma has %q, which ingest cannot emit — a dead entry", name)
		}
	}
}

// TestLanguageMapResolves walks every mapped lexer name and asserts chroma resolves it to a
// lexer that actually claims that name or alias.
//
// A plain "lexers.Get() != nil" would not do: Get falls back to matching the string as a
// filename, so a typo like "Pyhton" can come back with some lexer attached and look fine.
func TestLanguageMapResolves(t *testing.T) {
	for language, chromaName := range LanguageToChroma {
		if chromaName == "" {
			continue
		}
		lexer := lexers.Get(chromaName)
		if lexer == nil {
			t.Errorf("%s: chroma has no lexer named %q", language, chromaName)
			continue
		}
		cfg := lexer.Config()
		claimed := strings.EqualFold(cfg.Name, chromaName)
		for _, alias := range cfg.Aliases {
			if strings.EqualFold(alias, chromaName) {
				claimed = true
			}
		}
		if !claimed {
			t.Errorf("%s: %q resolved to lexer %q (aliases %v) — that is chroma guessing from a "+
				"filename, not a real name", language, chromaName, cfg.Name, cfg.Aliases)
		}
	}
}

// TestPlainTextLanguagesAreDeliberate pins the languages that render uncoloured.
//
// The point is that the list can only grow by someone editing it here, next to the reason. An
// unmapped language is a test failure; a plain-text one is a decision.
func TestPlainTextLanguagesAreDeliberate(t *testing.T) {
	want := []string{
		"AsciiDoc",       // no chroma lexer
		"Git Attributes", // no chroma lexer
		"Ignore List",    // no chroma lexer
		"Less",           // no chroma lexer; the CSS one mis-reads Less badly
		"Lockfile",       // no single syntax across lockfile formats
		"Pug",            // no chroma lexer
		"Text",           // plain by definition
		"WebAssembly",    // no chroma lexer for .wat/.wast
	}
	var got []string
	for language, chromaName := range LanguageToChroma {
		if chromaName == "" {
			got = append(got, language)
		}
	}
	sort.Strings(got)
	if strings.Join(got, ", ") != strings.Join(want, ", ") {
		t.Errorf("plain-text languages changed:\n got: %v\nwant: %v", got, want)
	}
}

// TestIngestLanguageListIsCurrent keeps the checked-in fixture honest against the real map.
//
// The fixture is what TestLanguageMapCoversIngest compares the highlighter against, so a stale
// fixture makes that test pass while a language ingest can actually emit goes uncoloured — which
// is exactly how `.cfg` and `.conf` shipped with no syntax colouring for a version.
//
// It used to derive the list by parsing src/lib/ingest/languages.ts. Phase 9 deleted that file
// and this test began skipping instead of failing, so it covered nothing from then on. It reads
// ingest.LanguageNames() now: the map itself rather than a copy of it, which is both simpler and
// a stronger claim.
func TestIngestLanguageListIsCurrent(t *testing.T) {
	fromIngest := ingest.LanguageNames()
	fixture := ingestLanguageNames(t)
	sort.Strings(fixture)
	if strings.Join(fromIngest, "\n") != strings.Join(fixture, "\n") {
		t.Errorf("testdata/ingest-languages.txt is out of date with internal/ingest's language map\n"+
			"in the map only: %v\nin the fixture only: %v",
			missing(fromIngest, fixture), missing(fixture, fromIngest))
	}
}

func missing(from, in []string) []string {
	have := make(map[string]bool, len(in))
	for _, s := range in {
		have[s] = true
	}
	var out []string
	for _, s := range from {
		if !have[s] {
			out = append(out, s)
		}
	}
	return out
}

/* ---- LexerName ----------------------------------------------------------- */

func TestLexerNamePrefersTheLanguage(t *testing.T) {
	if got := LexerName("Go", "main.go"); got != "Go" {
		t.Errorf("LexerName(Go) = %q", got)
	}
	// SVG is XML on purpose, and the language wins over what the .svg extension would say.
	if got := LexerName("SVG", "logo.svg"); got != "XML" {
		t.Errorf("LexerName(SVG) = %q, want XML", got)
	}
}

// A language with no chroma lexer still gets a chance from the file name — which is how
// package-lock.json (language "Lockfile") comes out as JSON rather than grey.
func TestLexerNameFallsBackToTheFileName(t *testing.T) {
	if got := LexerName("Lockfile", "package-lock.json"); got != "JSON" {
		t.Errorf("LexerName(Lockfile, package-lock.json) = %q, want JSON", got)
	}
	if got := LexerName("", "script.py"); got != "Python" {
		t.Errorf("LexerName(\"\", script.py) = %q, want Python", got)
	}
	// A full path must not confuse the matcher: only the base name is a file name.
	if got := LexerName("", "deep/nested/dir/script.py"); got != "Python" {
		t.Errorf("LexerName on a nested path = %q, want Python", got)
	}
	if got := LexerName("Text", "NOTICE"); got != "" {
		t.Errorf("LexerName(Text, NOTICE) = %q, want plain text", got)
	}
}

/* ---- CountLines ---------------------------------------------------------- */

// The editor rule: a trailing newline does not open a new line. The blob page's gutter and its
// "N lines" label both read this, and the insights code-size series cites the same rule, so all
// three have to agree.
func TestCountLines(t *testing.T) {
	cases := []struct {
		code string
		want int
	}{
		{"", 0},
		{"a", 1},
		{"a\n", 1},
		{"a\nb", 2},
		{"a\nb\n", 2},
		{"a\nb\n\n", 3},
		{"\n", 1},
		{"\n\n", 2},
		// CRLF and lone CR are line breaks too, and are counted as the one break they are.
		{"a\r\nb\r\n", 2},
		{"a\r\nb", 2},
		{"a\rb", 2},
		{"a\rb\r", 2},
		{"a\r", 1},
		{"\r", 1},
	}
	for _, c := range cases {
		if got := CountLines(c.code); got != c.want {
			t.Errorf("CountLines(%q) = %d, want %d", c.code, got, c.want)
		}
	}
}

/* ---- Highlight ----------------------------------------------------------- */

var lineIDRe = regexp.MustCompile(`<span class="line" id="([^"]*)">`)

func lineIDs(html string) []string {
	var out []string
	for _, m := range lineIDRe.FindAllStringSubmatch(html, -1) {
		out = append(out, m[1])
	}
	return out
}

// The gutter is a CSS counter over these spans and the label comes from CountLines, so one span
// per counted line is the whole contract. A file that ends with a newline — almost every file —
// is the case that breaks it.
func TestHighlightLineCountMatchesCountLines(t *testing.T) {
	sources := []string{
		"package main\n\nfunc main() {}\n",
		"package main\n\nfunc main() {}",
		"one line",
		"one line\n",
		"a\n\n\nb\n",
	}
	for _, src := range sources {
		html := Highlight(src, "Go", "main.go", "")
		ids := lineIDs(html)
		if want := CountLines(src); len(ids) != want {
			t.Errorf("%q: %d line spans, CountLines says %d\n%s", src, len(ids), want, html)
		}
	}
}

func TestHighlightLineIDsAreSequential(t *testing.T) {
	html := Highlight("a := 1\nb := 2\nc := 3\n", "Go", "main.go", "")
	want := []string{"L1", "L2", "L3"}
	if got := lineIDs(html); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("line ids = %v, want %v", got, want)
	}
	if !strings.HasPrefix(html, `<pre class="hf-chroma" tabindex="0"><code>`) {
		t.Errorf("wrapper changed: %s", html[:min(80, len(html))])
	}
	if !strings.HasSuffix(html, "</code></pre>") {
		t.Errorf("wrapper changed at the end: %s", html)
	}
}

// A multi-file note page renders one card per file. Without a per-file prefix every card
// emitted id="L1"…"L30" again, so #L5 resolved to the first file only and every later file's
// lines were permanently unlinkable.
func TestHighlightIDPrefixKeepsMultipleBlocksDistinct(t *testing.T) {
	first := Highlight("root = true\n[*]\nindent_size = 2\n", "EditorConfig", ".editorconfig", "f-editorconfig-")
	second := Highlight("[build]\ncommand = \"npm run build\"\n", "TOML", "netlify.toml", "f-netlify-toml-")

	wantFirst := []string{"f-editorconfig-L1", "f-editorconfig-L2", "f-editorconfig-L3"}
	if got := lineIDs(first); strings.Join(got, ",") != strings.Join(wantFirst, ",") {
		t.Errorf("first block ids = %v, want %v", got, wantFirst)
	}
	wantSecond := []string{"f-netlify-toml-L1", "f-netlify-toml-L2"}
	if got := lineIDs(second); strings.Join(got, ",") != strings.Join(wantSecond, ",") {
		t.Errorf("second block ids = %v, want %v", got, wantSecond)
	}
	for _, id := range lineIDs(first) {
		if strings.Contains(second, `id="`+id+`"`) {
			t.Errorf("id %q appears in both blocks", id)
		}
	}
	// An empty prefix is the documented single-file case and must still give bare L1.
	if got := lineIDs(Highlight("x\n", "TOML", "a.toml", "")); got[0] != "L1" {
		t.Errorf("empty prefix gave %q, want L1", got[0])
	}
}

func TestHighlightEscapesSource(t *testing.T) {
	html := Highlight("const s = \"<script>alert('x')</script>\";\n", "JavaScript", "a.js", "")
	if strings.Contains(html, "<script>") {
		t.Errorf("a script tag reached the page:\n%s", html)
	}
	for _, want := range []string{"&lt;script&gt;", "&quot;"} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %q in:\n%s", want, html)
		}
	}
}

func TestHighlightEmitsTokenClassesNotInlineStyles(t *testing.T) {
	html := Highlight("// a comment\nfunc f() {}\n", "Go", "a.go", "")
	if strings.Contains(html, "style=") {
		t.Errorf("inline styles are an owner-level no: %s", html)
	}
	if !strings.Contains(html, `class="hf-c1"`) && !strings.Contains(html, `class="hf-c"`) {
		t.Errorf("the comment got no comment class:\n%s", html)
	}
	// Every emitted token class must carry the site's prefix, so nothing collides with a
	// stylesheet that is not ours.
	for _, m := range regexp.MustCompile(`<span class="([^"]+)">`).FindAllStringSubmatch(html, -1) {
		if m[1] != "line" && !strings.HasPrefix(m[1], "hf-") {
			t.Errorf("unprefixed class %q", m[1])
		}
	}
}

// Plain text still gets the full line structure — the gutter, the ids and the anchors are the
// same on an uncoloured file as on a coloured one.
func TestHighlightPlainTextKeepsTheMarkup(t *testing.T) {
	html := Highlight("just words\nand more <words>\n", "Text", "notes.txt", "")
	if got := lineIDs(html); strings.Join(got, ",") != "L1,L2" {
		t.Errorf("plain-text line ids = %v", got)
	}
	if strings.Contains(html, `<span class="hf-`) {
		t.Errorf("plain text should carry no token spans:\n%s", html)
	}
	if !strings.Contains(html, "and more &lt;words&gt;") {
		t.Errorf("plain text was not escaped:\n%s", html)
	}
}

// A repository committed from Windows must get the same gutter, ids and line count as one
// committed from Linux — and no stray carriage returns in the markup.
func TestHighlightNormalisesCRLF(t *testing.T) {
	crlf := "x = 1\r\ny = 2\r\n"
	html := Highlight(crlf, "Python", "a.py", "")
	if got := lineIDs(html); strings.Join(got, ",") != "L1,L2" {
		t.Errorf("CRLF line ids = %v", got)
	}
	if strings.Contains(html, "\r") {
		t.Errorf("a carriage return survived into the markup: %q", html)
	}
	if CountLines(crlf) != 2 {
		t.Errorf("CountLines on CRLF = %d, want 2", CountLines(crlf))
	}
}

// One line span per counted line, whatever the file's line endings.
//
// This is the invariant the gutter, the "N lines" label and the #L anchors all stand on, and
// CRLF alone does not test it: "\r\n" carries a "\n", so a count that looks only for "\n" still
// agrees. A lone CR — classic Mac endings, and anything a generator emits with a bare "\r" — is
// what pulls the two apart, and CSS `white-space: pre` breaks the line whether or not the
// counter agrees.
func TestLineSpansMatchCountLinesForEveryLineEnding(t *testing.T) {
	for _, src := range []string{
		"a\nb\n", "a\nb", "a\r\nb\r\n", "a\r\nb", "a\rb", "a\rb\r", "a\r", "\r",
		"x\ny\rz\r\nw\n", "a\r\r\nb",
	} {
		html := Highlight(src, "Text", "a.txt", "")
		if got, want := len(lineIDs(html)), CountLines(src); got != want {
			t.Errorf("%q: %d line spans, CountLines says %d", src, got, want)
		}
		if strings.Contains(html, "\r") {
			t.Errorf("%q: a carriage return survived into the markup", src)
		}
	}
}

// idPrefix lands in an id attribute, so it is escaped like every other input this package
// touches: callers slug it from a path inside an imported repository.
func TestHighlightEscapesTheIDPrefix(t *testing.T) {
	html := Highlight("x\n", "Text", "", `"><script>alert(1)</script><b id="`)
	if strings.Contains(html, "<script>") {
		t.Errorf("an id prefix broke out of its attribute:\n%s", html)
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Errorf("the prefix was not escaped:\n%s", html)
	}
}

// Empty input keeps one empty line span — matching what Shiki emitted, so the blob page's
// markup for a zero-byte file does not change under the new engine.
func TestHighlightEmptyInput(t *testing.T) {
	html := Highlight("", "Go", "main.go", "")
	if got := lineIDs(html); strings.Join(got, ",") != "L1" {
		t.Errorf("empty input line ids = %v, want [L1]", got)
	}
}

// Every mapped lexer has to survive a real render, not just resolve: a lexer that errors or
// loses lines would take the whole page down with it.
func TestEveryMappedLexerRendersASnippet(t *testing.T) {
	const snippet = "alpha beta\n\"gamma\" 42 # delta\n<tag attr=\"v\">\n"
	for _, language := range ingestLanguageNames(t) {
		language := language
		t.Run(language, func(t *testing.T) {
			html := Highlight(snippet, language, "", "")
			if got, want := len(lineIDs(html)), CountLines(snippet); got != want {
				t.Errorf("%d line spans, want %d\n%s", got, want, html)
			}
			if strings.Contains(html, "<tag") {
				t.Errorf("markup leaked out of the source:\n%s", html)
			}
		})
	}
}
