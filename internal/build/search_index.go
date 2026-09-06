package build

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"frznforge/internal/model"
	"frznforge/internal/routes"
)

// /search-index.json — the port of buildSearchIndex in src/lib/search.ts.
//
// Doc order is a pure function of the artifact — pages, then each repo followed by its files in
// artifact order, then notes, then organizations — so the file is byte-stable across builds
// like the artifact itself. Actions are NOT in here: they depend on which page the palette was
// opened from, so web/js/hf-command-palette.js adds them.
//
// # Why three structs instead of one
//
// The TypeScript builds each doc as an object literal, so a doc simply has no `date` key unless
// that kind sets one — and `date` may legitimately be null when it IS set. Go cannot express
// "absent" and "present but null" with one nullable field, and `omitempty` would collapse them.
// The wire format has exactly three shapes, so there are three structs, and the field order in
// each is the emitted key order:
//
//	page / file : kind, title, detail, url
//	repo / note : kind, title, detail, url, keywords, date   (date may be null)
//	org         : kind, title, detail, url, keywords
type plainDoc struct {
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	URL    string `json:"url"`
}

type datedDoc struct {
	Kind     string  `json:"kind"`
	Title    string  `json:"title"`
	Detail   string  `json:"detail"`
	URL      string  `json:"url"`
	Keywords string  `json:"keywords"`
	Date     *string `json:"date"`
}

type keywordDoc struct {
	Kind     string `json:"kind"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	URL      string `json:"url"`
	Keywords string `json:"keywords"`
}

// emitSearchIndex writes the index the command palette fetches on first open.
func emitSearchIndex(b *Builder) error {
	docs := []any{
		plainDoc{Kind: "page", Title: "Overview", Detail: "Profile page", URL: b.Router.HomeURL()},
		plainDoc{Kind: "page", Title: "Repositories", Detail: "All repositories", URL: b.Router.ReposURL()},
	}
	if len(b.Data.Notes) > 0 {
		docs = append(docs, plainDoc{Kind: "page", Title: "Notes", Detail: "All notes", URL: b.Router.NotesIndexURL()})
	}
	if len(b.Data.Organizations) > 0 {
		docs = append(docs, plainDoc{Kind: "page", Title: "Organizations", Detail: "All organizations", URL: b.Router.OrgsIndexURL()})
	}

	for i := range b.Data.Repos {
		repo := &b.Data.Repos[i]
		detail := deref(repo.Description)
		if repo.Description == nil && repo.Empty {
			detail = "Empty repository"
		}
		keywords := append([]string{repo.Slug}, repo.Tags...)
		for _, l := range repo.Languages {
			keywords = append(keywords, l.Name)
		}
		docs = append(docs, datedDoc{
			Kind: "repo", Title: repo.Name, Detail: detail, URL: b.Router.RepoURL(repo.Slug),
			Keywords: strings.Join(keywords, " "), Date: repo.UpdatedAt,
		})
		if repo.DefaultBranch == nil {
			continue
		}
		// Default-branch file paths only. Indexing every browsable ref would multiply the file
		// docs by the ref count for almost no extra reach.
		for _, e := range repo.Tree {
			if e.Type != "blob" && e.Type != "symlink" {
				continue
			}
			if !routes.IsRawServable(e.Path) {
				continue
			}
			docs = append(docs, plainDoc{
				Kind: "file", Title: e.Path, Detail: repo.Slug,
				URL: b.Router.BlobURL(repo.Slug, *repo.DefaultBranch, e.Path),
			})
		}
	}

	// Notes: a file name inside a note must find the note, so every file path is a keyword.
	for _, n := range b.Data.Notes {
		detail := deref(n.Description)
		if n.Description == nil {
			detail = noteFallbackDetail(n)
		}
		keywords := append([]string{n.Slug}, n.Tags...)
		for _, f := range n.Files {
			keywords = append(keywords, f.Path)
		}
		docs = append(docs, datedDoc{
			Kind: "note", Title: n.Title, Detail: detail, URL: b.Router.NoteURL(n.Slug),
			Keywords: strings.Join(keywords, " "), Date: n.Date,
		})
	}

	// Organizations: searchable by their own name/description and by any member repo slug.
	for _, o := range b.Data.Organizations {
		detail := deref(o.Description)
		if o.Description == nil {
			detail = orgFallbackDetail(len(o.Repos))
		}
		docs = append(docs, keywordDoc{
			Kind: "org", Title: o.Name, Detail: detail, URL: b.Router.OrgURL(o.Slug),
			Keywords: strings.Join(append([]string{o.Slug}, o.Repos...), " "),
		})
	}

	payload, err := marshalCompact(struct {
		Version int   `json:"version"`
		Docs    []any `json:"docs"`
	}{Version: 1, Docs: docs})
	if err != nil {
		return err
	}
	return b.WriteFile(b.Router.SearchIndexURL(), payload)
}

// marshalCompact matches `JSON.stringify(value)`: no indentation, no trailing newline, and no
// HTML escaping — a repo description containing `<` has to reach the browser as written.
func marshalCompact(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	// Encode appends a newline; JSON.stringify does not.
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// noteFallbackDetail is the secondary line for a note with no description: its single file's
// name, or a file count.
func noteFallbackDetail(n model.Note) string {
	if len(n.Files) == 1 && n.Files[0].Name != "" {
		return n.Files[0].Name
	}
	return fmt.Sprintf("%d %s", len(n.Files), pluralise(len(n.Files), "file", "files"))
}

// orgFallbackDetail is the secondary line for an organization with no description.
func orgFallbackDetail(repoCount int) string {
	return fmt.Sprintf("%d %s", repoCount, pluralise(repoCount, "repository", "repositories"))
}
