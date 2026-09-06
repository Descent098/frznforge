package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"frznforge/internal/config"
	"frznforge/internal/ingest/testsupport"
	"frznforge/internal/model"
)

// The acceptance bar for the port is byte identity with the TypeScript ingest, so the test
// that matters most is not a Go assertion at all: it builds one deliberately awkward fixture
// repository, scans it with BOTH implementations, and compares the two Repo records.
//
// The comparison runs both sides through the same Go encoder rather than diffing raw JSON.
// That is exact on values and on array order — which is what this phase owns — while staying
// silent about map KEY order, which belongs to the model layer: encoding/json sorts map keys,
// JSON.stringify emits them in insertion order, and where the two disagree (refTrees holding a
// branch that sorts after a tag) the fix is in model.go, not here.

// parityFixture builds a repository that exercises every parsing trap at once: nested
// directories, a non-ASCII path (core.quotepath), a path with '#' (unservable), a rename
// (three NUL fields where a change has one), a merge (no numstat at all), CRLF content, a
// binary blob, an oversized blob, a second author, an orphaned tag, and a branch name with a
// slash in it.
func parityFixture(t *testing.T) *testsupport.Repo {
	t.Helper()
	repo := testsupport.Create(t, "parity-repo", "main")

	repo.WriteBytes(map[string][]byte{
		"README.md":   []byte("# Parity\n\nA fixture with <html> & \"quotes\".\n"),
		"LICENSE":     []byte(mitLicense),
		"src/main.go": []byte("package main\n\nfunc main() {}\n"),
		"src/util.ts": []byte("export const util = 1;\n"),
		// Non-ASCII and space-bearing names: git quotes both in any output that is not -z, so
		// these are what prove every listing this package reads is NUL-delimited. Built from
		// runes so this source file stays ASCII.
		"docs/caf" + string(rune(0xe9)) + ".md": []byte("cafe notes\n"),
		"docs/two words.md":                     []byte("spaces\n"),
		string(rune(0x65e5)) + "-log.txt":       []byte("nichi\n"),
		"crlf.txt":                              []byte("one\r\ntwo\r\n"),
		"assets/x.bin":                          {0x00, 0x01, 0x02, 0x00, 0xff},
	})
	repo.Add()
	repo.Commit("first commit", testsupport.CommitOptions{})

	repo.WriteBytes(map[string][]byte{
		".frznforge.json": []byte(`{
  "name": "Parity Fixture",
  "description": "A repo used to prove the Go and TypeScript scanners agree.",
  "links": { "homepage": "https://example.com/", "issues": "https://example.com/issues" },
  "tags": ["fixture", "parity"],
  "template": false
}`),
		"big.txt": bytesRepeat('x', 400),
	})
	repo.Add()
	repo.Commit("add metadata and a big file\n\nWith a body that has trailing space.   \n\n",
		testsupport.CommitOptions{AuthorName: "Other Person", AuthorEmail: "Other@Example.com"})

	// A rename, so the numstat parser has to walk past two extra NUL fields.
	repo.Git("mv", "src/util.ts", "src/renamed.ts")
	repo.Commit("rename util", testsupport.CommitOptions{})

	// A path a static URL cannot round-trip.
	repo.WriteAndCommit(map[string]string{"notes/c#-tips.md": "sharp\n"}, "add an unservable path",
		testsupport.CommitOptions{})

	repo.Tag("v1.0.0", testsupport.TagOptions{Annotated: true, Message: "release one\n\nnotes\n"})

	// A side branch, merged back: the merge commit gets no numstat records at all.
	repo.Checkout("feature/side", true)
	repo.WriteAndCommit(map[string]string{"src/side.go": "package main\n"}, "side work", testsupport.CommitOptions{})
	repo.Checkout("main", false)
	repo.GitWith(map[string]string{
		"GIT_AUTHOR_DATE":    testsupport.At(3600),
		"GIT_COMMITTER_DATE": testsupport.At(3600),
	}, "merge", "--no-ff", "--no-verify", "-q", "-m", "merge feature/side", "feature/side")

	repo.Tag("light", testsupport.TagOptions{})

	// A tag whose name sorts BEFORE every branch name in this fixture.
	//
	// refTrees key order is branches-then-tags, each group by name — not overall alphabetical.
	// Without a tag like this the two orders coincide (every branch here already sorts before
	// every tag), a Go map's sorted output looks correct, and the divergence stays invisible.
	// It is invisible in this project's own artifact and in the e2e fixture for exactly that
	// reason. See model.RefTreeMap.
	repo.Tag("aaa-first", testsupport.TagOptions{Annotated: true, Message: "sorts before every branch\n"})

	// A dormant branch: with branchTrees capped it is the one that loses its tree.
	repo.Checkout("dormant", true)
	repo.WriteAndCommit(map[string]string{"dormant.txt": "old\n"}, "dormant work",
		testsupport.CommitOptions{Date: "2023-05-05T05:05:05Z"})

	// A hosted branch — cap-exempt, and scanned with the bigger hosted cap.
	repo.Checkout("main", false)
	repo.Checkout("gh-pages", true)
	repo.WriteAndCommit(map[string]string{"index.html": "<!doctype html>\n<p>hi</p>\n", "bundle.js": strings.Repeat("z", 300)},
		"publish site", testsupport.CommitOptions{})

	// A tag whose commit no branch reaches: it has to land in extraCommits.
	repo.Checkout("main", false)
	repo.Checkout("orphan", true)
	repo.WriteAndCommit(map[string]string{"orphan.txt": "orphan\n"}, "orphan work", testsupport.CommitOptions{})
	repo.Tag("v0.5.0", testsupport.TagOptions{Annotated: true, Message: "orphaned release"})
	repo.Checkout("main", false)
	repo.Git("branch", "-D", "orphan")

	// Guard: a filesystem that mangled the non-ASCII name would quietly turn the sharpest part
	// of this fixture into a no-op. The listing is asked for with -z on purpose — plain
	// `ls-files` applies core.quotepath and answers `"docs/caf\303\251.md"`, which is exactly
	// the escaping every reader in this package avoids by staying NUL-delimited.
	if tracked := repo.Git("ls-files", "-z"); !strings.Contains(tracked, "caf"+string(rune(0xe9))) {
		t.Fatalf("the non-ASCII fixture path did not survive to the index:\n%s", tracked)
	}
	return repo
}

// TestScanRepoEdgeCasesMatchTypeScript covers the two shapes the main fixture cannot: a
// repository with no commits at all, and metadata that has to be truncated mid-surrogate.
func TestScanRepoEdgeCasesMatchTypeScript(t *testing.T) {
	if testing.Short() {
		t.Skip("parity test shells out to tsx")
	}
	root := projectRoot(t)
	if _, err := os.Stat(tsxBinary(root)); err != nil {
		t.Skipf("tsx is not installed (%s); run npm install to enable the parity test", tsxBinary(root))
	}

	options := map[string]any{
		"maxBlobBytes":       512 * 1024,
		"maxCommits":         nil,
		"maxCommitAgeDays":   nil,
		"tagTrees":           25,
		"branchTrees":        10,
		"archives":           true,
		"hostedMaxFileBytes": 20 * 1024 * 1024,
		"insights":           map[string]any{"enabled": true, "samples": 24, "maxBytesPerSample": 20 * 1024 * 1024},
	}

	cases := []struct {
		name  string
		build func(t *testing.T) *testsupport.Repo
	}{
		{"empty", func(t *testing.T) *testsupport.Repo { return testsupport.Create(t, "empty-repo", "main") }},
		{"long-description", func(t *testing.T) *testsupport.Repo {
			repo := testsupport.Create(t, "meta-repo", "main")
			// The 299th UTF-16 unit — the last one a truncation would keep — is the HIGH half
			// of a surrogate pair, so the cut has to step back one unit rather than emit a lone
			// surrogate.
			desc := strings.Repeat("a", 298) + string(rune(0x1f600)) + strings.Repeat("b", 20)
			meta, err := json.Marshal(map[string]any{"description": desc})
			if err != nil {
				t.Fatal(err)
			}
			repo.WriteAndCommit(map[string]string{".frznforge.json": string(meta)}, "meta", testsupport.CommitOptions{})
			return repo
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := tc.build(t)
			goResult, err := ScanRepo(context.Background(), ScanSource{AbsPath: repo.Dir}, scanOptionsFromMap(t, options, nil))
			if err != nil {
				t.Fatalf("Go scan: %v", err)
			}
			if goResult.Skipped {
				t.Fatalf("Go scan skipped the fixture: %+v", goResult.Warning)
			}
			ts := runTypeScriptScan(t, root, repo.Dir, options, nil)
			if got, want := encodeLikeArtifact(t, goResult.Repo), encodeLikeArtifact(t, &ts.Repo); got != want {
				t.Fatalf("Go and TypeScript scans differ\n%s", firstDifference(want, got))
			}
		})
	}
}

const mitLicense = `MIT License

Copyright (c) 2024 Test User

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction.
`

func TestScanRepoMatchesTypeScript(t *testing.T) {
	if testing.Short() {
		t.Skip("parity test shells out to tsx")
	}
	root := projectRoot(t)
	tsx := tsxBinary(root)
	if _, err := os.Stat(tsx); err != nil {
		t.Skipf("tsx is not installed (%s); run npm install to enable the parity test", tsx)
	}

	repo := parityFixture(t)
	contributors := []config.ContributorConfig{{
		Name:        "The Other Person",
		Emails:      []string{"other@example.com", "other@old.example.com"},
		Avatar:      "avatars/other.png",
		Description: "wrote the second commit",
		URL:         "https://example.com/other",
	}}

	cases := []struct {
		name    string
		options map[string]any
	}{
		{
			// Caps that bite: one branch tree and one tag tree, so both capped warnings fire
			// and the hosted branch has to survive the branch cap anyway.
			name: "capped",
			options: map[string]any{
				"maxBlobBytes":       128,
				"maxCommits":         nil,
				"maxCommitAgeDays":   nil,
				"tagTrees":           1,
				"branchTrees":        1,
				"archives":           true,
				"hostedMaxFileBytes": 4096,
				"insights":           map[string]any{"enabled": true, "samples": 24, "maxBytesPerSample": 20 * 1024 * 1024},
			},
		},
		{
			// Everything browsable, and a tiny insights budget so a checkpoint goes
			// approximate — the one place a null reaches codeSize.
			name: "uncapped",
			options: map[string]any{
				"maxBlobBytes":       512 * 1024,
				"maxCommits":         nil,
				"maxCommitAgeDays":   nil,
				"tagTrees":           25,
				"branchTrees":        "all",
				"archives":           true,
				"hostedMaxFileBytes": 20 * 1024 * 1024,
				"insights":           map[string]any{"enabled": true, "samples": 3, "maxBytesPerSample": 64},
			},
		},
		{
			// History narrowed: extraCommits has to pick up the commits the cap dropped, and
			// nothing derived from `commits` may quietly include them.
			name: "narrowed",
			options: map[string]any{
				"maxBlobBytes":       512 * 1024,
				"maxCommits":         2,
				"maxCommitAgeDays":   nil,
				"tagTrees":           25,
				"branchTrees":        10,
				"archives":           false,
				"hostedMaxFileBytes": 20 * 1024 * 1024,
				"insights":           map[string]any{"enabled": true, "samples": 24, "maxBytesPerSample": 20 * 1024 * 1024},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			goResult, err := ScanRepo(context.Background(), ScanSource{
				AbsPath:        repo.Dir,
				HostedRequests: []*string{nil},
			}, scanOptionsFromMap(t, tc.options, contributors))
			if err != nil {
				t.Fatalf("Go scan: %v", err)
			}
			if goResult.Skipped {
				t.Fatalf("Go scan skipped the fixture: %+v", goResult.Warning)
			}

			ts := runTypeScriptScan(t, root, repo.Dir, tc.options, contributors)

			gotJSON := encodeLikeArtifact(t, goResult.Repo)
			wantJSON := encodeLikeArtifact(t, &ts.Repo)
			if gotJSON != wantJSON {
				t.Fatalf("Go and TypeScript scans differ\n%s", firstDifference(wantJSON, gotJSON))
			}

			// Blob bytes and archive bytes are compared separately: the TS side reports lengths
			// rather than content, and an archive is git's own zip on both sides.
			if len(goResult.Blobs) != len(ts.Blobs) {
				t.Fatalf("blob count: Go %d, TypeScript %d", len(goResult.Blobs), len(ts.Blobs))
			}
			for sha, buf := range goResult.Blobs {
				size, ok := ts.Blobs[sha]
				if !ok {
					t.Fatalf("blob %s is missing from the TypeScript result", sha)
				}
				if len(buf) != size {
					t.Fatalf("blob %s: Go %d bytes, TypeScript %d", sha, len(buf), size)
				}
			}
			if len(goResult.Archives) != len(ts.Archives) {
				t.Fatalf("archive count: Go %d, TypeScript %d", len(goResult.Archives), len(ts.Archives))
			}
			for i, a := range goResult.Archives {
				if a.File != ts.Archives[i].File || int64(len(a.Data)) != ts.Archives[i].Bytes {
					t.Fatalf("archive %d: Go %s/%d, TypeScript %s/%d",
						i, a.File, len(a.Data), ts.Archives[i].File, ts.Archives[i].Bytes)
				}
			}
		})
	}
}

// tsScanOutput mirrors internal/ingest/testdata/ts-scan.ts.
type tsScanOutput struct {
	Repo     model.Repo     `json:"repo"`
	Blobs    map[string]int `json:"blobs"`
	Archives []struct {
		File  string `json:"file"`
		Bytes int64  `json:"bytes"`
	} `json:"archives"`
}

func runTypeScriptScan(t *testing.T, root, repoDir string, options map[string]any, contributors []config.ContributorConfig) tsScanOutput {
	t.Helper()
	// The TypeScript takes contributors POST-zod, and zod's PublicPath transform has already
	// turned `avatars/x.png` into `/avatars/x.png` by then. The Go config keeps the value as
	// written and transforms it at use, so the request has to carry the transformed form for
	// the two sides to be comparing the same input.
	asParsed := make([]config.ContributorConfig, len(contributors))
	for i, c := range contributors {
		asParsed[i] = c
		if c.Avatar != "" {
			asParsed[i].Avatar = config.PublicPath(c.Avatar)
		}
	}
	request := map[string]any{
		"source":       map[string]any{"absPath": repoDir, "hostedRequests": []any{nil}},
		"options":      options,
		"contributors": asParsed,
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(requestPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(tsxBinary(root), filepath.Join("internal", "ingest", "testdata", "ts-scan.ts"), requestPath)
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("tsx ts-scan.ts: %v\n%s", err, stderr.String())
	}

	var out tsScanOutput
	dec := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	// A field the Go model does not know about means the two schemas have drifted, which is
	// exactly what this test exists to catch.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("decode TypeScript scan: %v", err)
	}
	return out
}

// scanOptionsFromMap builds the Go options from the same map the TypeScript side is given, so
// the two runs cannot be configured differently by accident.
func scanOptionsFromMap(t *testing.T, m map[string]any, contributors []config.ContributorConfig) ScanOptions {
	t.Helper()
	num := func(key string) int64 {
		v, ok := m[key]
		if !ok || v == nil {
			t.Fatalf("option %q is missing", key)
		}
		return int64(v.(int))
	}
	optNum := func(key string) *int64 {
		v, ok := m[key]
		if !ok || v == nil {
			return nil
		}
		n := int64(v.(int))
		return &n
	}
	insights := m["insights"].(map[string]any)
	opts := ScanOptions{
		MaxBlobBytes:       num("maxBlobBytes"),
		MaxCommits:         optNum("maxCommits"),
		MaxCommitAgeDays:   optNum("maxCommitAgeDays"),
		TagTrees:           int(num("tagTrees")),
		Archives:           m["archives"].(bool),
		HostedMaxFileBytes: num("hostedMaxFileBytes"),
		Insights: InsightsOptions{
			Enabled:           insights["enabled"].(bool),
			Samples:           insights["samples"].(int),
			MaxBytesPerSample: int64(insights["maxBytesPerSample"].(int)),
		},
		Contributors: BuildContributorIndex(contributors),
	}
	switch v := m["branchTrees"].(type) {
	case string:
		if v != "all" {
			t.Fatalf("branchTrees %q is not a number or \"all\"", v)
		}
		opts.BranchTreesAll = true
	case int:
		opts.BranchTrees = v
	default:
		t.Fatalf("branchTrees has type %T", v)
	}
	return opts
}

// encodeLikeArtifact serialises a Repo exactly as model.Serialize serialises the artifact
// around it, so a difference here is a difference in the file that ships.
func encodeLikeArtifact(t *testing.T, repo *model.Repo) string {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(repo); err != nil {
		t.Fatalf("encode repo: %v", err)
	}
	return buf.String()
}

// firstDifference reports the first differing line with a little context, which is far more
// useful than a diff of two several-thousand-line JSON documents.
func firstDifference(want, got string) string {
	wantLines := strings.Split(want, "\n")
	gotLines := strings.Split(got, "\n")
	for i := 0; i < len(wantLines) || i < len(gotLines); i++ {
		w, g := "", ""
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if i < len(gotLines) {
			g = gotLines[i]
		}
		if w == g {
			continue
		}
		var b strings.Builder
		for j := max(0, i-6); j < i; j++ {
			b.WriteString("   " + wantLines[j] + "\n")
		}
		b.WriteString("TS " + w + "\n")
		b.WriteString("GO " + g + "\n")
		return b.String()
	}
	return "(no line differs; the documents differ only in trailing bytes)"
}

func projectRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func tsxBinary(root string) string {
	name := "tsx"
	if runtime.GOOS == "windows" {
		name = "tsx.cmd"
	}
	return filepath.Join(root, "node_modules", ".bin", name)
}
