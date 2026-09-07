package routes

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"frznforge/internal/model"
)

// TestAllRoutesMatchesTypeScript is the cross-language sync test for the URL layer.
//
// testdata/expected-routes.json was produced by the TypeScript implementation itself
// (`npx tsx scripts/dump-routes.ts internal/model/testdata/fixture-forge.json …`), at both a
// root deploy and a /mysite sub-path deploy. If the Go port emits a different set — or the
// same set in a different order — the two renderers would build different sites from the same
// artifact, and this is the cheapest possible place to find that out.
//
// FROZEN as of 0.4.0. The generator and the TypeScript it read were deleted in Phase 9, so this
// golden can no longer be regenerated — it is now a record of what the previous engine emitted
// rather than a live comparison. That is the correct status for it: its job was to hold the Go
// port to the behaviour it replaced, and that job is finished. A deliberate change to this
// output means editing the golden by hand and saying why in the commit; an accidental one still
// fails here, which is the whole point of keeping it.
func TestAllRoutesMatchesTypeScript(t *testing.T) {
	data := loadFixture(t)

	raw, err := os.ReadFile(filepath.Join("testdata", "expected-routes.json"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var expected map[string][]string
	if err := json.Unmarshal(raw, &expected); err != nil {
		t.Fatalf("parse golden: %v", err)
	}

	for name, base := range map[string]string{"root": "", "mysite": "/mysite"} {
		t.Run(name, func(t *testing.T) {
			got := Router{Base: base}.AllRoutes(&data)
			want := expected[name]
			if len(got) != len(want) {
				t.Errorf("route count: got %d, want %d", len(got), len(want))
			}
			// Compare as ordered lists: page emission order is not itself a contract, but a
			// difference in order almost always means a difference in the traversal, and the
			// TypeScript order is the one the sync tests already encode.
			n := min(len(got), len(want))
			shown := 0
			for i := 0; i < n; i++ {
				if got[i] != want[i] {
					if shown < 10 {
						t.Errorf("route %d:\n got: %s\nwant: %s", i, got[i], want[i])
						shown++
					}
				}
			}
			if len(got) > len(want) {
				t.Errorf("extra routes, first: %s", got[len(want)])
			} else if len(want) > len(got) {
				t.Errorf("missing routes, first: %s", want[len(got)])
			}
		})
	}
}

// TestEncodeURIComponentMatchesJavaScript pins the one encoding function whose Go standard
// library equivalent is subtly different.
//
// url.PathEscape leaves $ & + , : ; = @ alone; encodeURIComponent does not. Every one of those
// is legal in a git path, so using the stdlib here would silently emit different URLs than the
// TypeScript build for real repositories. Expected values are what Node's encodeURIComponent
// actually returns.
func TestEncodeURIComponentMatchesJavaScript(t *testing.T) {
	cases := map[string]string{
		"plain.txt":           "plain.txt",
		"read me.md":          "read%20me.md",
		"a&b":                 "a%26b",
		"a+b":                 "a%2Bb",
		"a,b":                 "a%2Cb",
		"a;b":                 "a%3Bb",
		"a=b":                 "a%3Db",
		"a@b":                 "a%40b",
		"a$b":                 "a%24b",
		"a:b":                 "a%3Ab",
		"a/b":                 "a%2Fb",
		"a?b":                 "a%3Fb",
		"café.md":             "caf%C3%A9.md",
		"日本語.txt":             "%E6%97%A5%E6%9C%AC%E8%AA%9E.txt",
		"~unreserved-_.!*'()": "~unreserved-_.!*'()",
	}
	for in, want := range cases {
		if got := EncodeURIComponent(in); got != want {
			t.Errorf("EncodeURIComponent(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestEncodePathSegmentsKeepsSlashes — the directory structure has to survive encoding, or
// every nested file lands at the wrong URL.
func TestEncodePathSegmentsKeepsSlashes(t *testing.T) {
	got := EncodePathSegments("src/read me/a&b.ts")
	want := "src/read%20me/a%26b.ts"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRefSlugRoundTrips(t *testing.T) {
	for _, name := range []string{"main", "feat/zip", "release/v0.7", "a/b/c"} {
		if got := RefFromSlug(RefSlug(name)); got != name {
			t.Errorf("round trip of %q gave %q", name, got)
		}
	}
	if RefSlug("feat/zip") != "feat~zip" {
		t.Errorf("RefSlug should replace / with ~")
	}
}

// TestIsRawServable — the two characters no static URL can round-trip. A path this rejects
// gets no route at all, which is why it is a shared helper rather than an inline check.
func TestIsRawServable(t *testing.T) {
	servable := []string{"a.txt", "read me.md", "a&b+c;d.txt", "日本語.txt", "deep/nested/file.go"}
	unservable := []string{"c#-tips.md", "50% off.txt", "a/b#c", "%"}
	for _, p := range servable {
		if !IsRawServable(p) {
			t.Errorf("%q should be servable", p)
		}
	}
	for _, p := range unservable {
		if IsRawServable(p) {
			t.Errorf("%q should NOT be servable", p)
		}
	}
}

// TestBrowsableRefsOrder — the default branch first, then branches, then tags, each group by
// name. The renderer's ref switcher and the route list both depend on this.
func TestBrowsableRefsOrder(t *testing.T) {
	data := loadFixture(t)
	for i := range data.Repos {
		repo := &data.Repos[i]
		refs := BrowsableRefs(repo)
		if repo.DefaultBranch != nil && *repo.DefaultBranch != "" && len(refs) > 0 {
			if !refs[0].IsDefault || refs[0].Name != *repo.DefaultBranch {
				t.Errorf("%s: first browsable ref should be the default branch, got %q", repo.Slug, refs[0].Name)
			}
		}
		seenTag := false
		for _, ref := range refs[min(1, len(refs)):] {
			if ref.Kind == "tag" {
				seenTag = true
			} else if seenTag {
				t.Errorf("%s: branch %q appears after a tag", repo.Slug, ref.Name)
			}
		}
	}
}

// TestResolveReleasesPrefersProvider — a repo with imported releases must not fall back to its
// tags, and the fallback must still work for tag-mode repos.
func TestResolveReleasesPrefersProvider(t *testing.T) {
	data := loadFixture(t)
	found := 0
	for i := range data.Repos {
		repo := &data.Repos[i]
		rels := ResolveReleases(repo)
		if len(repo.Releases) > 0 {
			found++
			for _, r := range rels {
				if r.Source != "provider" {
					t.Errorf("%s: expected provider releases, got source %q", repo.Slug, r.Source)
				}
			}
		}
		// Newest first, ties by tag ascending.
		for j := 1; j < len(rels); j++ {
			if rels[j-1].Date < rels[j].Date {
				t.Errorf("%s: releases not sorted newest first (%s before %s)", repo.Slug, rels[j-1].Date, rels[j].Date)
			}
		}
	}
	if found == 0 {
		t.Skip("fixture has no provider-imported releases")
	}
}

func loadFixture(t *testing.T) model.ForgeData {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "model", "testdata", "fixture-forge.json"))
	if err != nil {
		t.Fatalf("read fixture artifact: %v", err)
	}
	data, err := model.Parse(raw)
	if err != nil {
		t.Fatalf("parse fixture artifact: %v", err)
	}
	return data
}
