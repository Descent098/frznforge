package render

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The staleness guard on the two cross-language goldens.
//
// tests/fixtures/{format,listing}-cases.json are generated FROM web/js/{format,listing}.js, which
// makes the JavaScript the reference and TestFormatMatchesBrowser / TestListingMatchesBrowser the
// check. That arrangement has one hole, and it is the hole a snapshot always has: the golden is
// only as current as the last person who remembered to regenerate it. Change format.js, skip the
// regeneration, and Go keeps agreeing with the OLD JavaScript while the browser ships the new —
// the precise divergence the fixture exists to catch, hidden by the fixture itself.
//
// It matters more from 0.4.0 on, not less. web/js/*.js is THE browser implementation and it
// survives the Node deletion; what does not survive is tests/unit/format.test.ts, so after Phase 9
// these goldens are the only thing standing between format.js and an unnoticed change.
//
// The fix is to make the fixture say what it was generated from. Regenerating is one command and
// the failure message is the command.
func TestGoldensAreNotStale(t *testing.T) {
	for _, tc := range []struct {
		fixture   string
		generator string
	}{
		{"format-cases.json", "node tests/fixtures/gen-format-cases.mjs"},
		{"listing-cases.json", "node tests/fixtures/gen-listing-cases.mjs"},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			root := projectRoot(t)
			raw, err := os.ReadFile(filepath.Join(root, "tests", "fixtures", tc.fixture))
			if err != nil {
				t.Fatalf("read the golden: %v", err)
			}
			var golden struct {
				Source struct {
					File   string `json:"file"`
					Sha256 string `json:"sha256"`
				} `json:"source"`
			}
			if err := json.Unmarshal(raw, &golden); err != nil {
				t.Fatal(err)
			}
			if golden.Source.File == "" || golden.Source.Sha256 == "" {
				t.Fatalf("%s carries no source stamp — regenerate it with `%s`", tc.fixture, tc.generator)
			}

			src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(golden.Source.File)))
			if err != nil {
				t.Fatalf("the golden names %s, which cannot be read: %v", golden.Source.File, err)
			}
			sum := sha256.Sum256(src)
			if got := hex.EncodeToString(sum[:]); got != golden.Source.Sha256 {
				t.Errorf("%s has changed since %s was generated.\n"+
					"  recorded: %s\n  actual:   %s\n"+
					"The Go tests are still checking themselves against the old behaviour. Run:\n  %s\n"+
					"and re-run the tests; a real divergence will then show up as a value mismatch rather than as this.",
					golden.Source.File, tc.fixture, golden.Source.Sha256, got, tc.generator)
			}
		})
	}
}

// projectRoot walks up to the directory holding go.mod.
func projectRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("could not find the module root")
	return ""
}
