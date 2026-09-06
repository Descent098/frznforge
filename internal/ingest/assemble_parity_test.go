package ingest

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"frznforge/internal/model"
)

// The acceptance bar for the port is byte identity with the TypeScript ingest, so these tests
// run the real thing: they build an input, hand it to both implementations, and compare the
// records through the same Go encoder that writes the artifact.
//
// ts-scan.ts covers the scanner; internal/ingest/testdata/ts-assemble.ts covers the two halves
// of the assembly that have logic worth disagreeing about — reading a notes folder and
// resolving organization membership. Hosting is exercised through ResolveHostedBranch, which
// ts-scan.ts already compares, plus the unit tests in hosting_test.go.

// tsAssembleNotes mirrors the notes branch of ts-assemble.ts.
type tsAssembleNotes struct {
	Notes    []model.Note    `json:"notes"`
	Warnings []model.Warning `json:"warnings"`
	Blobs    map[string]int  `json:"blobs"`
}

// tsAssembleOrgs mirrors the orgs branch of ts-assemble.ts.
type tsAssembleOrgs struct {
	Organizations []model.Organization `json:"organizations"`
	Warnings      []model.Warning      `json:"warnings"`
}

func runTypeScriptAssemble(t *testing.T, root string, request map[string]any, out any) {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(requestPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(tsxBinary(root), filepath.Join("internal", "ingest", "testdata", "ts-assemble.ts"), requestPath)
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("tsx ts-assemble.ts: %v\n%s", err, stderr.String())
	}
	dec := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	// A field the Go model does not know about means the two schemas have drifted, which is
	// exactly what this test exists to catch.
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		t.Fatalf("decode TypeScript output: %v\n%s", err, stdout.String())
	}
}

// requireTsx skips when the TypeScript side cannot be run at all.
func requireTsx(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("parity test shells out to tsx")
	}
	root := projectRoot(t)
	if _, err := os.Stat(tsxBinary(root)); err != nil {
		t.Skipf("tsx is not installed (%s); run npm install to enable the parity test", tsxBinary(root))
	}
	return root
}

// notesParityFixture is one folder holding every note shape at once: a single-file note with
// full frontmatter, a folder note whose metadata comes from index.md, a folder that falls back
// to README.md, a folder with no markdown at all, an entry with no frontmatter and no heading,
// two more entries that slugify onto the same value, a binary file, an over-cap file, files
// whose names a static URL cannot round-trip, a CRLF file, a non-ASCII name, and a file whose
// bytes are not valid UTF-8.
func notesParityFixture(t *testing.T) string {
	t.Helper()
	files := map[string][]byte{
		"heat-buckets.md": []byte("---\ntitle: Heat buckets\ndescription: Fire to ice.\ndate: 2026-06-02\ntags: [design, css, design]\n---\n\n# Ignored, frontmatter wins\n\nprose\n"),
		"with-heading.md": []byte("intro\n\n# Actual Heading\n\nmore\n"),
		"no-heading.md":   []byte("just text, no heading\n"),
		"crlf.md":         []byte("---\r\ntitle: CRLF note\r\ndate: 2026-03-04 10:00:00\r\n---\r\n\r\n# Heading\r\n"),
		// Two entries that both slugify to "heat-buckets", plus the one above: the suffix
		// counter and the walk order both have to agree with the TypeScript.
		"Heat Buckets/index.md":            []byte("---\ntitle: Folder one\ntags:\n  - a\n  - b\n---\n\n# Folder one\n"),
		"Heat Buckets/bin/setup.sh":        []byte("#!/bin/sh\necho hi\n"),
		"Heat Buckets/ci/pages.yml":        []byte("name: pages\n"),
		"heat.buckets.md":                  []byte("# File three\n"),
		"guide/README.md":                  []byte("---\ntitle: From the readme\ndate: 2026-01-02T03:04:05+02:00\n---\n\nbody\n"),
		"guide/other.md":                   []byte("---\ntitle: Not used\n---\n\n# Not used either\n"),
		"static-host-configs/vercel.json":  []byte("{\"trailingSlash\": true}\n"),
		"static-host-configs/netlify.toml": []byte("[build]\n"),
		"awkward/index.md":                 []byte("# Awkward\n"),
		"awkward/c#-tips.md":               []byte("# sharp\n"),
		"awkward/50% off.txt":              []byte("fifty\n"),
		"awkward/read me.txt":              []byte("fine\n"),
		"assets/index.md":                  []byte("# Assets\n"),
		"assets/pixel.png":                 {0x89, 0x50, 0x4e, 0x47, 0x00, 0x01, 0x02, 0x00},
		"assets/big.txt":                   bytes.Repeat([]byte("x"), 200),
		// Ignored: the walk must not see either of these.
		"_wip.md":        []byte("skip"),
		".hidden/a.md":   []byte("skip"),
		"empty/_only.md": []byte("skip"),
		// A name git would quote and a name outside ASCII entirely.
		"caf" + string(rune(0xe9)) + ".md": []byte("# Cafe\n"),
		string(rune(0x65e5)) + "-log.txt":  []byte("nichi\n"),
		// Ill-formed UTF-8 in a file that is not binary (no NUL): Node replaces each maximal
		// subpart with one U+FFFD, and the title has to come out the same on both sides.
		"latin1.md": {'#', ' ', 'c', 'a', 'f', 0xe9, '\n'},
		// A date the artifact must drop, and a note with no frontmatter file at all.
		"undated/notes.md": []byte("---\ndate: March 4, 2026\n---\n\n# Plain Heading\n\nbody\n"),
	}
	return makeNotesDir(t, files)
}

func TestCollectNotesMatchesTypeScript(t *testing.T) {
	root := requireTsx(t)
	dir := notesParityFixture(t)

	for _, tc := range []struct {
		name    string
		request map[string]any
		options CollectNotesOptions
	}{
		{
			name:    "default cap",
			request: map[string]any{"mode": "notes", "dir": dir},
		},
		{
			// A cap that bites: over-cap files are streamed and sniffed rather than read, which
			// is the path with the most room to disagree.
			name:    "capped at 100 bytes",
			request: map[string]any{"mode": "notes", "dir": dir, "maxFileBytes": 100},
			options: CollectNotesOptions{MaxFileBytes: int64Ptr(100)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := collectFrom(t, dir, tc.options)
			// A fixture that quietly produced nothing would make every comparison below pass,
			// so the shapes it exists to cover are asserted before they are compared.
			if len(got.Notes) < 10 || len(got.Warnings) < 3 {
				t.Fatalf("the fixture collapsed: %d notes, %d warnings", len(got.Notes), len(got.Warnings))
			}

			var ts tsAssembleNotes
			runTypeScriptAssemble(t, root, tc.request, &ts)

			if want, have := encodeAsArtifact(t, ts.Notes), encodeAsArtifact(t, got.Notes); want != have {
				t.Fatalf("notes differ\n%s", firstDifference(want, have))
			}
			if want, have := encodeAsArtifact(t, ts.Warnings), encodeAsArtifact(t, got.Warnings); want != have {
				t.Fatalf("warnings differ\n%s", firstDifference(want, have))
			}
			if len(got.Blobs) != len(ts.Blobs) {
				t.Fatalf("blob count: Go %d, TypeScript %d", len(got.Blobs), len(ts.Blobs))
			}
			for sha, buf := range got.Blobs {
				size, ok := ts.Blobs[sha]
				if !ok {
					t.Fatalf("blob %s is missing from the TypeScript result", sha)
				}
				if len(buf) != size {
					t.Fatalf("blob %s: Go %d bytes, TypeScript %d", sha, len(buf), size)
				}
			}
		})
	}
}

func TestResolveOrganizationsMatchesTypeScript(t *testing.T) {
	root := requireTsx(t)
	// Every shape at once: a merged duplicate slug, a repo claimed from both directions, one
	// claimed by two orgs, a dangling repos entry listed twice, an org with no members, and a
	// repo naming an org that is not configured.
	organizations := []any{
		map[string]any{"slug": "zed", "name": "Zed", "description": "kept", "repos": []string{"alpha", "ghost", "ghost"}, "avatar": "logos/zed.png"},
		map[string]any{"slug": "acme", "name": "Acme", "repos": []string{"shared"}},
		map[string]any{"slug": "zed", "name": "Zed again", "description": "dropped", "repos": []string{"shared"}},
		map[string]any{"slug": "empty", "name": "Empty"},
	}
	repos := []map[string]any{
		{"slug": "zulu", "org": "nowhere"},
		{"slug": "alpha", "org": "acme"},
		{"slug": "shared", "org": nil},
		{"slug": "aardvark", "org": "nowhere"},
	}
	inputs := []OrgRepoInput{
		{Slug: "zulu", Org: "nowhere"},
		{Slug: "alpha", Org: "acme"},
		{Slug: "shared"},
		{Slug: "aardvark", Org: "nowhere"},
	}

	cfgJSON, err := json.Marshal(map[string]any{
		"owner":         map[string]any{"name": "Tester", "handle": "tester"},
		"organizations": organizations,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := testResolvedConfig(t, t.TempDir(), string(cfgJSON))
	got := ResolveOrganizations(cfg, inputs)

	if len(got.Organizations) != 3 || len(got.Warnings) != 3 {
		t.Fatalf("the fixture collapsed: %d orgs, %d warnings", len(got.Organizations), len(got.Warnings))
	}

	var ts tsAssembleOrgs
	runTypeScriptAssemble(t, root, map[string]any{
		"mode":   "orgs",
		"config": map[string]any{"organizations": organizations},
		"repos":  repos,
	}, &ts)

	if want, have := encodeAsArtifact(t, ts.Organizations), encodeAsArtifact(t, got.Organizations); want != have {
		t.Fatalf("organizations differ\n%s", firstDifference(want, have))
	}
	if want, have := encodeAsArtifact(t, ts.Warnings), encodeAsArtifact(t, got.Warnings); want != have {
		t.Fatalf("warnings differ\n%s", firstDifference(want, have))
	}
}

func int64Ptr(n int64) *int64 { return &n }

// encodeAsArtifact serialises any artifact fragment exactly as model.Serialize serialises the
// document around it, so a difference here is a difference in the file that ships.
func encodeAsArtifact(t *testing.T, v any) string {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.String()
}
