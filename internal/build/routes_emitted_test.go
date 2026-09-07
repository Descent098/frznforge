package build_test

import (
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"frznforge/internal/build"
	"frznforge/internal/config"
	"frznforge/internal/model"
	"frznforge/internal/routes"
)

// TestBuildEmitsEveryRoute pins the build against routes.AllRoutes, in both directions.
//
// This is the failure mode a page-family port is most prone to, and the only one that leaves no
// trace: an emitter whose loop never runs writes ZERO files and returns nil, and the build
// prints "built N files" and exits 0. Nothing else in the suite notices — the determinism test
// compares two runs of the same code, so a family that emits nothing emits nothing twice and
// passes.
//
// The reverse direction matters as much. A page the router does not list is a page nothing
// links to and the search index does not know about, and it is how a stale route survives a
// rename: the emitter still writes /repos/x/releases/ after the router stopped predicting it,
// and the only symptom is a file nobody can reach.
func TestBuildEmitsEveryRoute(t *testing.T) {
	// Same rule as buildRoots in sync_test.go, and for the same reason: this renders the
	// developer's whole corpus, which went from one repository to 73 and took this single test
	// from seconds to 490 of them — most of a default `go test` budget, for a claim
	// TestSyncOnASelfBuiltProject already makes on a fixture it builds itself and cannot skip.
	//
	// So the fixture is the gate and this is the deeper run: FRZNFORGE_FULL_CORPUS=1.
	if os.Getenv("FRZNFORGE_FULL_CORPUS") == "" {
		t.Skip("set FRZNFORGE_FULL_CORPUS=1 to render the local corpus (slow); " +
			"TestSyncOnASelfBuiltProject covers this claim on a fixture that never skips")
	}
	root := repoRoot(t)
	cfg, err := config.Load(root)
	if err != nil {
		t.Skipf("no loadable config on this machine: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(cfg.OutDir, "forge.json"))
	if err != nil {
		t.Skip("no artifact on this machine; run `frznforge ingest` first")
	}
	data, err := model.Parse(raw)
	if err != nil {
		t.Fatalf("parse artifact: %v", err)
	}

	out := filepath.Join(t.TempDir(), "site")
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	if _, err := build.Run(build.Options{Root: root, OutDir: out, Now: now}); err != nil {
		t.Fatalf("build: %v", err)
	}

	router := routes.Router{Base: cfg.Site.Base}
	want := map[string]string{} // file path → the route that asked for it
	for _, route := range router.AllRoutes(&data) {
		want[decoded(t, filePathFor(router.Base, route))] = route
	}
	// The search index is a route the router does not list: it is a data file, not a page.
	want[decoded(t, build.RawPath(router.Base, router.SearchIndexURL()))] = router.SearchIndexURL()
	// public/ and web/ are copied verbatim; they are not routes either.
	for _, dir := range []string{"public", "web"} {
		for _, rel := range treeFiles(t, filepath.Join(root, dir)) {
			want[rel] = dir + "/ (copied verbatim)"
		}
	}

	got := map[string]bool{}
	for _, rel := range treeFiles(t, out) {
		got[rel] = true
	}

	var missing, extra []string
	byFamily := map[string]int{}
	for rel, route := range want {
		if !got[rel] {
			missing = append(missing, rel+"  (route "+route+")")
			continue
		}
		byFamily[familyOf(router.Base, route)]++
	}
	for rel := range got {
		if _, ok := want[rel]; !ok {
			extra = append(extra, rel)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)

	if len(missing) > 0 {
		t.Errorf("%d predicted routes were never written — a family's loop did not run:\n  %s",
			len(missing), strings.Join(head(missing, 20), "\n  "))
	}
	if len(extra) > 0 {
		t.Errorf("%d files were written that no route predicts — nothing links to them:\n  %s",
			len(extra), strings.Join(head(extra, 20), "\n  "))
	}

	// Per-family counts in the log, so a run that passes still shows what each family produced.
	// A family that drops from hundreds of pages to one is a passing build and a broken site.
	names := make([]string, 0, len(byFamily))
	for name := range byFamily {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t.Logf("%-22s %5d files", name, byFamily[name])
	}
}

// filePathFor applies the rule the route's KIND implies: a raw, archive or hosted-site route
// carries bytes and is never renamed; everything else is a page.
func filePathFor(base, route string) string {
	if isBytesRoute(base, route) {
		return build.RawPath(base, route)
	}
	return build.FilePath(base, route)
}

// isBytesRoute mirrors which emitters call WriteFile rather than WritePage. Written from the
// URL shape because that is all a route is here; the emitters themselves know it structurally.
func isBytesRoute(base, route string) bool {
	rel := build.RawPath(base, route)
	if strings.HasSuffix(rel, "/") {
		return false
	}
	seg := strings.Split(rel, "/")
	switch {
	case strings.HasPrefix(rel, "repos/") && len(seg) > 2 && (seg[2] == "raw" || seg[2] == "archive"):
		return true
	case strings.HasPrefix(rel, "notes/") && len(seg) > 2 && seg[2] == "raw":
		return true
	case rel == "404":
		return false
	default:
		// What is left with no trailing slash is a hosted site's file, served at its literal
		// path under /<slug>/.
		return path.Ext(rel) != "" || len(seg) > 1
	}
}

func familyOf(base, route string) string {
	rel := build.RawPath(base, route)
	switch {
	case rel == "":
		return "profile"
	case rel == "repos/":
		return "repos listing"
	case rel == "404":
		return "404"
	case rel == "search-index.json":
		return "search index"
	case strings.HasPrefix(rel, "notes/"):
		return "notes"
	case strings.HasPrefix(rel, "orgs/"):
		return "orgs"
	case rel == "public/ (copied verbatim)" || rel == "web/ (copied verbatim)":
		return "assets"
	}
	if !strings.HasPrefix(rel, "repos/") {
		return "hosted sites"
	}
	seg := strings.Split(rel, "/")
	if len(seg) < 3 || seg[2] == "" {
		return "repo overview"
	}
	switch seg[2] {
	case "tree", "blob", "raw", "commits", "commit", "branches", "tags", "releases", "release",
		"insights", "archive":
		return "repo " + seg[2]
	}
	return "repo " + seg[2]
}

func treeFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	for rel := range hashTree(t, root) {
		out = append(out, rel)
	}
	sort.Strings(out)
	return out
}

// decoded undoes the percent-encoding the router applies to path segments: the URL says
// `read%20me.md` and the file on disk is `read me.md`.
func decoded(t *testing.T, p string) string {
	t.Helper()
	out, err := url.PathUnescape(p)
	if err != nil {
		t.Fatalf("undecodable route path %q: %v", p, err)
	}
	return out
}

func head(list []string, n int) []string {
	if len(list) <= n {
		return list
	}
	return append(list[:n:n], "…")
}
