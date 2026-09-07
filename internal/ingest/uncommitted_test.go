package ingest

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"frznforge/internal/ingest/testsupport"
)

// The working-tree rule, ported from tests/unit/uncommitted.test.ts.
//
// "Ingest reads git through the CLI only, never the working tree" is the load-bearing invariant
// of this whole project: it is what makes an artifact a function of the commits rather than of
// whoever happened to have a file open. Every other guarantee — that two machines produce the
// same bytes, that a rebuild is reproducible, that a published page reflects what is committed —
// rests on it.
//
// It is also the invariant most easily lost by accident, because the natural way to read a file
// is to open it. Nothing else in the Go suite covers this; the port went unnoticed until the
// Phase 9 coverage audit went looking.
//
// The strongest assertion is the last one: dirty the work tree every way it can be dirtied, and
// the artifact must come out byte-identical.
func TestUncommittedChangesNeverLeak(t *testing.T) {
	repo := testsupport.Create(t, "dirty", "main")
	repo.WriteAndCommit(map[string]string{
		"README.md":  "# clean\n",
		"src/app.ts": "export const v = 1;\n",
		"LICENSE":    "MIT License\n",
	}, "init", testsupport.CommitOptions{})

	clean, err := ScanRepo(context.Background(), ScanSource{AbsPath: repo.Dir}, testScanOptions())
	if err != nil {
		t.Fatalf("clean scan: %v", err)
	}
	if clean.Skipped {
		t.Fatalf("clean scan skipped the fixture: %+v", clean.Warning)
	}

	// Every way a work tree can differ from HEAD.
	repo.Write(map[string]string{"src/untracked.py": "print(1)\n"})                                   // untracked
	repo.Write(map[string]string{"src/app.ts": strings.Repeat("export const v = 2; // dirty\n", 50)}) // modified, unstaged
	repo.Write(map[string]string{"src/staged.go": "package main\n"})
	repo.Add("src/staged.go") // staged, uncommitted
	repo.Write(map[string]string{
		"README.md":       "# DIRTY\n",
		"LICENSE":         "Apache License Version 2.0\n",
		".frznforge.json": `{"name":"DIRTY"}`, // even the metadata file must come from the commit
	})

	dirty, err := ScanRepo(context.Background(), ScanSource{AbsPath: repo.Dir}, testScanOptions())
	if err != nil {
		t.Fatalf("dirty scan: %v", err)
	}
	if dirty.Skipped {
		t.Fatalf("dirty scan skipped the fixture: %+v", dirty.Warning)
	}

	t.Run("untracked and staged paths are absent", func(t *testing.T) {
		for _, e := range dirty.Repo.Tree {
			switch e.Path {
			case "src/untracked.py", "src/staged.go", ".frznforge.json":
				t.Errorf("%q reached the tree from the work tree", e.Path)
			}
		}
		got := make([]string, 0, len(dirty.Repo.Files))
		for p := range dirty.Repo.Files {
			got = append(got, p)
		}
		want := map[string]bool{"LICENSE": true, "README.md": true, "src/app.ts": true}
		if len(got) != len(want) {
			t.Errorf("files = %v, want exactly the three committed paths", got)
		}
		for _, p := range got {
			if !want[p] {
				t.Errorf("files contains %q, which was never committed", p)
			}
		}
	})

	t.Run("a modified file keeps its committed content, size and sha", func(t *testing.T) {
		f, ok := dirty.Repo.Files["src/app.ts"]
		if !ok {
			t.Fatal("src/app.ts is missing")
		}
		if f.Sha != clean.Repo.Files["src/app.ts"].Sha {
			t.Errorf("sha changed when only the work tree did")
		}
		if want := int64(len("export const v = 1;\n")); f.Size != want {
			t.Errorf("size = %d, want the committed %d", f.Size, want)
		}
		if content, ok := dirty.Blobs[f.Sha]; !ok {
			t.Error("the committed blob was not stored")
		} else if string(content) != "export const v = 1;\n" {
			t.Errorf("stored blob = %q, want the committed content", content)
		}
	})

	t.Run("readme, license, metadata and languages reflect the commit only", func(t *testing.T) {
		if dirty.Repo.Readme == nil || dirty.Repo.Readme.Content != "# clean\n" {
			t.Errorf("readme came from the work tree: %+v", dirty.Repo.Readme)
		}
		if dirty.Repo.License == nil || dirty.Repo.License.Spdx == nil || *dirty.Repo.License.Spdx != "MIT" {
			t.Errorf("license came from the work tree: %+v", dirty.Repo.License)
		}
		if dirty.Repo.Name != "dirty" {
			t.Errorf("name = %q — the uncommitted .frznforge.json was read", dirty.Repo.Name)
		}
		if len(dirty.Repo.Languages) != 1 || dirty.Repo.Languages[0].Name != "TypeScript" {
			t.Errorf("languages = %+v, want only the committed TypeScript", dirty.Repo.Languages)
		}
	})

	// The one that subsumes the rest: same commits in, same bytes out, whatever is on disk.
	t.Run("the whole repo record is identical", func(t *testing.T) {
		before, err := json.Marshal(clean.Repo)
		if err != nil {
			t.Fatal(err)
		}
		after, err := json.Marshal(dirty.Repo)
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			cleanWindow, dirtyWindow := firstDifferingRun(string(before), string(after))
			t.Errorf("dirtying the work tree changed the artifact\n clean: %s\n dirty: %s", cleanWindow, dirtyWindow)
		}
	})
}

// firstDifferingRun returns a short window around the first difference of two JSON strings.
func firstDifferingRun(a, b string) (string, string) {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	lo := i - 60
	if lo < 0 {
		lo = 0
	}
	clip := func(s string) string {
		hi := i + 60
		if hi > len(s) {
			hi = len(s)
		}
		if lo > len(s) {
			return "(end)"
		}
		return s[lo:hi]
	}
	return clip(a), clip(b)
}
