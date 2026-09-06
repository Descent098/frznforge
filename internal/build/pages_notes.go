package build

import (
	"fmt"
	"html/template"
	"regexp"
	"strings"

	"frznforge/internal/frontmatter"
	"frznforge/internal/highlight"
	"frznforge/internal/markdown"
	"frznforge/internal/model"
	"frznforge/internal/render"
	"frznforge/internal/routes"
)

// The notes family: /notes/, /notes/<slug>/, and the raw bytes of every stored note file.
// Port of src/pages/notes/index.astro, src/pages/notes/[slug]/index.astro,
// src/components/NoteFileView.astro and src/pages/notes/[slug]/raw/[...path].ts.
//
// # Trust
//
// Note markdown renders TRUSTED. A note is a file the site owner put in notes.dir on their own
// disk; nothing here ever came off somebody else's forge, which is exactly the condition
// markdown.IsTrustedSource checks for a repo. Raw HTML in a note is the author's own HTML.
//
// # The one structural difference from the repo blob viewer
//
// A note page stacks several file cards, so the Preview/Source toggle cannot key off the fixed
// ids the blob page uses (#hf-md-preview / #hf-md-source). Every card gets its own radio group
// named hf-nv-<n>, and web/css/notes.css does the hiding off the hf-nv-* CLASSES instead — the
// inputs must therefore stay leading siblings of .hf-blob-head and of both body divs, or the
// `~` rules in that stylesheet stop matching and both views render at once.

const (
	// Same ceilings as the repo blob viewer: past either of these, show plain text rather than
	// pay to highlight a file nobody reads top to bottom.
	noteHighlightMaxBytes = 500 * 1024
	noteHighlightMaxLines = 5000
	// noteIndexFilesShown is how many file names a card names before "+N more".
	noteIndexFilesShown = 4
)

// noteImageExt is the set the file view renders as a picture rather than as source. SVG is in
// it deliberately: a note is the owner's own file, so there is no untrusted-markup problem.
var noteImageExt = regexp.MustCompile(`(?i)\.(png|jpe?g|gif|webp|svg|ico)$`)

// noteAnchorUnsafe is every run of characters an id should not carry. Case-insensitive in the
// Astro original (`/[^a-z0-9]+/gi`), which means letters of either case survive.
var noteAnchorUnsafe = regexp.MustCompile(`[^A-Za-z0-9]+`)

/* ---- index --------------------------------------------------------------- */

// noteCard is one row of /notes/.
type noteCard struct {
	Note model.Note
	// Heat drives both the card's rail (heat-*) and the date's colour (t-*), from one bucket,
	// so the two can never disagree.
	Heat string
	// Shown are the first noteIndexFilesShown files; Hidden is how many were left out.
	Shown  []model.NoteFile
	Hidden int
}

type notesIndexPayload struct {
	Cards []noteCard
}

/* ---- one note ------------------------------------------------------------ */

// noteFileView is one file card. Everything the template branches on is decided here, in Go,
// because the decision is a chain of five conditions on the artifact and the blob — not
// presentation.
type noteFileView struct {
	File model.NoteFile
	// Anchor is this card's fragment id; the table of contents links to it and the highlighted
	// line ids are namespaced with it.
	Anchor string
	// Group discriminates this card's radio group. Two cards sharing a name would toggle each
	// other.
	Group     string
	PreviewID string
	SourceID  string

	// Raw is the raw-bytes URL, or "" for a file that has no route: unstored, or a name no
	// static URL can round-trip. Such a file still renders inline — it just has nothing to
	// hang Raw / Download / an <img> off.
	Raw string

	// Mode picks the body: md-toggle, md-only, image, code, plain or fallback.
	Mode string

	// HasLines is false for a file whose bytes were never read, where "0 lines" would be a lie.
	HasLines  bool
	LineCount int64

	Text      string
	CodeHTML  template.HTML
	MdPreview template.HTML
	MdSource  template.HTML

	// MaxLines and MaxBytes are echoed in the "highlighting skipped" note.
	MaxLines int64
	MaxBytes int64
}

type notePayload struct {
	Note  model.Note
	Heat  string
	Files []noteFileView
}

/* ---- emitters ------------------------------------------------------------ */

// emitNotes writes the index, one page per note, and the raw file routes.
func emitNotes(b *Builder) error {
	if err := emitNotesIndex(b); err != nil {
		return err
	}
	for _, note := range b.Data.Notes {
		if err := emitNote(b, note); err != nil {
			return fmt.Errorf("note %s: %w", note.Slug, err)
		}
	}
	return nil
}

// emitNotesIndex writes /notes/.
//
// A plain static list, not an island: notes arrive pre-sorted newest-first and the command
// palette already indexes their titles and tags. The page exists even with no notes — it is
// unconditional in routes.NotesRoutes — so the empty state has to say where notes come from.
func emitNotesIndex(b *Builder) error {
	cards := make([]noteCard, 0, len(b.Data.Notes))
	for _, note := range b.Data.Notes {
		shown := note.Files
		if len(shown) > noteIndexFilesShown {
			shown = shown[:noteIndexFilesShown]
		}
		cards = append(cards, noteCard{
			Note:   note,
			Heat:   render.HeatFor(deref(note.Date), b.Site.Now, b.Cfg.Theme.Heat),
			Shown:  shown,
			Hidden: len(note.Files) - len(shown),
		})
	}
	page := render.Page{
		Title: "Notes",
		Description: fmt.Sprintf("%d %s — snippets, cheatsheets and single-file writing.",
			len(b.Data.Notes), pluralise(len(b.Data.Notes), "note", "notes")),
		Active:      "notes",
		Payload:     notesIndexPayload{Cards: cards},
		ExtraStyles: []string{b.Router.WithBase("/css/notes.css")},
	}
	return b.WritePage(b.Router.NotesIndexURL(), "page-notes", page)
}

// emitNote writes /notes/<slug>/ and the raw route of each of its stored files.
func emitNote(b *Builder, note model.Note) error {
	anchors := noteAnchors(note)
	files := make([]noteFileView, 0, len(note.Files))
	mermaid := false
	for i, file := range note.Files {
		view, err := buildNoteFileView(b, note, file, anchors[i], i+1)
		if err != nil {
			return err
		}
		if markdown.ContainsMermaid(string(view.MdPreview)) {
			mermaid = true
		}
		files = append(files, view)
	}

	// Every card prints a Copy path button, so the copy wiring is unconditional.
	scripts := []string{b.Router.WithBase("/js/copy.js")}
	if mermaid {
		scripts = append(scripts, b.Router.WithBase("/js/mermaid.js"))
	}
	page := render.Page{
		Title:       note.Title,
		Description: deref(note.Description),
		Active:      "notes",
		Payload: notePayload{
			Note:  note,
			Heat:  render.HeatFor(deref(note.Date), b.Site.Now, b.Cfg.Theme.Heat),
			Files: files,
		},
		ExtraStyles:  []string{b.Router.WithBase("/css/notes.css")},
		ExtraScripts: scripts,
	}
	if err := b.WritePage(b.Router.NoteURL(note.Slug), "page-note", page); err != nil {
		return err
	}
	return emitNoteRawFiles(b, note)
}

// emitNoteRawFiles writes the exact bytes of every stored note file.
//
// Only STORED files get a route, and only paths routes.IsRawServable accepts: a name holding
// '#' or '%' cannot survive the URL → filename round trip, and ingest has already raised
// note-file-unservable for it. The two conditions are the same pair routes.NotesRoutes
// enumerates, so the sync test and the build cannot disagree about what exists.
func emitNoteRawFiles(b *Builder, note model.Note) error {
	for _, file := range note.Files {
		if !file.Stored || !routes.IsRawServable(file.Path) {
			continue
		}
		content, err := b.Blob(file.Sha)
		if err != nil {
			return fmt.Errorf("raw %s: %w", file.Path, err)
		}
		if err := b.WriteFile(b.Router.NoteRawURL(note.Slug, file.Path), content); err != nil {
			return err
		}
	}
	return nil
}

/* ---- one file ------------------------------------------------------------ */

// buildNoteFileView decides how one file renders and pre-renders whatever it needs.
func buildNoteFileView(b *Builder, note model.Note, file model.NoteFile, anchor string, index int) (noteFileView, error) {
	group := fmt.Sprintf("%d", index)
	view := noteFileView{
		File:      file,
		Anchor:    anchor,
		Group:     group,
		PreviewID: "hf-nv-" + group + "-preview",
		SourceID:  "hf-nv-" + group + "-source",
		MaxLines:  noteHighlightMaxLines,
		MaxBytes:  noteHighlightMaxBytes,
	}
	if file.Stored && routes.IsRawServable(file.Path) {
		view.Raw = b.Router.NoteRawURL(note.Slug, file.Path)
	}
	showImage := view.Raw != "" && noteImageExt.MatchString(file.Path)
	isMarkdown := file.Markdown && !file.Binary && file.Stored && !showImage

	hasText := file.Stored && !file.Binary
	var text string
	if hasText {
		content, err := b.Blob(file.Sha)
		if err != nil {
			return view, fmt.Errorf("file %s: %w", file.Path, err)
		}
		text = string(content)
		view.Text = text
		view.HasLines = true
		view.LineCount = int64(highlight.CountLines(text))
	}
	tooBig := hasText && (file.Size > noteHighlightMaxBytes || view.LineCount > noteHighlightMaxLines)

	// The line ids are namespaced by the section anchor. Without it every card on a folder note
	// would emit id="L1"…, so #L5 would resolve to the first card and every later card's lines
	// would be permanently unlinkable.
	idPrefix := anchor + "-"
	switch {
	case isMarkdown && hasText:
		view.MdPreview = htmlOf(b.Markdown(notePreviewSource(text, note.Title), true))
		if !tooBig {
			// 'Markdown' rather than file.language: the source view of a .md file is markdown
			// whatever the language map called it.
			view.MdSource = htmlOf(highlight.Highlight(text, "Markdown", file.Path, idPrefix))
		}
	case !isMarkdown && !showImage && hasText && !tooBig:
		view.CodeHTML = htmlOf(highlight.Highlight(text, deref(file.Language), file.Path, idPrefix))
	}

	switch {
	case isMarkdown && view.MdSource != "":
		view.Mode = "md-toggle"
	case isMarkdown:
		// Markdown too large to highlight: the preview still renders, there is just nothing to
		// toggle to, so no radios are emitted and no toggle is offered.
		view.Mode = "md-only"
	case showImage:
		view.Mode = "image"
	case view.CodeHTML != "":
		view.Mode = "code"
	case hasText:
		view.Mode = "plain"
	default:
		view.Mode = "fallback"
	}
	return view, nil
}

// notePreviewSource is the markdown the preview renders: no frontmatter, and no opening
// heading that is only the page's own title repeating itself.
//
// Both halves come from internal/frontmatter, the package ingest parsed the note with, rather
// than from regexes of this file's own. Note frontmatter is metadata — the artifact already
// carries it on the Note — and left in place, a `---`-terminated block renders as a setext
// heading with `title: …` underneath it.
//
// The SOURCE view is deliberately untouched: its whole point is to show the file exactly as it
// is on disk, which is also what the raw route serves.
func notePreviewSource(src, noteTitle string) string {
	return frontmatter.StripLeadingHeading(frontmatter.SplitFile(src).Body, noteTitle)
}

// noteAnchors assigns every file of a note its fragment id.
//
// Derived from the path so the anchor is readable and stable across rebuilds; a positional
// suffix is appended only when two paths would collapse onto the same id (`a/b.md` and
// `a-b.md`), which keeps every id unique either way.
func noteAnchors(note model.Note) []string {
	seen := make(map[string]bool, len(note.Files))
	out := make([]string, 0, len(note.Files))
	for i, file := range note.Files {
		base := "f-" + strings.ToLower(strings.Trim(noteAnchorUnsafe.ReplaceAllString(file.Path, "-"), "-"))
		id := base
		if seen[base] {
			id = fmt.Sprintf("%s-%d", base, i+1)
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}
