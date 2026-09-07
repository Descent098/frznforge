package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// repoTS is the TypeScript config the migration is checked against, frozen into testdata.
//
// It used to read ../../frznforge.config.ts, which Phase 9 deleted — so all three migration
// tests skipped on every machine, forever, for a feature 0.3.0 users need in order to upgrade at
// all. The frozen pair (this file and testdata/migrated-config.jsonc) is also the only honest
// subject now: the claim is "converting THIS produces THAT", and both halves have to hold still
// for that to mean anything.
func repoTS(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "migrated-config.ts"))
	if err != nil {
		t.Fatalf("read the frozen TypeScript config: %v", err)
	}
	return raw
}

// TestMigrateRepoConfig converts this repository's real config and checks the result against
// the committed .jsonc — not byte for byte, but value for value, which is the claim that
// matters: the two files say the same thing. It fails the moment someone edits one and forgets
// the other.
func TestMigrateRepoConfig(t *testing.T) {
	out, err := MigrateTS(repoTS(t))
	if err != nil {
		t.Fatalf("MigrateTS on the repo's own config: %v", err)
	}
	fresh, err := ParseBytes(out)
	if err != nil {
		t.Fatalf("the migration output does not parse: %v\n%s", err, out)
	}
	committed, err := ParseBytes(repoJSONC(t))
	if err != nil {
		t.Fatalf("the committed %s does not parse: %v", Filename, err)
	}
	if !reflect.DeepEqual(fresh, committed) {
		t.Errorf("the committed %s is out of date — re-run `frznforge config migrate --force`\n fresh: %s\n  file: %s",
			Filename, mustJSON(t, fresh), mustJSON(t, committed))
	}
}

// TestCommittedJSONCIsAFreshMigration is the byte-for-byte half of TestMigrateRepoConfig.
//
// That test compares parsed *values*, which is blind to exactly the thing this converter exists
// to protect: someone can delete a comment from the committed .jsonc, or reflow it, and every
// value-level check still passes. Comments are the configuration's documentation, so the
// committed file has to be what the converter actually produces, not merely something that
// happens to parse to the same struct.
func TestCommittedJSONCIsAFreshMigration(t *testing.T) {
	fresh, err := MigrateTS(repoTS(t))
	if err != nil {
		t.Fatalf("MigrateTS on the repo's own config: %v", err)
	}
	committed := repoJSONC(t)
	if string(fresh) == string(committed) {
		return
	}
	t.Errorf("the committed %s is not what the converter produces — re-run `frznforge config migrate --force`", Filename)
	freshLines, fileLines := strings.Split(string(fresh), "\n"), strings.Split(string(committed), "\n")
	for i := 0; i < len(freshLines) || i < len(fileLines); i++ {
		var a, b string
		if i < len(freshLines) {
			a = freshLines[i]
		}
		if i < len(fileLines) {
			b = fileLines[i]
		}
		if a != b {
			t.Errorf("line %d:\n fresh: %q\n  file: %q", i+1, a, b)
		}
	}
}

// TestCommentRetention is the reason this converter exists. frznforge.config.ts is roughly 60%
// comments, and they are the configuration's documentation: a migration that keeps the values
// and drops the prose turns 3 KB of explanation into 1 KB of data.
func TestCommentRetention(t *testing.T) {
	ts := repoTS(t)
	before, err := CountCommentLines(ts)
	if err != nil {
		t.Fatal(err)
	}
	out, err := MigrateTS(ts)
	if err != nil {
		t.Fatal(err)
	}
	after, err := CountCommentLines(out)
	if err != nil {
		t.Fatal(err)
	}
	if before == 0 {
		t.Fatal("the TypeScript config has no comments; this test is measuring nothing")
	}
	if pct := float64(after) * 100 / float64(before); pct < 90 {
		t.Errorf("comment retention is %.0f%% (%d of %d lines) — the converter is losing documentation", pct, after, before)
	}
	// The line count is a blunt instrument, so check that specific, distinctive sentences came
	// across intact rather than merely that *something* commented survived.
	text := string(out)
	for _, phrase := range []string{
		"frznforge site configuration.",
		"Reference: docs/user/configuration.md",
		"Markdown file rendered on the profile page",
		"'hearth' — warm: off-white / ember-tinted charcoal canvas",
		"Until you add repos here, the site builds with an empty listing.",
		"exercise the de-duplication",
		"would fall back to filesystem mtimes and give up byte-identical rebuilds",
		"Text files larger than this are listed but their content is not stored.",
	} {
		if !strings.Contains(text, phrase) {
			t.Errorf("comment lost: %q", phrase)
		}
	}
	// And that it is still a comment, not accidental data: stripping comments must remove them.
	stripped := string(StripJSONC(out))
	if strings.Contains(stripped, "Reference: docs/user/configuration.md") {
		t.Error("a comment survived StripJSONC — it was not emitted as a comment")
	}
}

// TestMigrateKeepsCommentsWhereTheyWere — a comment documents the key it sits on, so moving it
// (or collecting the lot at the top) would be almost as bad as dropping it.
func TestMigrateKeepsCommentsWhereTheyWere(t *testing.T) {
	src := `// header, kept above the object
import { defineConfig } from './src/lib/config/schema';

export default defineConfig({
  // a leading comment
  owner: {
    name: 'K', // trailing
    /* block { with braces } and a , comma */
    handle: 'kieran',
  },
});
`
	want := `// header, kept above the object

{
  // a leading comment
  "owner": {
    "name": "K", // trailing
    /* block { with braces } and a , comma */
    "handle": "kieran",
  },
}
`
	got, err := MigrateTS([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("\n got:\n%s\nwant:\n%s", got, want)
	}
}

// TestMigrateStrings — every string is decoded and re-quoted, because JSON has neither single
// quotes nor \' and would otherwise inherit an escape it cannot read.
func TestMigrateStrings(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"single quotes", `'hello'`, `"hello"`},
		{"escaped single quote", `'doesn\'t'`, `"doesn't"`},
		{"double quote inside single quotes", `'say "hi"'`, `"say \"hi\""`},
		{"a URL keeps its slashes", `'https://example.com//x'`, `"https://example.com//x"`},
		{"backslash", `'C:\\tmp'`, `"C:\\tmp"`},
		{"newline escape", `'a\nb'`, `"a\nb"`},
		{"unicode escape", `'\u00e9t\u00e9'`, `"été"`},
		{"surrogate pair", `'\ud83d\ude00'`, `"😀"`},
		{"template literal without a placeholder", "`plain`", `"plain"`},
		{"HTML is not escaped", `'a <b> & c'`, `"a <b> & c"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := migrateValue(t, tc.in)
			if got != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}

// TestMigrateFoldsNumericExpressions — `512 * 1024` is unreadable as 524288 and illegal as an
// expression, so it folds AND keeps what was written as a trailing comment.
func TestMigrateFoldsNumericExpressions(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"a plain number needs no note", `50`, `50`},
		{"kilobytes", `512 * 1024`, `524288 // 512 * 1024`},
		{"megabytes", `20 * 1024 * 1024`, `20971520 // 20 * 1024 * 1024`},
		{"parentheses and precedence", `(2 + 3) * 4`, `20 // (2 + 3) * 4`},
		{"precedence without parentheses", `2 + 3 * 4`, `14 // 2 + 3 * 4`},
		{"subtraction", `1024 - 24`, `1000 // 1024 - 24`},
		{"exact division", `4096 / 4`, `1024 // 4096 / 4`},
		{"negative", `-1`, `-1`},
		{"digit separators", `1_000`, `1000 // 1_000`},
		{"hex", `0x400`, `1024 // 0x400`},
		{"a fraction stays a fraction", `1 / 2`, `0.5 // 1 / 2`},
		{"whitespace is normalised in the note", "512\n    * 1024", `524288 // 512 * 1024`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := migrateValue(t, tc.in)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestMigrateFoldNoteJoinsAnExistingComment — the note must not silently overwrite, or land
// after, a comment the author already put on that line.
func TestMigrateFoldNoteJoinsAnExistingComment(t *testing.T) {
	out := migrateBody(t, "  maxBlobBytes: 512 * 1024, // the cap\n")
	want := `  "maxBlobBytes": 524288, // the cap (512 * 1024)`
	if !strings.Contains(out, want) {
		t.Errorf("got:\n%s\nwant a line containing:\n%s", out, want)
	}
}

// TestMigrateFoldNotesShareALine — two expressions can fold onto one line, and one trailing
// comment has to carry both. Replacing the first note with the second would drop the expression
// the author wrote with nothing left pointing at it.
func TestMigrateFoldNotesShareALine(t *testing.T) {
	out := migrateBody(t, "  a: 512 * 1024, b: 2 * 3,\n")
	for _, want := range []string{"512 * 1024", "2 * 3"} {
		if !strings.Contains(out, want) {
			t.Errorf("the note %q was dropped:\n%s", want, out)
		}
	}
}

// TestMigrateFoldNoteSkipsBlockComments — the note is flushed at the end of an output line, and
// a multi-line block comment has line ends inside it. Writing the note there would bury it in
// the author's prose and edit a comment this converter copies verbatim.
func TestMigrateFoldNoteSkipsBlockComments(t *testing.T) {
	out := migrateBody(t, "  a: 512 * 1024, /* an author's\n  multi-line note */\n  b: 1,\n")
	if strings.Contains(out, "an author's // 512 * 1024") {
		t.Errorf("the fold note was written inside a block comment:\n%s", out)
	}
	if !strings.Contains(out, "an author's") || !strings.Contains(out, "multi-line note") {
		t.Errorf("the author's comment was mangled:\n%s", out)
	}
	if !strings.Contains(out, "512 * 1024") {
		t.Errorf("the fold note was dropped:\n%s", out)
	}
}

// TestMigrateImportWithoutSemicolon — `semi: false` is a common Prettier setting, and the
// import it produces is valid TypeScript. Refusing it would block a migration that is otherwise
// entirely convertible.
func TestMigrateImportWithoutSemicolon(t *testing.T) {
	src := "import { defineConfig } from './src/lib/config/schema'\n\nexport default defineConfig({\n  owner: { name: 'K', handle: 'kieran' },\n  listing: { pageSize: 50 },\n})\n"
	out, err := MigrateTS([]byte(src))
	if err != nil {
		t.Fatalf("an import without a trailing `;` must still convert: %v", err)
	}
	cfg, err := ParseBytes(out)
	if err != nil {
		t.Fatalf("the output does not parse: %v\n%s", err, out)
	}
	if cfg.Listing.PageSize != 50 {
		t.Errorf("pageSize: %d", cfg.Listing.PageSize)
	}
	if strings.Contains(string(out), "defineConfig") || strings.Contains(string(out), "import") {
		t.Errorf("the import wrapper survived into the output:\n%s", out)
	}
}

// TestMigrateRefusesWhatItCannotConvert is the safety property: a config that needs JavaScript
// to produce its values has to stop with the line number, never guess. A silently wrong
// maxBlobBytes is a truncated site nobody notices for a month.
func TestMigrateRefusesWhatItCannotConvert(t *testing.T) {
	cases := map[string]struct {
		src      string
		wantLine string
	}{
		"an environment variable":       {"export default defineConfig({\n  owner: {\n    name: process.env.NAME,\n  },\n});\n", "line 3"},
		"a function call":               {"export default defineConfig({\n  owner: { name: makeName() },\n});\n", "line 2"},
		"a template placeholder":        {"export default defineConfig({\n  site: { title: `forge ${version}` },\n});\n", "line 2"},
		"a spread":                      {"export default defineConfig({\n  ...shared,\n  owner: { name: 'K' },\n});\n", "line 2"},
		"undefined":                     {"export default defineConfig({\n  site: { url: undefined },\n});\n", "line 2"},
		"shorthand property":            {"export default defineConfig({\n  site,\n});\n", "line 2"},
		"a variable in an expression":   {"export default defineConfig({\n  ingest: {\n    maxBlobBytes: 512 * KB,\n  },\n});\n", "line 3"},
		"a computed key":                {"export default defineConfig({\n  [key]: 1,\n});\n", "line 2"},
		"an unterminated block comment": {"export default defineConfig({\n  /* never closed\n  site: {},\n});\n", "line 2"},
		"an unterminated string":        {"export default defineConfig({\n  site: { title: 'oops },\n});\n", "line 2"},
		"a stray statement":             {"const KB = 1024;\nexport default defineConfig({});\n", "line 1"},
		"no config object at all":       {"// just a comment\n", "no config object"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := MigrateTS([]byte(tc.src))
			if err == nil {
				t.Fatalf("expected an error, got:\n%s", out)
			}
			if !strings.Contains(err.Error(), tc.wantLine) {
				t.Errorf("the error must point at %s, got: %v", tc.wantLine, err)
			}
		})
	}
}

// TestMigrateHandlesBOMAndCRLF — the two things a Windows editor adds to a file without asking.
func TestMigrateHandlesBOMAndCRLF(t *testing.T) {
	body := "// a header comment\r\nimport { defineConfig } from './src/lib/config/schema';\r\n\r\nexport default defineConfig({\r\n  // a comment\r\n  owner: { name: 'K', handle: 'kieran' },\r\n});\r\n"
	src := append([]byte{0xEF, 0xBB, 0xBF}, []byte(body)...)

	out, err := MigrateTS(src)
	if err != nil {
		t.Fatalf("MigrateTS: %v", err)
	}
	if HasBOM(out) {
		t.Error("the BOM was copied into the output; it should be dropped")
	}
	if !strings.Contains(string(out), "\r\n") {
		t.Errorf("CRLF endings were not preserved:\n%q", out)
	}
	if strings.Contains(strings.ReplaceAll(string(out), "\r\n", ""), "\n") {
		t.Errorf("line endings came out mixed:\n%q", out)
	}
	cfg, err := ParseBytes(out)
	if err != nil {
		t.Fatalf("the CRLF output does not parse: %v", err)
	}
	if cfg.Owner.Handle != "kieran" {
		t.Errorf("handle: %q", cfg.Owner.Handle)
	}
}

// TestCountCommentLinesIsStringAware — the retention percentage would be a lie if a `//` inside
// a URL counted as a comment.
func TestCountCommentLinesIsStringAware(t *testing.T) {
	n, err := CountCommentLines([]byte("{\n  \"url\": \"https://example.com\",\n  // one real comment\n  /* two\n     three */\n}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("expected 3 commented lines (one //, two of a block), got %d", n)
	}
}

/* ---- helpers -------------------------------------------------------------- */

// migrateValue runs one value through the converter and returns what it became, trailing fold
// comment included. The value is written without a trailing comma so the result is the whole
// rest of the line.
func migrateValue(t *testing.T, value string) string {
	t.Helper()
	out := migrateBody(t, "  x: "+value+"\n")
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, `  "x":`) {
			return strings.TrimSpace(strings.TrimPrefix(line, `  "x":`))
		}
	}
	t.Fatalf("no converted value in:\n%s", out)
	return ""
}

func migrateBody(t *testing.T, body string) string {
	t.Helper()
	out, err := MigrateTS([]byte("export default defineConfig({\n" + body + "});\n"))
	if err != nil {
		t.Fatalf("MigrateTS(%q): %v", body, err)
	}
	return string(out)
}
