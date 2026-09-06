package build

import (
	"fmt"
	"os"
	"path/filepath"

	"frznforge/internal/model"
	"frznforge/internal/render"
)

// The families that move bytes rather than build markup: the 404 page, source archives, hosted
// site files, and the search index. Grouped together because they share a shape — each is a
// short, mechanical mapping from the artifact to a file — and none of them needs a page
// template beyond the shell.

// emitNotFound writes the 404 page. It is `/404` with no trailing slash, so FilePath gives it
// `404.html` — the name every static host looks for.
func emitNotFound(b *Builder) error {
	return b.WritePage(b.Router.NotFoundURL(), "page-404", render.Page{Title: "Not found"})
}

// emitRepoArchives copies the zips ingest produced with `git archive`.
//
// Copied, never regenerated. The bytes already exist and are already deterministic; building a
// second zip in Go would be a differently-deterministic archive for no gain, and would put this
// build in the business of reproducing git's archive format.
func emitRepoArchives(b *Builder, repo *model.Repo) error {
	for _, a := range repo.Archives {
		src := filepath.Join(b.Cfg.OutDir, filepath.FromSlash(a.File))
		content, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("archive %s: %w (re-run `frznforge ingest`)", a.File, err)
		}
		if int64(len(content)) != a.Bytes {
			return fmt.Errorf("archive %s: artifact says %d bytes, file has %d — the artifact and the store disagree",
				a.File, a.Bytes, len(content))
		}
		if err := b.WriteFile(b.Router.ArchiveURL(repo.Slug, a.Ref), content); err != nil {
			return err
		}
	}
	return nil
}

// emitHosted writes every file of every hosted static site (schema v7).
//
// Each lands at its LITERAL path under /<slug>/, so `index.html` ends up at
// `<slug>/index.html` and `/<slug>/` resolves through the same directory-index handling every
// static host already does.
func emitHosted(b *Builder) error {
	for _, f := range b.Router.HostedFiles(b.Data) {
		content, err := b.Blob(f.Sha)
		if err != nil {
			return fmt.Errorf("hosted %s/%s: %w", f.Site.Slug, f.Path, err)
		}
		if err := b.WriteFile(f.URL, content); err != nil {
			return err
		}
	}
	return nil
}
