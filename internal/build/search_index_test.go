package build

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"frznforge/internal/config"
	"frznforge/internal/model"
	"frznforge/internal/render"
	"frznforge/internal/routes"
)

// TestSearchIndexMatchesTypeScript compares the emitted /search-index.json against the file the
// TypeScript build produces for the same artifact — byte for byte.
//
// Byte-for-byte is achievable here (unlike the HTML, which is compared structurally) because
// this file has no framework in it: it is `JSON.stringify` of a value derived purely from the
// artifact. So it is checked the strict way. The golden is generated FROM the TypeScript:
//
//	npx tsx scripts/dump-search-index.ts internal/model/testdata/fixture-forge.json \
//	    internal/build/testdata/expected-search-index.json
//
// The things this actually catches are the ones that are invisible by eye: a `date` key present
// on the wrong kinds, `keywords` missing the repo slug, the doc ORDER, `<` escaped when it
// should not be, and the fallback details for a note or an org with no description.
func TestSearchIndexMatchesTypeScript(t *testing.T) {
	wantRaw, err := os.ReadFile(filepath.Join("testdata", "expected-search-index.json"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	gotRaw := buildIndexFor(t, "")

	// ONE deliberate divergence, asserted rather than tolerated — see
	// TestSearchIndexDropsFilesWithNoPage for the whole argument.
	want := dropUnservableFileDocs(t, wantRaw)
	if !bytes.Equal(gotRaw, want) {
		t.Fatalf("search index differs from the TypeScript build\n%s", firstDiff(want, gotRaw))
	}
}

// TestSearchIndexDropsFilesWithNoPage pins a bug fix, and proves the fixture still exercises it.
//
// The TypeScript index lists every blob in the default-branch tree, including paths holding `#`
// or `%`. Those paths get no blob page — `routes.IsRawServable` excludes them, and ingest raises
// `repo-path-unservable` saying in so many words that the file "is listed in the file table but
// has no page". So the command palette offered results that 404. The Go emitter applies the same
// exclusion the route builder does, which is the only way the two can agree.
//
// The second assertion matters as much as the first: if the fixture ever loses its `#`/`%` files
// this test would pass vacuously, and the divergence above would be silently unverified.
func TestSearchIndexDropsFilesWithNoPage(t *testing.T) {
	wantRaw, err := os.ReadFile(filepath.Join("testdata", "expected-search-index.json"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var ts struct {
		Docs []struct {
			Kind  string `json:"kind"`
			Title string `json:"title"`
			URL   string `json:"url"`
		} `json:"docs"`
	}
	if err := json.Unmarshal(wantRaw, &ts); err != nil {
		t.Fatal(err)
	}
	var unservable []string
	for _, d := range ts.Docs {
		if d.Kind == "file" && !routes.IsRawServable(d.Title) {
			unservable = append(unservable, d.Title)
		}
	}
	if len(unservable) == 0 {
		t.Fatal("the fixture no longer contains a file path with # or %, so this divergence is untested")
	}
	got := buildIndexFor(t, "")
	for _, path := range unservable {
		if bytes.Contains(got, []byte(`"title":"`+path+`"`)) {
			t.Errorf("index still lists %q, which has no blob page", path)
		}
	}
	t.Logf("dropped %d file docs that had no page: %v", len(unservable), unservable)
}

// dropUnservableFileDocs removes the file docs the Go emitter deliberately omits, so the rest of
// the index can be compared byte for byte.
func dropUnservableFileDocs(t *testing.T, raw []byte) []byte {
	t.Helper()
	var idx struct {
		Version int               `json:"version"`
		Docs    []json.RawMessage `json:"docs"`
	}
	if err := json.Unmarshal(raw, &idx); err != nil {
		t.Fatal(err)
	}
	kept := idx.Docs[:0]
	for _, d := range idx.Docs {
		var probe struct {
			Kind  string `json:"kind"`
			Title string `json:"title"`
		}
		if err := json.Unmarshal(d, &probe); err != nil {
			t.Fatal(err)
		}
		if probe.Kind == "file" && !routes.IsRawServable(probe.Title) {
			continue
		}
		kept = append(kept, d)
	}
	idx.Docs = kept
	out, err := marshalCompact(idx)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestSearchIndexUnderSubPath — every URL in the index must carry the deploy base, or the
// palette navigates out of a sub-path deploy on the first Enter.
func TestSearchIndexUnderSubPath(t *testing.T) {
	got := buildIndexFor(t, "/mysite")
	if bytes.Contains(got, []byte(`"url":"/repos/`)) {
		t.Error("index contains a root-absolute /repos/ URL under a /mysite deploy")
	}
	if !bytes.Contains(got, []byte(`"url":"/mysite/repos/`)) {
		t.Error("index has no /mysite-prefixed repo URL")
	}
}

// buildIndexFor renders just the search index for the fixture artifact at a given deploy base.
func buildIndexFor(t *testing.T, base string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "model", "testdata", "fixture-forge.json"))
	if err != nil {
		t.Fatalf("read fixture artifact: %v", err)
	}
	data, err := model.Parse(raw)
	if err != nil {
		t.Fatalf("parse fixture artifact: %v", err)
	}

	cfg := &config.Resolved{}
	cfg.Site.Title = "frznforge"
	cfg.Site.Base = base
	cfg.Owner.Name = "Kieran Wood"
	cfg.Owner.Handle = "kieran"
	cfg.Theme.Palette = "hearth"

	site := &render.Site{Cfg: cfg, Data: &data, Router: routes.Router{Base: base}, Now: time.Unix(0, 0).UTC()}
	r, err := render.New(site)
	if err != nil {
		t.Fatalf("templates: %v", err)
	}
	out := t.TempDir()
	b := &Builder{Cfg: cfg, Data: &data, Site: site, Renderer: r, Router: site.Router, OutDir: out}
	if err := emitSearchIndex(b); err != nil {
		t.Fatalf("emit: %v", err)
	}
	rel := FilePath(base, b.Router.SearchIndexURL())
	got, err := os.ReadFile(filepath.Join(out, rel))
	if err != nil {
		t.Fatalf("read emitted index: %v", err)
	}
	return got
}

func firstDiff(want, got []byte) string {
	i := 0
	for i < len(want) && i < len(got) && want[i] == got[i] {
		i++
	}
	lo := i - 90
	if lo < 0 {
		lo = 0
	}
	clip := func(b []byte) string {
		hi := i + 90
		if hi > len(b) {
			hi = len(b)
		}
		if lo > len(b) {
			return "(end)"
		}
		return string(b[lo:hi])
	}
	return "first difference at byte " + itoa(i) + "\n  want: …" + clip(want) + "\n  got : …" + clip(got)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}
