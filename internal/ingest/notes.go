package ingest

// Notes (schema v4): turn the plain folder at notes.dir into model.Note values plus blobs for
// the shared content-addressed store — the port of src/lib/ingest/notes.ts.
//
// This is the one part of ingest that reads the filesystem directly instead of going through
// git plumbing, and that is correct: notes.dir is not a repository, so there is no committed
// tree to read and no working copy to avoid. The "ingest never reads the working tree" rule is
// about git repos.
//
// Everything here is ordered explicitly. Directory entries are sorted before they are walked
// (readdir order is filesystem-dependent), paths are built with the OS separator and only
// converted to forward slashes for the artifact, and frontmatter goes through
// internal/frontmatter, which is shared with the note viewer so both halves agree on where a
// block ends.

import (
	"crypto/sha1"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"frznforge/internal/config"
	"frznforge/internal/frontmatter"
	"frznforge/internal/markdown"
	"frznforge/internal/model"
	"frznforge/internal/routes"
)

/* ---- public surface ------------------------------------------------------- */

// CollectNotesOptions overrides what CollectNotes would otherwise take from the config. The
// zero value means "use the config for everything", which is what ingest passes.
type CollectNotesOptions struct {
	// Dir is the absolute notes folder. Empty means config.NotesDir.
	//
	// Naming one also opts into the notes-dir-missing warning, exactly as declaring a `notes`
	// block in the config does — see CollectNotes for why that is not unconditional.
	Dir string
	// MaxFileBytes is the cap above which a file is tooLarge and not stored. nil means
	// notes.maxFileBytes, else ingest.maxBlobBytes.
	MaxFileBytes *int64
	// UseMtime falls back to filesystem modification times for an undated note. nil means
	// notes.useMtime.
	UseMtime *bool
}

// CollectNotesResult is what CollectNotes hands back to the assembly.
type CollectNotesResult struct {
	// Notes are already in artifact order (model.CompareNotes) and have unique slugs.
	Notes []model.Note
	// Blobs is content for every NoteFile with Stored set, keyed by NoteFile.Sha. The assembly
	// merges this into the same map it persists to <outDir>/blobs/, so nothing extra is written
	// here.
	Blobs map[string][]byte
	// Warnings are site-level (Repo nil), emitted in directory walk order.
	Warnings []model.Warning
}

/* ---- title / date derivation ---------------------------------------------- */

// stripNoteExtension drops the last extension: `check-determinism.ps1` → `check-determinism`.
// A leading dot is not an extension, so `.env` survives whole.
func stripNoteExtension(name string) string {
	if dot := strings.LastIndex(name, "."); dot > 0 {
		return name[:dot]
	}
	return name
}

var noteSeparatorRun = regexp.MustCompile(`[-_.]+`)

// HumaniseName turns a file or folder name into a display title: separators become spaces and
// the first letter is upper-cased, leaving existing capitalisation alone
// (`static-host-configs` → "Static host configs", `XDG_dirs` → "XDG dirs").
func HumaniseName(name string) string {
	words := jsTrim(collapseJSWhitespace(noteSeparatorRun.ReplaceAllString(stripNoteExtension(name), " ")))
	if words == "" {
		return ""
	}
	return jsUpperFirst(words)
}

// collapseJSWhitespace is `.replace(/\s+/g, ' ')`. It cannot be a Go regexp: Go's `\s` is the
// five ASCII spaces, while JavaScript's also covers NBSP, the U+2000 block, the line/paragraph
// separators and the BOM — all of which are legal in a filename.
func collapseJSWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inRun := false
	for _, r := range s {
		if isJSWhitespace(r) {
			if !inRun {
				b.WriteByte(' ')
				inRun = true
			}
			continue
		}
		inRun = false
		b.WriteRune(r)
	}
	return b.String()
}

// jsUpperFirst upper-cases the first UTF-16 code unit the way `words[0].toUpperCase()` does.
//
// Go's strings.ToUpper is Unicode SIMPLE case mapping; JavaScript's toUpperCase is the FULL
// mapping, so 'ß' becomes "SS" there and stays 'ß' here. jsUpperCaseSpecials carries every
// BMP code point where the two disagree, so a note called `ßeta-notes.md` gets the same title
// from both implementations.
func jsUpperFirst(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError && size <= 1 {
		return s // not valid UTF-8; leave it exactly as it came off disk
	}
	if r > 0xffff {
		// JavaScript sees the high surrogate half here, and a lone surrogate upper-cases to
		// itself — so the whole string is unchanged.
		return s
	}
	if special, ok := jsUpperCaseSpecials[r]; ok {
		return special + s[size:]
	}
	return strings.ToUpper(string(r)) + s[size:]
}

var nonSlugRunNote = regexp.MustCompile(`[^a-z0-9]+`)

// NoteSlug turns a file or folder name into a URL slug, with the same normalisation Slugify
// applies to repos. Not a call to Slugify because its empty-input fallback is "repo", which
// would be a lie on a note page.
func NoteSlug(name string) string {
	slug := nonSlugRunNote.ReplaceAllString(strings.ToLower(stripNoteExtension(name)), "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		return "note"
	}
	return slug
}

var (
	// dateOnlyRe is `YYYY-MM-DD`, the whole value.
	dateOnlyRe = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})$`)
	// dateTimeRe is `YYYY-MM-DD` + a time, separated by `T` or a space, with an OPTIONAL
	// `Z`/`±HH:MM` zone. Fractional seconds are accepted and then dropped, like every other
	// date in the artifact.
	dateTimeRe = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})[T ](\d{2}):(\d{2})(?::(\d{2})(?:\.(\d+))?)?(Z|[+-]\d{2}:\d{2})?$`)
)

// NormaliseNoteDate turns a frontmatter `date` into an artifact date, or nil when it is not a
// date the artifact can carry.
//
// Only two shapes are accepted, and both read the same on every machine:
//
//   - `YYYY-MM-DD` → midnight UTC;
//   - `YYYY-MM-DD` + a time, with or without a zone → that instant, seconds precision, UTC.
//     **A missing zone means UTC, not the build machine's zone.** This is the determinism rule
//     doing real work: `Date.parse('2026-03-04 10:00:00')` is local time per ECMA-262, so
//     deferring to it made the same frontmatter emit `…T17:00:00Z` in Edmonton and
//     `…T10:00:00Z` in CI — a different Note.Date, a different note order, a different
//     forge.json from identical inputs.
//
// Everything else — `March 4, 2026`, `03/04/2026`, an RFC 2822 string — is dropped, because
// `Date.parse` handles those implementation-defined and zone-dependently. A value that is not
// a date is dropped silently: a mistyped date must not fail a build, and the note simply
// renders as undated.
func NormaliseNoteDate(value string) *string {
	t := jsTrim(value)
	if t == "" {
		return nil
	}

	if m := dateOnlyRe.FindStringSubmatch(t); m != nil {
		y, mo, d := parseDigits(m[1]), parseDigits(m[2]), parseDigits(m[3])
		// The TypeScript checks this calendar by round-tripping through `Date.UTC`, which
		// silently rolls "2026-02-31" over into March — and which also maps a year of 0–99 onto
		// 1900+y. Both quirks are reproduced: the round-trip below compares against the digits
		// AS WRITTEN, so "0026-03-04" fails it and is dropped exactly as it is there.
		calendarYear := y
		if y >= 0 && y <= 99 {
			calendarYear = 1900 + y
		}
		p := time.Date(calendarYear, time.Month(mo), d, 0, 0, 0, 0, time.UTC)
		if p.Year() != y || int(p.Month()) != mo || p.Day() != d {
			return nil
		}
		out := t + "T00:00:00Z"
		return &out
	}

	m := dateTimeRe.FindStringSubmatch(t)
	if m == nil {
		return nil
	}
	y, mo, d := parseDigits(m[1]), parseDigits(m[2]), parseDigits(m[3])
	hh, mm := parseDigits(m[4]), parseDigits(m[5])
	ss := 0
	if m[6] != "" {
		ss = parseDigits(m[6])
	}
	// The ranges the ECMA-262 Date Time String Format accepts. Note what is NOT checked: the
	// day is only range-checked against 31, so "2026-02-31T00:00:00Z" is a valid string that
	// ROLLS OVER to 3 March — which is what Date.parse does, and what time.Date does below.
	if mo < 1 || mo > 12 || d < 1 || d > 31 || hh > 24 || mm > 59 || ss > 59 {
		return nil
	}
	// Hour 24 is midnight ending the day, and only when nothing else is set.
	if hh == 24 && (mm != 0 || ss != 0 || strings.Trim(m[7], "0") != "") {
		return nil
	}

	offset := time.Duration(0)
	if zone := m[8]; zone != "" && zone != "Z" {
		oh, om := parseDigits(zone[1:3]), parseDigits(zone[4:6])
		if oh > 23 || om > 59 {
			return nil
		}
		offset = time.Duration(oh)*time.Hour + time.Duration(om)*time.Minute
		if zone[0] == '-' {
			offset = -offset
		}
	}

	// Fractional seconds are matched only so the hour-24 rule above can see them; the artifact
	// carries seconds precision, so they are truncated here rather than rounded — which is what
	// stripping the milliseconds off `toISOString()` amounts to.
	out := time.Date(y, time.Month(mo), d, hh, mm, ss, 0, time.UTC).Add(-offset).Format(noteDateLayout)
	return &out
}

// noteDateLayout is the artifact's date format: UTC, seconds precision, literal Z.
const noteDateLayout = "2006-01-02T15:04:05Z"

// parseDigits is strconv.Atoi for strings a regexp has already proved are digits.
func parseDigits(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

/* ---- ordering ------------------------------------------------------------- */

// compareUTF16 orders two strings by UTF-16 code unit, which is what a plain `a < b` does in
// JavaScript.
//
// Go's `<` compares UTF-8 bytes, and the two disagree for astral characters: in UTF-16 they
// are surrogate pairs (U+D800–U+DFFF), so they sort BEFORE anything in U+E000–U+FFFF, while in
// UTF-8 they sort after. A notes folder holding both an emoji-named entry and a fullwidth-Latin
// one would otherwise be walked in a different order here than there — and walk order decides
// which note keeps the bare slug.
func compareUTF16(a, b string) int {
	if isASCIIOnly(a) && isASCIIOnly(b) {
		return strings.Compare(a, b)
	}
	au, bu := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(au) && i < len(bu); i++ {
		if au[i] != bu[i] {
			if au[i] < bu[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(au) < len(bu):
		return -1
	case len(au) > len(bu):
		return 1
	}
	return 0
}

func isASCIIOnly(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

/* ---- filesystem walk ------------------------------------------------------ */

// noteBinarySniffBytes is how much of an oversized file is inspected for the binary heuristic —
// git's own window.
const noteBinarySniffBytes = 8000

// isIgnoredNoteEntry — names starting with '.' or '_' are private to the author, never published.
func isIgnoredNoteEntry(name string) bool {
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

// sortedNoteEntries lists dir with ignored names removed, in UTF-16 name order. os.ReadDir already
// sorts, but by Go's byte order — see compareUTF16.
func sortedNoteEntries(dir string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	kept := entries[:0]
	for _, e := range entries {
		if !isIgnoredNoteEntry(e.Name()) {
			kept = append(kept, e)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return compareUTF16(kept[i].Name(), kept[j].Name()) < 0 })
	return kept, nil
}

// foundNoteFile is one file under a note root, before it is hashed.
type foundNoteFile struct {
	// abs is the path on disk.
	abs string
	// rel is the path relative to the note root, forward slashes — the artifact's NoteFile.Path.
	rel string
}

// walkNoteFiles lists every regular file under root, recursively, in sorted walk order.
//
// Symlinks and other non-regular entries are skipped: following them would let a note escape
// notes.dir (or loop forever), and the site has nothing to render for a device node.
func walkNoteFiles(root, dir string) ([]foundNoteFile, error) {
	entries, err := sortedNoteEntries(dir)
	if err != nil {
		return nil, err
	}
	var out []foundNoteFile
	for _, entry := range entries {
		abs := filepath.Join(dir, entry.Name())
		switch {
		case entry.IsDir():
			nested, err := walkNoteFiles(root, abs)
			if err != nil {
				return nil, err
			}
			out = append(out, nested...)
		case entry.Type().IsRegular():
			rel, err := filepath.Rel(root, abs)
			if err != nil {
				return nil, fmt.Errorf("note file %s is not under its note root %s: %w", abs, root, err)
			}
			out = append(out, foundNoteFile{abs: abs, rel: filepath.ToSlash(rel)})
		}
	}
	return out, nil
}

// scannedNoteFile is a hashed note file plus what note assembly needs beyond the artifact fields.
type scannedNoteFile struct {
	file model.NoteFile
	// content is the raw bytes, or nil when the file is over the cap (then it is not stored).
	content []byte
	// mtimeMillis is only consulted when notes.useMtime is on.
	mtimeMillis int64
}

// noteHashDomain separates note blob keys from git object ids.
//
// Notes and repo files share ONE content-addressed store, and repo keys are git object shas
// (`sha1('blob <len>\0' + bytes)`). Hashing note bytes bare would put both in the same
// namespace over different pre-images, so a note file whose raw bytes happen to BE
// `blob <len>\0<content>` would hash to the git sha of `<content>` and — notes are merged last
// — overwrite that repo file's stored bytes. A distinct domain makes the two namespaces
// provably disjoint.
const noteHashDomain = "note"

// scanNoteFile hashes and classifies one file exactly as ScanTree classifies a repo blob: same
// binary heuristic, same "within the cap ⇒ stored, binary included" rule, same language map.
// Only the hash differs — see noteHashDomain.
//
// Oversized files are streamed so a huge note file cannot blow up the build's memory: the sha
// still covers every byte, and only the sniff window is kept. The header needs the length up
// front, which for the streamed path comes from the stat; a file that changes size mid-build
// would produce a header disagreeing with its own content, so the streamed length is verified
// and the file dropped if it moved.
//
// ok is false for a file that vanished, could not be read, or moved under us. That is
// deliberate rather than fatal: one unreadable file must not fail a build, and it simply does
// not appear in the note.
func scanNoteFile(found foundNoteFile, maxFileBytes int64) (scannedNoteFile, bool) {
	info, err := os.Stat(found.abs)
	if err != nil {
		return scannedNoteFile{}, false // vanished or unreadable between the walk and here
	}

	name := found.rel[strings.LastIndex(found.rel, "/")+1:]
	hash := sha1.New()
	var content []byte
	var size int64
	var binary bool

	if info.Size() <= maxFileBytes {
		content, err = os.ReadFile(found.abs)
		if err != nil {
			return scannedNoteFile{}, false
		}
		size = int64(len(content))
		fmt.Fprintf(hash, "%s %d\x00", noteHashDomain, size)
		hash.Write(content)
		binary = LooksBinary(content)
	} else {
		f, err := os.Open(found.abs)
		if err != nil {
			return scannedNoteFile{}, false
		}
		defer f.Close()
		fmt.Fprintf(hash, "%s %d\x00", noteHashDomain, info.Size())
		prefix := make([]byte, 0, noteBinarySniffBytes)
		buf := make([]byte, 64*1024)
		for {
			n, readErr := f.Read(buf)
			if n > 0 {
				hash.Write(buf[:n])
				size += int64(n)
				if len(prefix) < noteBinarySniffBytes {
					prefix = append(prefix, buf[:n]...)
				}
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				return scannedNoteFile{}, false
			}
		}
		if size != info.Size() {
			return scannedNoteFile{}, false // the file changed under us; its sha would be a lie
		}
		binary = LooksBinary(prefix)
	}

	tooLarge := content == nil
	return scannedNoteFile{
		file: model.NoteFile{
			Name:     name,
			Path:     found.rel,
			Sha:      fmt.Sprintf("%x", hash.Sum(nil)),
			Size:     size,
			Binary:   binary,
			TooLarge: tooLarge,
			Stored:   !tooLarge,
			Language: DetectLanguage(found.rel),
			Markdown: markdown.IsMarkdownPath(found.rel),
		},
		content:     content,
		mtimeMillis: info.ModTime().UnixMilli(),
	}, true
}

/* ---- note assembly -------------------------------------------------------- */

// noteFrontmatterFiles is where a folder note's frontmatter is read from, in priority order.
var noteFrontmatterFiles = []string{"index.md", "index.markdown", "readme.md", "readme.markdown"}

// noteFrontmatterFileFor picks the file a folder note takes its frontmatter from: index.md, else
// README.md, at the note's root.
func noteFrontmatterFileFor(files []scannedNoteFile) *scannedNoteFile {
	for _, candidate := range noteFrontmatterFiles {
		for i := range files {
			if strings.ToLower(files[i].file.Path) == candidate {
				return &files[i]
			}
		}
	}
	return nil
}

// noteTextOf is the text of a scanned file, or ok=false when it is binary or was too large to keep.
func noteTextOf(scanned *scannedNoteFile) (string, bool) {
	if scanned == nil || scanned.content == nil || scanned.file.Binary {
		return "", false
	}
	return decodeUTF8Lossy(scanned.content), true
}

// noteBodyOf is the prose of a scanned markdown file — its text with any frontmatter removed.
func noteBodyOf(scanned *scannedNoteFile) (string, bool) {
	text, ok := noteTextOf(scanned)
	if !ok {
		return "", false
	}
	return frontmatter.Parse(text).Body, true
}

// buildNote assembles one note from its scanned files. entryName is the file or folder name as
// it appears in notes.dir; slug has already been de-duplicated by the caller.
func buildNote(entryName, slug, kind string, files []scannedNoteFile, useMtime bool) model.Note {
	sorted := make([]scannedNoteFile, len(files))
	copy(sorted, files)
	sort.SliceStable(sorted, func(i, j int) bool {
		return compareUTF16(sorted[i].file.Path, sorted[j].file.Path) < 0
	})

	// Frontmatter: the file itself for a single-file note, index.md/README.md for a folder.
	// Other files in a folder keep their frontmatter as content — the docs are explicit.
	var fmFile *scannedNoteFile
	if kind == "file" {
		if sorted[0].file.Markdown {
			fmFile = &sorted[0]
		}
	} else {
		fmFile = noteFrontmatterFileFor(sorted)
	}
	fm := frontmatter.Frontmatter{Data: map[string]frontmatter.Value{}}
	if text, ok := noteTextOf(fmFile); ok {
		fm = frontmatter.Parse(text)
	}

	// H1 fallback: the frontmatter file when there is one, else the first markdown file by
	// path. Either way the scan runs on PROSE, never on raw file text: a '#' line inside a
	// frontmatter block is a YAML comment, and FirstHeading would promote it to the title.
	headingFile := fmFile
	if headingFile == nil {
		for i := range sorted {
			if sorted[i].file.Markdown {
				headingFile = &sorted[i]
				break
			}
		}
	}
	headingText := fm.Body
	if headingFile != fmFile {
		headingText, _ = noteBodyOf(headingFile)
	}

	title := jsTrim(fm.Data["title"].Str)
	if title == "" {
		title = frontmatter.FirstHeading(headingText)
	}
	if title == "" {
		title = HumaniseName(entryName)
	}
	if title == "" {
		title = slug
	}

	var description *string
	if d := jsTrim(fm.Data["description"].Str); d != "" {
		description = &d
	}

	// Tags accept both a list and a bare scalar, de-duplicated in the order the author wrote
	// them.
	raw := fm.Data["tags"]
	rawTags := raw.List
	if !raw.IsList {
		rawTags = nil
		if raw.Str != "" {
			rawTags = []string{raw.Str}
		}
	}
	tags := []string{}
	for _, tag := range rawTags {
		t := jsTrim(tag)
		if t != "" && !containsString(tags, t) {
			tags = append(tags, t)
		}
	}

	var date *string
	if v, ok := fm.Data["date"]; ok && !v.IsList {
		date = NormaliseNoteDate(v.Str)
	}
	if date == nil && useMtime && len(sorted) > 0 {
		// Newest file in the note, so editing any file in a folder note refreshes its date.
		// Seeded at zero like the TypeScript reduce, which floors a pre-epoch note at 1970.
		newest := int64(0)
		for _, f := range sorted {
			if f.mtimeMillis > newest {
				newest = f.mtimeMillis
			}
		}
		stamp := time.UnixMilli(newest).UTC().Format(noteDateLayout)
		date = &stamp
	}

	noteFiles := make([]model.NoteFile, len(sorted))
	total := int64(0)
	for i, f := range sorted {
		noteFiles[i] = f.file
		total += f.file.Size
	}
	return model.Note{
		Slug:        slug,
		Title:       title,
		Description: description,
		Tags:        tags,
		Date:        date,
		Kind:        kind,
		Files:       noteFiles,
		TotalBytes:  total,
	}
}

/* ---- collection ----------------------------------------------------------- */

// CollectNotes reads notes.dir into notes and blobs.
//
// It never fails for a missing or unreadable folder — that is a notes-dir-missing warning and
// an empty result, because one bad path must not fail a build (the same contract every other
// ingest source follows). The warning is only raised when the folder was actually asked for:
// notes.dir is defaulted, so warning unconditionally would put a permanent "1 ingest warning"
// in the footer of every site that has no notes.
func CollectNotes(cfg *config.Resolved, options CollectNotesOptions) (CollectNotesResult, error) {
	dir := options.Dir
	if dir == "" {
		dir = cfg.NotesDir
	}
	maxFileBytes := cfg.Ingest.MaxBlobBytes
	if cfg.Notes.MaxFileBytes != nil {
		maxFileBytes = *cfg.Notes.MaxFileBytes
	}
	if options.MaxFileBytes != nil {
		maxFileBytes = *options.MaxFileBytes
	}
	useMtime := cfg.Notes.UseMtime
	if options.UseMtime != nil {
		useMtime = *options.UseMtime
	}

	res := CollectNotesResult{Notes: []model.Note{}, Blobs: map[string][]byte{}, Warnings: []model.Warning{}}

	info, err := os.Stat(dir)
	var entries []os.DirEntry
	if err == nil && !info.IsDir() {
		err = fmt.Errorf("%s is not a directory", dir)
	}
	if err == nil {
		entries, err = sortedNoteEntries(dir)
	}
	if err != nil {
		if options.Dir != "" || cfg.NotesConfigured() {
			res.Warnings = append(res.Warnings, model.Warning{
				Code:    "notes-dir-missing",
				Repo:    nil,
				Message: fmt.Sprintf("notes directory %s does not exist or is not readable; no notes were collected", dir),
			})
		}
		return res, nil
	}

	taken := map[string]bool{}
	for _, entry := range entries {
		abs := filepath.Join(dir, entry.Name())
		kind := ""
		switch {
		case entry.Type().IsRegular():
			kind = "file"
		case entry.IsDir():
			kind = "folder"
		default:
			continue // symlinks, sockets, … — see walkNoteFiles
		}

		var scanned []scannedNoteFile
		if kind == "file" {
			one, ok := scanNoteFile(foundNoteFile{abs: abs, rel: entry.Name()}, maxFileBytes)
			if !ok {
				continue
			}
			scanned = []scannedNoteFile{one}
		} else {
			found, err := walkNoteFiles(abs, abs)
			if err != nil {
				return CollectNotesResult{}, err
			}
			for _, f := range found {
				if file, ok := scanNoteFile(f, maxFileBytes); ok {
					scanned = append(scanned, file)
				}
			}
			// "Only files are notes; there is nothing to collect from an empty folder."
			if len(scanned) == 0 {
				continue
			}
		}

		// Slug de-duplication happens in walk order, so which note keeps the bare slug is
		// stable across machines.
		base := NoteSlug(entry.Name())
		slug := base
		for n := 2; taken[slug]; n++ {
			slug = fmt.Sprintf("%s-%d", base, n)
		}
		if slug != base {
			res.Warnings = append(res.Warnings, model.Warning{
				Code:    "note-slug-collision",
				Repo:    nil,
				Message: fmt.Sprintf("note '%s' resolves to slug '%s', already taken; using '%s'", entry.Name(), base, slug),
			})
		}
		taken[slug] = true

		// A note file whose name carries '#' or '%' can be shown but not SERVED — see
		// routes.IsRawServable. Warn once per file, in walk order, so the author learns why the
		// Raw and Download buttons are missing instead of finding a dead link.
		for _, f := range scanned {
			if f.file.Stored && !routes.IsRawServable(f.file.Path) {
				res.Warnings = append(res.Warnings, model.Warning{
					Code: "note-file-unservable",
					Repo: nil,
					Message: fmt.Sprintf(
						"note '%s' file '%s' contains '#' or '%%', which a static raw URL cannot round-trip; "+
							"it renders on the note page but has no raw/download link", slug, f.file.Path),
				})
			}
		}

		for _, f := range scanned {
			if f.file.Stored && f.content != nil {
				res.Blobs[f.file.Sha] = f.content
			}
		}
		res.Notes = append(res.Notes, buildNote(entry.Name(), slug, kind, scanned, useMtime))
	}

	sort.SliceStable(res.Notes, func(i, j int) bool { return model.CompareNotes(res.Notes[i], res.Notes[j]) < 0 })
	return res, nil
}

/* ---- text decoding -------------------------------------------------------- */

// decodeUTF8Lossy decodes bytes the way Node's `buf.toString('utf8')` does: each maximal
// subpart of an ill-formed sequence becomes exactly one U+FFFD.
//
// Go would otherwise pass the bad bytes straight through, and encoding/json replaces them one
// per byte — so a Latin-1 note whose H1 became the note's title would emit a different number
// of escapes in forge.json than the TypeScript does. The subpart rules are Unicode's
// "best practice for U+FFFD substitution" (Table 3-7), which is what the WHATWG decoder V8
// uses implements.
func decodeUTF8Lossy(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var out strings.Builder
	out.Grow(len(b))
	for i := 0; i < len(b); {
		c := b[i]
		if c < 0x80 {
			out.WriteByte(c)
			i++
			continue
		}
		// Continuation bounds for the second byte vary by lead byte; the third and fourth are
		// always 80..BF.
		var want int
		var lo, hi byte = 0x80, 0xbf
		switch {
		case c >= 0xc2 && c <= 0xdf:
			want = 1
		case c == 0xe0:
			want, lo = 2, 0xa0
		case c >= 0xe1 && c <= 0xec, c >= 0xee && c <= 0xef:
			want = 2
		case c == 0xed:
			want, hi = 2, 0x9f
		case c == 0xf0:
			want, lo = 3, 0x90
		case c >= 0xf1 && c <= 0xf3:
			want = 3
		case c == 0xf4:
			want, hi = 3, 0x8f
		default:
			// 0x80–0xC1 and 0xF5–0xFF are never a lead byte: a maximal subpart of one byte.
			out.WriteRune(utf8.RuneError)
			i++
			continue
		}
		n := 1
		ok := true
		for ; n <= want; n++ {
			if i+n >= len(b) {
				ok = false
				break
			}
			cc := b[i+n]
			min, max := lo, hi
			if n > 1 {
				min, max = 0x80, 0xbf
			}
			if cc < min || cc > max {
				ok = false
				break
			}
		}
		if !ok {
			// One replacement for the maximal subpart, resuming at the byte that broke it.
			out.WriteRune(utf8.RuneError)
			i += n
			continue
		}
		out.Write(b[i : i+want+1])
		i += want + 1
	}
	return out.String()
}
