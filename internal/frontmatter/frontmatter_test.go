package frontmatter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestMatchesTypeScript is the cross-language sync test for the frontmatter subset.
//
// testdata/expected.json is produced by the TypeScript implementation itself
// (`npx tsx scripts/dump-frontmatter.ts internal/frontmatter/testdata/expected.json`), so it is
// the reference and this is the check. The cases cover the things this parser is deliberately
// strict about: an unterminated block, the `...` terminator, nested mappings and nested
// sequences (both must DROP the key rather than half-parse it), quoting, comments that are and
// are not comments, CRLF and a BOM.
//
// Regenerate with that command if the cases change; a diff here is a real difference.
func TestMatchesTypeScript(t *testing.T) {
	var golden struct {
		Parse []struct {
			Name    string                     `json:"name"`
			Src     string                     `json:"src"`
			Present bool                       `json:"present"`
			Raw     string                     `json:"raw"`
			Body    string                     `json:"body"`
			Data    map[string]json.RawMessage `json:"data"`
		} `json:"parse"`
		FirstHeading []struct {
			Name string  `json:"name"`
			Body string  `json:"body"`
			Out  *string `json:"out"`
		} `json:"firstHeading"`
		StripLeadingHeading []struct {
			Name  string `json:"name"`
			Body  string `json:"body"`
			Title string `json:"title"`
			Out   string `json:"out"`
		} `json:"stripLeadingHeading"`
	}

	raw, err := os.ReadFile(filepath.Join("testdata", "expected.json"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("parse golden: %v", err)
	}
	if len(golden.Parse) == 0 {
		t.Fatal("golden is empty — a vacuous pass is worse than no test")
	}

	for _, c := range golden.Parse {
		t.Run("parse/"+c.Name, func(t *testing.T) {
			split := SplitFile(c.Src)
			if split.Present != c.Present {
				t.Errorf("present = %v, TypeScript says %v", split.Present, c.Present)
			}
			if split.Raw != c.Raw {
				t.Errorf("raw:\n got %q\nwant %q", split.Raw, c.Raw)
			}
			fm := Parse(c.Src)
			if fm.Body != c.Body {
				t.Errorf("body:\n got %q\nwant %q", fm.Body, c.Body)
			}
			// Compare the key sets first: a key that should have been DROPPED but survived is
			// the failure mode this package exists to prevent.
			if len(fm.Data) != len(c.Data) {
				t.Errorf("keys: got %v, TypeScript produced %v", keysOf(fm.Data), rawKeysOf(c.Data))
			}
			for key, wantRaw := range c.Data {
				got, ok := fm.Data[key]
				if !ok {
					t.Errorf("missing key %q", key)
					continue
				}
				var wantList []string
				if err := json.Unmarshal(wantRaw, &wantList); err == nil {
					if !got.IsList || !reflect.DeepEqual(got.List, wantList) {
						t.Errorf("%q: got %#v, want list %v", key, got, wantList)
					}
					continue
				}
				var wantStr string
				if err := json.Unmarshal(wantRaw, &wantStr); err != nil {
					t.Fatalf("golden value for %q is neither string nor list: %s", key, wantRaw)
				}
				if got.IsList || got.Str != wantStr {
					t.Errorf("%q: got %#v, want scalar %q", key, got, wantStr)
				}
			}
			for key := range fm.Data {
				if _, ok := c.Data[key]; !ok {
					t.Errorf("key %q should have been dropped, but Go kept it", key)
				}
			}
		})
	}

	for _, c := range golden.FirstHeading {
		t.Run("firstHeading/"+c.Name, func(t *testing.T) {
			want := ""
			if c.Out != nil {
				want = *c.Out
			}
			if got := FirstHeading(c.Body); got != want {
				t.Errorf("got %q, want %q", got, want)
			}
		})
	}

	for _, c := range golden.StripLeadingHeading {
		t.Run("strip/"+c.Name, func(t *testing.T) {
			if got := StripLeadingHeading(c.Body, c.Title); got != c.Out {
				t.Errorf("got %q, want %q", got, c.Out)
			}
		})
	}
}

func keysOf(m map[string]Value) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	return out
}

func rawKeysOf(m map[string]json.RawMessage) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestNestedMaps covers Maps, which has no TypeScript counterpart to be golden-tested against:
// the notes parser it was ported from has no nested-map notion, and Data still drops these keys
// so that golden keeps passing. What Maps carries is the site build's two content files —
// profile.md's `forges:` and content/orgs/<slug>.md's `links:` — and their pills.
func TestNestedMaps(t *testing.T) {
	src := "---\n" +
		"description: hello\n" +
		"sites:\n" +
		"  - https://example.com\n" +
		"links:\n" +
		"  GitHub: https://github.com/x\n" +
		"  Email: mailto:a@b.c   # say hi\n" +
		"forges:\n" +
		"  github: https://github.com/x\n" +
		"deep:\n" +
		"  a:\n" +
		"    b: c\n" +
		"seqofmaps:\n" +
		"  - name: a\n" +
		"    url: b\n" +
		"---\nbody\n"
	fm := Parse(src)

	// Order is the file's order: `links:` is a row of pills and GitHub was written first.
	want := []MapEntry{{Key: "GitHub", Value: "https://github.com/x"}, {Key: "Email", Value: "mailto:a@b.c"}}
	if !reflect.DeepEqual(fm.Maps["links"], want) {
		t.Errorf("links = %#v, want %#v", fm.Maps["links"], want)
	}
	if got := fm.Maps["forges"]; len(got) != 1 || got[0] != (MapEntry{Key: "github", Value: "https://github.com/x"}) {
		t.Errorf("forges = %#v", got)
	}
	// Two levels deep, and a sequence of mappings, are both outside the subset: dropped whole
	// rather than half-read.
	if got, ok := fm.Maps["deep"]; ok {
		t.Errorf("deep should be dropped, got %#v", got)
	}
	if got, ok := fm.Maps["seqofmaps"]; ok {
		t.Errorf("seqofmaps should be dropped, got %#v", got)
	}
	// A nested map never leaks into Data — that is the surface the TypeScript golden pins.
	for _, key := range []string{"links", "forges", "deep", "seqofmaps"} {
		if _, ok := fm.Data[key]; ok {
			t.Errorf("Data must not carry the nested key %q", key)
		}
	}
	// The block is still consumed, so its indented lines never become keys of their own.
	if _, ok := fm.Data["GitHub"]; ok {
		t.Error("an indented mapping line became a top-level key")
	}
	if fm.Data["description"].Str != "hello" || len(fm.Data["sites"].List) != 1 {
		t.Errorf("scalars and sequences regressed: %#v", fm.Data)
	}
	if strings.TrimSpace(fm.Body) != "body" {
		t.Errorf("body = %q", fm.Body)
	}
}
