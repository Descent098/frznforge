package ingest

import (
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"frznforge/internal/config"
	"frznforge/internal/model"
)

/* ---- helpers -------------------------------------------------------------- */

// makeNotesDir writes a notes folder into a temp directory. Keys use '/' and may nest.
func makeNotesDir(t *testing.T, files map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func textFiles(files map[string]string) map[string][]byte {
	out := make(map[string][]byte, len(files))
	for k, v := range files {
		out[k] = []byte(v)
	}
	return out
}

// testResolvedConfig parses a config the way the real loader does, so notesConfigured and every
// default match a real run.
func testResolvedConfig(t *testing.T, root, jsonc string) *config.Resolved {
	t.Helper()
	cfg, err := config.ParseBytes([]byte(jsonc))
	if err != nil {
		t.Fatalf("parse test config: %v", err)
	}
	resolved, err := config.Resolve(cfg, root)
	if err != nil {
		t.Fatalf("resolve test config: %v", err)
	}
	return resolved
}

// bareConfig is the minimum a config needs: no notes block, so notes-dir-missing stays quiet
// unless a test names a directory.
func bareConfig(t *testing.T) *config.Resolved {
	t.Helper()
	return testResolvedConfig(t, t.TempDir(), `{"owner": {"name": "Tester", "handle": "tester"}}`)
}

func collectFrom(t *testing.T, dir string, options CollectNotesOptions) CollectNotesResult {
	t.Helper()
	options.Dir = dir
	res, err := CollectNotes(bareConfig(t), options)
	if err != nil {
		t.Fatalf("CollectNotes: %v", err)
	}
	return res
}

// noteShaOf is the blob-store key a note file must get: sha1 over "note <len>\0" + the bytes.
func noteShaOf(b []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "note %d\x00", len(b))
	h.Write(b)
	return fmt.Sprintf("%x", h.Sum(nil))
}

// gitShaOf is the git object id of the same bytes, which is what a repo file is keyed by.
func gitShaOf(b []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(b))
	h.Write(b)
	return fmt.Sprintf("%x", h.Sum(nil))
}

func noteBySlug(notes []model.Note, slug string) *model.Note {
	for i := range notes {
		if notes[i].Slug == slug {
			return &notes[i]
		}
	}
	return nil
}

func notePaths(n model.Note) []string {
	out := make([]string, len(n.Files))
	for i, f := range n.Files {
		out[i] = f.Path
	}
	return out
}

/* ---- name and date helpers ------------------------------------------------ */

func TestHumaniseNameAndNoteSlug(t *testing.T) {
	for _, tc := range []struct{ in, humanised, slug string }{
		{"check-determinism.ps1", "Check determinism", "check-determinism"},
		{"static-host-configs", "Static host configs", "static-host-configs"},
		{"xdg_base.dirs.txt", "Xdg base dirs", "xdg-base-dirs"},
		{"Static Host Configs", "Static Host Configs", "static-host-configs"},
		{"!!!.md", "!!!", "note"},
		{".env", "Env", "env"},
		{"---.md", "", "note"},
		{"   ", "", "note"},
		// JavaScript's toUpperCase is the FULL Unicode mapping; Go's is the simple one, so
		// without jsUpperCaseSpecials this title would come out as "ßeta notes".
		{"ßeta-notes.md", "SSeta notes", "eta-notes"},
	} {
		if got := HumaniseName(tc.in); got != tc.humanised {
			t.Errorf("HumaniseName(%q) = %q, want %q", tc.in, got, tc.humanised)
		}
		if got := NoteSlug(tc.in); got != tc.slug {
			t.Errorf("NoteSlug(%q) = %q, want %q", tc.in, got, tc.slug)
		}
	}
}

func TestNormaliseNoteDate(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"2026-05-14", "2026-05-14T00:00:00Z"},
		{"  2026-05-14  ", "2026-05-14T00:00:00Z"},
		{"2026-05-14T09:30:00+02:00", "2026-05-14T07:30:00Z"},
		// A zone-less date-time is UTC, not the build machine's zone. Date.parse reads it as
		// LOCAL time, which is exactly what this function exists to stop.
		{"2026-03-04 10:00:00", "2026-03-04T10:00:00Z"},
		{"2026-03-04T10:00:00", "2026-03-04T10:00:00Z"},
		{"2026-03-04T10:00", "2026-03-04T10:00:00Z"},
		{"2026-03-04T10:00:00.250", "2026-03-04T10:00:00Z"},
		{"2026-03-04T10:00:00Z", "2026-03-04T10:00:00Z"},
		{"2026-03-04T10:00:00-07:00", "2026-03-04T17:00:00Z"},
		// Hour 24 is midnight ending the day — the one hour ECMA-262 lets past 23.
		{"2026-03-04T24:00:00Z", "2026-03-05T00:00:00Z"},
		// A date-TIME is only range-checked, so an impossible day rolls over rather than being
		// rejected. That is what Date.parse does, and the artifact has to agree with it.
		{"2026-02-31T00:00:00Z", "2026-03-03T00:00:00Z"},
		// …while a bare date is calendar-checked by round-tripping through Date.UTC.
		{"2026-02-31", ""},
		{"someday", ""},
		{"", ""},
		{"March 4, 2026", ""},
		{"03/04/2026", ""},
		{"Wed, 04 Mar 2026 10:00:00 GMT", ""},
		{"2026-03-04 10:00:00 PST", ""},
		{"2026-03-04T24:00:01Z", ""},
		{"2026-03-04T10:60:00Z", ""},
		{"2026-03-04T10:00:60Z", ""},
		{"2026-03-04T10:00:00+24:00", ""},
		{"2026-13-04T10:00:00Z", ""},
		{"2026-03-00T10:00:00Z", ""},
		// Date.UTC maps a year of 0-99 onto 1900+y, so its own round-trip check rejects a
		// four-digit year below 0100 — but only on the date-only path.
		{"0026-03-04", ""},
		{"0026-03-04T10:00:00Z", "0026-03-04T10:00:00Z"},
	} {
		got := NormaliseNoteDate(tc.in)
		switch {
		case tc.want == "" && got != nil:
			t.Errorf("NormaliseNoteDate(%q) = %q, want null", tc.in, *got)
		case tc.want != "" && got == nil:
			t.Errorf("NormaliseNoteDate(%q) = null, want %q", tc.in, tc.want)
		case tc.want != "" && *got != tc.want:
			t.Errorf("NormaliseNoteDate(%q) = %q, want %q", tc.in, *got, tc.want)
		}
	}
}

// The artifact must not depend on the machine's zone, so the whole function is checked with
// TZ pinned to three different places.
func TestNormaliseNoteDateIgnoresHostZone(t *testing.T) {
	values := []string{"2026-03-04", "2026-03-04 10:00:00", "2026-03-04T10:00:00", "2026-03-04T10:00:00+02:00"}
	want := []string{"2026-03-04T00:00:00Z", "2026-03-04T10:00:00Z", "2026-03-04T10:00:00Z", "2026-03-04T08:00:00Z"}
	for _, zone := range []string{"Australia/Sydney", "UTC", "America/Edmonton"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Skipf("no zone database for %s on this machine", zone)
		}
		previous := time.Local
		time.Local = loc
		for i, v := range values {
			got := NormaliseNoteDate(v)
			if got == nil || *got != want[i] {
				t.Errorf("in %s: NormaliseNoteDate(%q) = %v, want %q", zone, v, got, want[i])
			}
		}
		time.Local = previous
	}
}

/* ---- ordering ------------------------------------------------------------- */

// Go's `<` compares UTF-8 bytes and JavaScript's compares UTF-16 code units. They disagree
// exactly where an astral character meets one in U+E000-U+FFFF, and walk order decides which
// note keeps the bare slug.
func TestCompareUTF16MatchesJavaScriptOrder(t *testing.T) {
	emoji := string(rune(0x1F600))    // surrogate pair D83D DE00 in UTF-16
	fullwidth := string(rune(0xFF21)) // BMP, above the surrogate range
	if compareUTF16(emoji, fullwidth) >= 0 {
		t.Errorf("compareUTF16(emoji, fullwidth) = %d, want < 0", compareUTF16(emoji, fullwidth))
	}
	if strings.Compare(emoji, fullwidth) <= 0 {
		t.Fatal("this test is pointless unless Go's byte order disagrees; it no longer does")
	}
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"a", "b", -1}, {"b", "a", 1}, {"a", "a", 0},
		{"Heat", "heat", -1}, // 'H' 0x48 before 'h' 0x68
		{"a", "ab", -1},
	} {
		if got := compareUTF16(tc.a, tc.b); got != tc.want {
			t.Errorf("compareUTF16(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

/* ---- collectNotes --------------------------------------------------------- */

func TestCollectNotesMissingDirectory(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	res := collectFrom(t, missing, CollectNotesOptions{})
	if len(res.Notes) != 0 || len(res.Blobs) != 0 {
		t.Fatalf("want nothing collected, got %d notes / %d blobs", len(res.Notes), len(res.Blobs))
	}
	if got := warningCodes(res.Warnings); !reflect.DeepEqual(got, []string{"notes-dir-missing"}) {
		t.Fatalf("warnings = %v", got)
	}
	if res.Warnings[0].Repo != nil {
		t.Error("notes-dir-missing is site-level; it must not name a repo")
	}
	if !strings.Contains(res.Warnings[0].Message, missing) {
		t.Errorf("message does not name the folder: %q", res.Warnings[0].Message)
	}
}

func TestCollectNotesPathIsAFile(t *testing.T) {
	dir := makeNotesDir(t, textFiles(map[string]string{"a.md": "x"}))
	res := collectFrom(t, filepath.Join(dir, "a.md"), CollectNotesOptions{})
	if got := warningCodes(res.Warnings); !reflect.DeepEqual(got, []string{"notes-dir-missing"}) {
		t.Fatalf("warnings = %v", got)
	}
}

// A defaulted notes.dir that does not exist is NOT a warning: a site that never opted into
// notes would otherwise carry a permanent "1 ingest warning" in its footer.
func TestCollectNotesSilentWhenNotesNeverConfigured(t *testing.T) {
	cfg := bareConfig(t)
	res, err := CollectNotes(cfg, CollectNotesOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warningCodes(res.Warnings))
	}

	root := t.TempDir()
	declared := testResolvedConfig(t, root,
		`{"owner": {"name": "Tester", "handle": "tester"}, "notes": {"dir": "./content/notes"}}`)
	res, err = CollectNotes(declared, CollectNotesOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := warningCodes(res.Warnings); !reflect.DeepEqual(got, []string{"notes-dir-missing"}) {
		t.Fatalf("a declared notes block must warn; warnings = %v", got)
	}
}

func TestCollectNotesSingleFileWithFrontmatter(t *testing.T) {
	src := "---\ntitle: Heat buckets\ndescription: Fire to ice.\ndate: 2026-06-02\ntags: [design, css, design]\n---\n\n# Ignored, frontmatter wins\n\nprose\n"
	dir := makeNotesDir(t, textFiles(map[string]string{"heat-buckets.md": src}))
	res := collectFrom(t, dir, CollectNotesOptions{})
	if len(res.Warnings) != 0 {
		t.Fatalf("warnings = %v", warningCodes(res.Warnings))
	}
	if len(res.Notes) != 1 {
		t.Fatalf("want 1 note, got %d", len(res.Notes))
	}
	note := res.Notes[0]
	if note.Slug != "heat-buckets" || note.Title != "Heat buckets" || note.Kind != "file" {
		t.Errorf("note = %+v", note)
	}
	if note.Description == nil || *note.Description != "Fire to ice." {
		t.Errorf("description = %v", note.Description)
	}
	if !reflect.DeepEqual(note.Tags, []string{"design", "css"}) {
		t.Errorf("tags = %v, want de-duplicated in author order", note.Tags)
	}
	if note.Date == nil || *note.Date != "2026-06-02T00:00:00Z" {
		t.Errorf("date = %v", note.Date)
	}
	if note.TotalBytes != int64(len(src)) {
		t.Errorf("totalBytes = %d, want %d", note.TotalBytes, len(src))
	}
	want := model.NoteFile{
		Name: "heat-buckets.md", Path: "heat-buckets.md", Sha: noteShaOf([]byte(src)),
		Size: int64(len(src)), Stored: true, Language: noteStrPtr("Markdown"), Markdown: true,
	}
	if !reflect.DeepEqual(note.Files, []model.NoteFile{want}) {
		t.Errorf("files = %+v\nwant %+v", note.Files, want)
	}
	if string(res.Blobs[note.Files[0].Sha]) != src {
		t.Error("the blob store does not hold the file's bytes under its sha")
	}
}

func TestCollectNotesTitleFallbacks(t *testing.T) {
	dir := makeNotesDir(t, textFiles(map[string]string{
		"with-heading.md": "intro\n\n# Actual Heading\n\nmore\n",
		"no-heading.md":   "just text, no heading\n",
	}))
	res := collectFrom(t, dir, CollectNotesOptions{})
	if got := noteBySlug(res.Notes, "with-heading"); got == nil || got.Title != "Actual Heading" {
		t.Errorf("with-heading title = %v", got)
	}
	if got := noteBySlug(res.Notes, "no-heading"); got == nil || got.Title != "No heading" {
		t.Errorf("no-heading title = %v", got)
	}
	for _, n := range res.Notes {
		if n.Date != nil || n.Description != nil || len(n.Tags) != 0 {
			t.Errorf("%s carries metadata it was never given: %+v", n.Slug, n)
		}
	}
}

// The H1 fallback runs on PROSE, never on raw file text: a '#' line inside a frontmatter block
// is a YAML comment, and scanning the raw file promoted it to the note's title.
func TestCollectNotesHeadingComesFromProse(t *testing.T) {
	dir := makeNotesDir(t, textFiles(map[string]string{
		"guide/overview.md":     "---\ntitle: Ignored by design\n# a note to self\ndate: 2026-01-01\n---\n\n# Real Heading\n\nbody\n",
		"plain-folder/notes.md": "---\ndate: 2026-01-01\n---\n\n# Plain Heading\n\nbody\n",
	}))
	res := collectFrom(t, dir, CollectNotesOptions{})
	guide := noteBySlug(res.Notes, "guide")
	if guide == nil || guide.Title != "Real Heading" {
		t.Fatalf("guide = %+v", guide)
	}
	// overview.md is neither index.md nor README.md, so its keys are content, not metadata.
	if guide.Date != nil || len(guide.Tags) != 0 {
		t.Errorf("a non-frontmatter file's keys became note metadata: %+v", guide)
	}
	if plain := noteBySlug(res.Notes, "plain-folder"); plain == nil || plain.Title != "Plain Heading" {
		t.Errorf("plain-folder = %+v", plain)
	}
}

func TestCollectNotesFolderNote(t *testing.T) {
	dir := makeNotesDir(t, textFiles(map[string]string{
		"deploying/index.md":           "---\ntitle: Deploying a frozen forge\ndate: 2026-07-11\ntags:\n  - deployment\n  - ci\n---\n\nprose\n",
		"deploying/deploy.sh":          "#!/bin/sh\necho hi\n",
		"deploying/ci/pages.yml":       "name: pages\n",
		"deploying/ci/nested/serve.py": "print(1)\n",
		"deploying/_draft.md":          "ignored",
		"deploying/.secret":            "ignored",
	}))
	res := collectFrom(t, dir, CollectNotesOptions{})
	if len(res.Notes) != 1 {
		t.Fatalf("want 1 note, got %d", len(res.Notes))
	}
	note := res.Notes[0]
	if note.Slug != "deploying" || note.Title != "Deploying a frozen forge" || note.Kind != "folder" {
		t.Errorf("note = %+v", note)
	}
	if note.Date == nil || *note.Date != "2026-07-11T00:00:00Z" {
		t.Errorf("date = %v", note.Date)
	}
	if !reflect.DeepEqual(note.Tags, []string{"deployment", "ci"}) {
		t.Errorf("tags = %v", note.Tags)
	}
	want := []string{"ci/nested/serve.py", "ci/pages.yml", "deploy.sh", "index.md"}
	if !reflect.DeepEqual(notePaths(note), want) {
		t.Errorf("paths = %v, want %v", notePaths(note), want)
	}
	for _, f := range note.Files {
		if strings.Contains(f.Path, `\`) {
			t.Errorf("a Windows separator reached the artifact: %q", f.Path)
		}
	}
	total := int64(0)
	for _, f := range note.Files {
		total += f.Size
	}
	if note.TotalBytes != total {
		t.Errorf("totalBytes = %d, want %d", note.TotalBytes, total)
	}
}

func TestCollectNotesFolderFrontmatterFromReadme(t *testing.T) {
	dir := makeNotesDir(t, textFiles(map[string]string{
		"guide/README.md": "---\ntitle: From the readme\n---\n\nbody\n",
		"guide/other.md":  "---\ntitle: Not used\n---\n\n# Not used either\n",
	}))
	res := collectFrom(t, dir, CollectNotesOptions{})
	if res.Notes[0].Title != "From the readme" {
		t.Errorf("title = %q", res.Notes[0].Title)
	}
}

func TestCollectNotesSkipsIgnoredEntriesAndEmptyFolders(t *testing.T) {
	dir := makeNotesDir(t, textFiles(map[string]string{
		"kept.md":                   "# Kept\n",
		"_wip.md":                   "skip",
		".gitkeep":                  "",
		"_drafts/a.md":              "skip",
		".hidden/b.md":              "skip",
		"empty-ish/_only-drafts.md": "skip",
	}))
	res := collectFrom(t, dir, CollectNotesOptions{})
	if len(res.Notes) != 1 || res.Notes[0].Slug != "kept" {
		t.Fatalf("notes = %v", res.Notes)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("warnings = %v", warningCodes(res.Warnings))
	}
}

func TestCollectNotesBinaryAndOverCapFiles(t *testing.T) {
	binary := []byte{0x89, 0x50, 0x4e, 0x47, 0x00, 0x01, 0x02, 0x00}
	big := strings.Repeat("x", 200) + "\n"
	dir := makeNotesDir(t, map[string][]byte{
		"assets/pixel.png": binary,
		"assets/big.txt":   []byte(big),
		"assets/index.md":  []byte("# Assets\n"),
	})
	cap100 := int64(100)
	res := collectFrom(t, dir, CollectNotesOptions{MaxFileBytes: &cap100})
	files := map[string]model.NoteFile{}
	for _, f := range res.Notes[0].Files {
		files[f.Path] = f
	}
	if got := files["pixel.png"]; !got.Binary || got.TooLarge || !got.Stored || got.Language != nil || got.Size != 8 {
		t.Errorf("pixel.png = %+v", got)
	}
	// Over the cap: hashed in full, classified, but not stored.
	got := files["big.txt"]
	if got.Binary || !got.TooLarge || got.Stored || got.Size != int64(len(big)) {
		t.Errorf("big.txt = %+v", got)
	}
	if got.Sha != noteShaOf([]byte(big)) {
		t.Errorf("big.txt sha = %s, want the hash of every byte", got.Sha)
	}
	if _, stored := res.Blobs[got.Sha]; stored {
		t.Error("an over-cap file was stored anyway")
	}
	if !reflect.DeepEqual(res.Blobs[files["pixel.png"].Sha], binary) {
		t.Error("a small binary must still be stored")
	}
	if want := int64(8 + len(big) + len("# Assets\n")); res.Notes[0].TotalBytes != want {
		t.Errorf("totalBytes = %d, want %d", res.Notes[0].TotalBytes, want)
	}
}

// An over-cap file is streamed, so its binary flag comes from a sniff of the first bytes
// rather than from content the build never keeps.
func TestCollectNotesSniffsOversizedBinary(t *testing.T) {
	buf := append(append([]byte("lead"), 0x00), []byte(strings.Repeat("A", 300))...)
	dir := makeNotesDir(t, map[string][]byte{"blob.bin": buf})
	cap100 := int64(100)
	res := collectFrom(t, dir, CollectNotesOptions{MaxFileBytes: &cap100})
	f := res.Notes[0].Files[0]
	if !f.Binary || !f.TooLarge || f.Stored || f.Size != int64(len(buf)) || f.Sha != noteShaOf(buf) {
		t.Errorf("blob.bin = %+v", f)
	}
	if len(res.Blobs) != 0 {
		t.Errorf("blobs = %d, want none", len(res.Blobs))
	}
}

func TestCollectNotesSlugCollisions(t *testing.T) {
	dir := makeNotesDir(t, textFiles(map[string]string{
		"Heat Buckets/index.md": "# Folder one\n",
		"heat-buckets.md":       "# File two\n",
		"heat.buckets.md":       "# File three\n",
	}))
	res := collectFrom(t, dir, CollectNotesOptions{})
	byTitle := map[string]string{}
	for _, n := range res.Notes {
		byTitle[n.Title] = n.Slug
	}
	// Walk order is code-point order on the entry name: 'H' (0x48) sorts before 'h' (0x68).
	want := map[string]string{"Folder one": "heat-buckets", "File two": "heat-buckets-2", "File three": "heat-buckets-3"}
	if !reflect.DeepEqual(byTitle, want) {
		t.Errorf("slugs by title = %v, want %v", byTitle, want)
	}
	if got := warningCodes(res.Warnings); !reflect.DeepEqual(got, []string{"note-slug-collision", "note-slug-collision"}) {
		t.Errorf("warnings = %v", got)
	}
	for _, w := range res.Warnings {
		if w.Repo != nil {
			t.Error("note warnings are site-level")
		}
	}
}

func TestCollectNotesOrdering(t *testing.T) {
	dated := func(title, date string) string {
		return fmt.Sprintf("---\ntitle: %s\ndate: %s\n---\n", title, date)
	}
	dir := makeNotesDir(t, textFiles(map[string]string{
		"older.md":      dated("Older", "2026-01-02"),
		"newer.md":      dated("Newer", "2026-03-04"),
		"zebra.md":      "# Zebra\n",
		"apple.md":      "# Apple\n",
		"same-day-b.md": dated("Beta", "2026-03-04"),
	}))
	res := collectFrom(t, dir, CollectNotesOptions{})
	titles := make([]string, len(res.Notes))
	for i, n := range res.Notes {
		titles[i] = n.Title
	}
	want := []string{"Beta", "Newer", "Older", "Apple", "Zebra"}
	if !reflect.DeepEqual(titles, want) {
		t.Errorf("order = %v, want %v (date desc, undated last, then title)", titles, want)
	}
}

func TestCollectNotesIsDeterministic(t *testing.T) {
	dir := makeNotesDir(t, textFiles(map[string]string{
		"one.md":        "---\ntitle: One\ndate: 2026-04-01\ntags: [a]\n---\n\n# One\n",
		"two/index.md":  "# Two\n",
		"two/data.json": "{\"a\":1}\n",
		"three.txt":     "plain\n",
	}))
	first := collectFrom(t, dir, CollectNotesOptions{})
	second := collectFrom(t, dir, CollectNotesOptions{})
	a, err := json.Marshal(first.Notes)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(second.Notes)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatalf("two runs differ:\n%s\n%s", a, b)
	}
	// Every stored file is in the blob store, keyed by its own hash, and nothing else is.
	stored := map[string]bool{}
	for _, n := range first.Notes {
		for _, f := range n.Files {
			if f.Stored {
				stored[f.Sha] = true
			}
		}
	}
	if len(stored) != len(first.Blobs) {
		t.Errorf("%d stored files but %d blobs", len(stored), len(first.Blobs))
	}
	for sha, buf := range first.Blobs {
		if !stored[sha] {
			t.Errorf("blob %s belongs to no file", sha)
		}
		if noteShaOf(buf) != sha {
			t.Errorf("blob %s is not the hash of its own bytes", sha)
		}
	}
}

func TestCollectNotesUseMtime(t *testing.T) {
	dir := makeNotesDir(t, textFiles(map[string]string{"undated.md": "# Undated\n"}))
	when := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(dir, "undated.md"), when, when); err != nil {
		t.Fatal(err)
	}
	if got := collectFrom(t, dir, CollectNotesOptions{}).Notes[0].Date; got != nil {
		t.Errorf("date = %q, want null without notes.useMtime", *got)
	}
	on := true
	got := collectFrom(t, dir, CollectNotesOptions{UseMtime: &on}).Notes[0].Date
	if got == nil || *got != "2026-02-03T04:05:06Z" {
		t.Errorf("date = %v, want the file's mtime", got)
	}
}

func TestCollectNotesReadsConfigWhenNoOptionsGiven(t *testing.T) {
	root := t.TempDir()
	notesDir := filepath.Join(root, "content", "notes")
	if err := os.MkdirAll(notesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(notesDir, "capped.md"),
		[]byte("# Capped\n"+strings.Repeat("y", 100)), 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 7, 8, 9, 10, 11, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(notesDir, "capped.md"), when, when); err != nil {
		t.Fatal(err)
	}
	cfg := testResolvedConfig(t, root, `{
      "owner": {"name": "Tester", "handle": "tester"},
      "notes": {"dir": "./content/notes", "maxFileBytes": 16, "useMtime": true}
    }`)
	res, err := CollectNotes(cfg, CollectNotesOptions{})
	if err != nil {
		t.Fatal(err)
	}
	note := res.Notes[0]
	if note.Slug != "capped" || note.Date == nil || *note.Date != "2026-07-08T09:10:11Z" {
		t.Errorf("note = %+v", note)
	}
	// Over the cap means no content, so no H1 either: the title falls back to the filename.
	if note.Title != "Capped" {
		t.Errorf("title = %q", note.Title)
	}
	if !note.Files[0].TooLarge || note.Files[0].Stored || len(res.Blobs) != 0 {
		t.Errorf("an over-cap file was stored: %+v", note.Files[0])
	}
}

// The store is shared with repo files, whose keys are git object ids. A note file whose raw
// bytes ARE a `blob <len>\0…` pre-image would, under a bare sha1, take the repo file's key —
// and notes are merged last, so it would overwrite that repo's content.
func TestCollectNotesKeysAreDomainSeparated(t *testing.T) {
	inner := "0.1.0\n"
	payload := append([]byte(fmt.Sprintf("blob %d\x00", len(inner))), inner...)
	dir := makeNotesDir(t, map[string][]byte{"payload.bin": payload})
	res := collectFrom(t, dir, CollectNotesOptions{})
	file := res.Notes[0].Files[0]
	if file.Sha != noteShaOf(payload) {
		t.Errorf("sha = %s, want the note-domain hash", file.Sha)
	}
	if file.Sha == gitShaOf([]byte(inner)) {
		t.Fatal("a note key collided with a git object id")
	}
	for _, src := range []string{"plain\n", "", strings.Repeat("x", 64)} {
		if noteShaOf([]byte(src)) == gitShaOf([]byte(src)) {
			t.Errorf("note and git hashes agree for %q", src)
		}
	}
}

func TestCollectNotesUnservableFiles(t *testing.T) {
	dir := makeNotesDir(t, textFiles(map[string]string{
		"awkward/index.md":    "# Awkward\n",
		"awkward/c#-tips.md":  "# sharp\n",
		"awkward/50% off.txt": "fifty\n",
		"awkward/read me.txt": "fine\n",
	}))
	res := collectFrom(t, dir, CollectNotesOptions{})
	note := res.Notes[0]
	want := []string{"50% off.txt", "c#-tips.md", "index.md", "read me.txt"}
	if !reflect.DeepEqual(notePaths(note), want) {
		t.Errorf("paths = %v, want %v — every file is still collected", notePaths(note), want)
	}
	for _, f := range note.Files {
		if !f.Stored {
			t.Errorf("%s was not stored; only the raw ROUTE is withheld", f.Path)
		}
	}
	if len(res.Blobs) != 4 {
		t.Errorf("blobs = %d, want 4", len(res.Blobs))
	}
	if got := warningCodes(res.Warnings); !reflect.DeepEqual(got, []string{"note-file-unservable", "note-file-unservable"}) {
		t.Fatalf("warnings = %v", got)
	}
	if !strings.Contains(strings.Join([]string{res.Warnings[0].Message, res.Warnings[1].Message}, " "), "50% off.txt") {
		t.Error("the warning does not name the file it is about")
	}
}

/* ---- text decoding -------------------------------------------------------- */

// Node replaces each maximal subpart of an ill-formed UTF-8 sequence with one U+FFFD. Go would
// pass the bytes through, and encoding/json would then escape them one per byte — a different
// title, and a different forge.json, from the same note. Expectations checked against
// `Buffer.from(bytes).toString('utf8')`.
func TestDecodeUTF8Lossy(t *testing.T) {
	replacement := "�"
	for _, tc := range []struct {
		name string
		in   []byte
		want string
	}{
		{"valid ascii", []byte("hello"), "hello"},
		{"valid multibyte", []byte("café"), "café"},
		{"latin-1 e-acute", []byte{'c', 'a', 'f', 0xe9}, "caf" + replacement},
		{"lone continuation", []byte{0x80}, replacement},
		{"truncated 3-byte", []byte{0xe2, 0x82}, replacement},
		{"overlong lead", []byte{0xc0, 0x80}, replacement + replacement},
		{"surrogate encoding", []byte{0xed, 0xa0, 0x80}, replacement + replacement + replacement},
		{"f5 is never a lead", []byte{0xf5, 0x80, 0x80, 0x80}, strings.Repeat(replacement, 4)},
		{"recovers after a bad byte", []byte{0xff, 'o', 'k'}, replacement + "ok"},
	} {
		if got := decodeUTF8Lossy(tc.in); got != tc.want {
			t.Errorf("%s: decodeUTF8Lossy(% x) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

func noteStrPtr(s string) *string { return &s }
