package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The artifact is frozen at schema v8 for all of 0.4.0 (see the package comment), which is
// what lets these tests be byte-identity statements rather than judgement calls. Every one of
// them fails on a real, specific mistake — they are listed as traps in
// docs/dev/plans/version-0.4.0-phased.md Phase 3.

func ptr[T any](v T) *T { return &v }

// TestRoundTripFixture is the contract test: an artifact written by the TypeScript ingest,
// parsed and re-emitted by Go, must come back byte for byte.
//
// The fixture is the one `tests/e2e/global-setup.ts` builds — five repos covering local,
// github and gitea sources, provider releases, annotated and lightweight tags, ref trees,
// archives, a hosted site, twelve warnings, and null licenses and insights. It was produced by
// the OTHER implementation, which is what makes this non-circular.
func TestRoundTripFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "fixture-forge.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	data, err := Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out, err := Serialize(data)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	if !bytes.Equal(raw, out) {
		t.Fatalf("re-serialized artifact differs from the fixture\n%s", firstDiff(raw, out))
	}
}

// TestRoundTripLiveArtifacts runs the same check against whatever real artifacts happen to be
// on this machine. Skipped when they are absent, so a clean checkout still passes — the
// committed fixture is the one that must always run.
func TestRoundTripLiveArtifacts(t *testing.T) {
	candidates := []string{
		filepath.Join("..", "..", "data", "forge.json"),
		filepath.Join("..", "..", "tests", ".tmp", "e2e", "data", "forge.json"),
	}
	ran := 0
	for _, path := range candidates {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		ran++
		t.Run(filepath.ToSlash(path), func(t *testing.T) {
			data, err := Parse(raw)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			out, err := Serialize(data)
			if err != nil {
				t.Fatalf("serialize: %v", err)
			}
			if !bytes.Equal(raw, out) {
				t.Fatalf("differs\n%s", firstDiff(raw, out))
			}
		})
	}
	if ran == 0 {
		t.Skip("no live artifact on this machine; the fixture test covers the contract")
	}
}

// TestSerializeLeavesHTMLAlone is the trap that would break essentially every repository:
// encoding/json escapes <, > and & by default, JSON.stringify does not, and READMEs are full
// of all three.
func TestSerializeLeavesHTMLAlone(t *testing.T) {
	d := EmptyForgeData()
	d.Repos = append(d.Repos, minimalRepo("alpha"))
	d.Repos[0].Description = ptr(`a <script> & "quotes" > here`)

	out, err := Serialize(d)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, `a <script> & \"quotes\" > here`) {
		t.Errorf("expected raw < > & in the output, got:\n%s", got)
	}
	// The six-character unicode escapes Go's default marshaller would have produced, built
	// rather than typed so no complete escape sequence appears in this source file.
	for _, r := range []rune{'<', '>', '&'} {
		esc := fmt.Sprintf("\\u%04x", r)
		if strings.Contains(got, esc) {
			t.Errorf("output contains %s — SetEscapeHTML(false) is not in effect", esc)
		}
	}
}

// TestOptionalIsAbsentNullableIsNull pins the distinction the Go port has to make by hand,
// because both look like a pointer: zod `.optional()` omits the key, `.nullable()` emits null.
func TestOptionalIsAbsentNullableIsNull(t *testing.T) {
	d := EmptyForgeData()
	r := minimalRepo("alpha")
	r.Links = RepoLinks{Issues: ptr("https://example.com/issues")} // homepage/donations/upstream absent
	r.Description = nil                                            // nullable → must appear as null
	d.Repos = append(d.Repos, r)

	out, err := Serialize(d)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, `"description": null`) {
		t.Error(`nullable "description" must be emitted as null, not omitted`)
	}
	if strings.Contains(got, `"homepage"`) || strings.Contains(got, `"donations"`) || strings.Contains(got, `"upstream"`) {
		t.Error("optional link keys must be absent when unset, not null")
	}
	if !strings.Contains(got, `"issues": "https://example.com/issues"`) {
		t.Error("a set optional link should still be emitted")
	}
}

// TestEmptyCollectionsAreNotNull guards the nil-slice hazard: a nil []T marshals as `null`,
// and the artifact says `[]`.
func TestEmptyCollectionsAreNotNull(t *testing.T) {
	out, err := Serialize(EmptyForgeData())
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, key := range []string{"repos", "notes", "organizations", "hosting", "warnings"} {
		if !strings.Contains(got, `"`+key+`": []`) {
			t.Errorf("expected %q to serialize as [], got:\n%s", key, got)
		}
	}
}

// TestSerializeEndsWithExactlyOneNewline — Encoder.Encode appends one, matching
// `JSON.stringify(...) + '\n'`. Adding another would double it.
func TestSerializeEndsWithExactlyOneNewline(t *testing.T) {
	out, err := Serialize(EmptyForgeData())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(out, []byte("}\n")) {
		t.Errorf("artifact must end with exactly one newline, got %q", string(out[max(0, len(out)-8):]))
	}
}

// TestMapKeysEmitSorted documents why plain Go maps are safe for commits/files/refTrees:
// encoding/json sorts map keys, and the TypeScript side inserts them already sorted, so the
// two orders agree. The fixture round-trip is the real proof; this states the reason.
func TestMapKeysEmitSorted(t *testing.T) {
	d := EmptyForgeData()
	r := minimalRepo("alpha")
	r.Files = map[string]FileInfo{
		"z.txt":     {Path: "z.txt", Sha: strings.Repeat("c", 40)},
		"a.txt":     {Path: "a.txt", Sha: strings.Repeat("a", 40)},
		"m/deep.md": {Path: "m/deep.md", Sha: strings.Repeat("b", 40)},
	}
	d.Repos = append(d.Repos, r)
	out, err := Serialize(d)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	ia, im, iz := strings.Index(got, `"a.txt"`), strings.Index(got, `"m/deep.md"`), strings.Index(got, `"z.txt"`)
	if !(ia < im && im < iz) {
		t.Errorf("map keys must be emitted in sorted order, got positions a=%d m=%d z=%d", ia, im, iz)
	}
}

// TestRepoSourceVariantsEmitTheirOwnKeys — the discriminated union is one Go struct, so each
// variant has to emit exactly the keys (and the order) its TypeScript member does.
func TestRepoSourceVariantsEmitTheirOwnKeys(t *testing.T) {
	cases := []struct {
		name   string
		source RepoSource
		want   string
	}{
		{"local", RepoSource{Type: "local", Path: ptr("../x")},
			`{"type":"local","path":"../x"}`},
		{"github", RepoSource{Type: "github", Host: ptr("https://api.github.com"), Owner: ptr("o"), Repo: ptr("r"), WebURL: ptr("https://github.com/o/r"), CloneURL: ptr("https://github.com/o/r.git")},
			`{"type":"github","host":"https://api.github.com","owner":"o","repo":"r","webUrl":"https://github.com/o/r","cloneUrl":"https://github.com/o/r.git"}`},
		{"gitlab", RepoSource{Type: "gitlab", Host: ptr("https://gitlab.com"), Project: ptr("g/s/p"), WebURL: ptr("https://gitlab.com/g/s/p"), CloneURL: ptr("https://gitlab.com/g/s/p.git")},
			`{"type":"gitlab","host":"https://gitlab.com","project":"g/s/p","webUrl":"https://gitlab.com/g/s/p","cloneUrl":"https://gitlab.com/g/s/p.git"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			enc.SetEscapeHTML(false)
			if err := enc.Encode(tc.source); err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(buf.String()); got != tc.want {
				t.Errorf("\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}

func TestValidateRejectsAWrongSchemaVersion(t *testing.T) {
	d := EmptyForgeData()
	d.SchemaVersion = 7
	err := Validate(&d)
	if err == nil {
		t.Fatal("expected an error for schema v7")
	}
	// The message has to tell a reader what to DO, not just what is wrong: an artifact left
	// over from an older version is fixed by re-running the build.
	if !strings.Contains(err.Error(), "re-run the build") {
		t.Errorf("error should say how to fix it, got: %v", err)
	}
}

func TestValidateRejectsMalformedValues(t *testing.T) {
	cases := map[string]func(*ForgeData){
		"short sha": func(d *ForgeData) {
			d.Repos[0].Commits = map[string]Commit{"abc": {Sha: "abc"}}
		},
		"non-ISO date": func(d *ForgeData) {
			d.Repos[0].Commits = map[string]Commit{
				strings.Repeat("a", 40): {Sha: strings.Repeat("a", 40), AuthorDate: "2026-01-01", CommitDate: "2026-01-01T00:00:00Z"},
			}
		},
		"bad slug": func(d *ForgeData) { d.Repos[0].Slug = "Not A Slug" },
		"unknown warning code": func(d *ForgeData) {
			d.Repos[0].Warnings = []Warning{{Code: "made-up", Message: "x"}}
		},
		"commit in both maps": func(d *ForgeData) {
			sha := strings.Repeat("a", 40)
			c := Commit{Sha: sha, AuthorDate: "2026-01-01T00:00:00Z", CommitDate: "2026-01-01T00:00:00Z"}
			d.Repos[0].Commits = map[string]Commit{sha: c}
			d.Repos[0].ExtraCommits = map[string]Commit{sha: c}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			d := EmptyForgeData()
			d.Repos = append(d.Repos, minimalRepo("alpha"))
			mutate(&d)
			if err := Validate(&d); err == nil {
				t.Error("expected validation to fail")
			}
		})
	}
}

// TestParseRejectsUnknownFields — an artifact from a NEWER frznforge must not round-trip
// through here silently losing whatever it added.
func TestParseRejectsUnknownFields(t *testing.T) {
	raw := []byte(`{"schemaVersion":8,"repos":[],"notes":[],"organizations":[],"hosting":[],"warnings":[],"somethingNew":1}`)
	if _, err := Parse(raw); err == nil {
		t.Fatal("expected an error for an unknown top-level field")
	}
}

func minimalRepo(slug string) Repo {
	return Repo{
		Slug:         slug,
		Name:         slug,
		Source:       RepoSource{Type: "local", Path: ptr("./" + slug)},
		Tags:         []string{},
		ReleaseMode:  "tags",
		Releases:     []Release{},
		Branches:     []Branch{},
		GitTags:      []Tag{},
		Commits:      map[string]Commit{},
		ExtraCommits: map[string]Commit{},
		Tree:         []TreeEntry{},
		Files:        map[string]FileInfo{},
		RefTrees:     *NewRefTreeMap(),
		Archives:     []Archive{},
		Languages:    []LanguageStat{},
		Contributors: []Contributor{},
		Warnings:     []Warning{},
	}
}

func firstDiff(want, got []byte) string {
	i := 0
	for i < len(want) && i < len(got) && want[i] == got[i] {
		i++
	}
	lo := max(0, i-80)
	return "at byte " + itoa(i) + " (line " + itoa(1+bytes.Count(want[:i], []byte("\n"))) + ")\n" +
		"  want: " + quote(want, lo, i+80) + "\n" +
		"  got:  " + quote(got, lo, i+80)
}

func quote(b []byte, lo, hi int) string {
	if hi > len(b) {
		hi = len(b)
	}
	if lo > len(b) {
		lo = len(b)
	}
	return string(b[lo:hi])
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
