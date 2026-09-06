package ingest

// Assembly: the half of src/lib/ingest/index.ts that turns finished scans into the artifact —
// slug collisions, ordering, the site-level warning list — plus writing forge.json and its two
// byte stores to disk.
//
// Nothing here talks to git or the network. It takes scan results in the order the caller
// produced them and is responsible for every ordering decision that reaches the file: repos
// sorted by slug, notes by model.CompareNotes, organizations and hosting by slug, and warnings
// in one fixed sequence (site-level, notes, organizations, contributors, hosting, then per repo
// in slug order). Get one of those wrong and two builds of the same commits produce different
// bytes, which is the whole property the artifact exists to have.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"frznforge/internal/config"
	"frznforge/internal/model"
)

// Artifact layout under ingest.outDir. The site reads all three by these names.
const (
	ArtifactFilename = "forge.json"
	BlobDirname      = "blobs"
	ArchiveDirname   = "archives"
)

// ScannedRepo is one finished scan as the assembly sees it.
type ScannedRepo struct {
	// Result is what ScanRepo returned, skipped results included.
	Result ScanResult
	// Org is `org` from this repo's source config, empty when it declared none.
	//
	// Carried alongside the scan rather than on model.Repo because it is a site-config concern,
	// not a property of the repository — and because organization membership has to be resolved
	// against the FINAL slug, after the collision renaming below.
	Org string
	// RemoteWarnings are the importer's warnings for a remote source, already stamped with the
	// pre-scan slug. They are moved onto the repo so they show on its page and get renamed with
	// it; for a skipped source they go straight to the site-level list.
	RemoteWarnings []model.Warning
}

// AssembleResult is the artifact and the two byte stores that go beside it.
type AssembleResult struct {
	Data model.ForgeData
	// Blobs is keyed by sha (git object ids for repo files, note keys for notes).
	Blobs map[string][]byte
	// Archives is keyed by the outDir-relative path, "archives/" prefix included.
	Archives map[string][]byte
}

// Assemble builds the artifact from finished scans.
//
// The order of `scanned` matters and is the caller's to get right: it decides which repo keeps
// a contested slug and where a skipped source's warnings land in the site-level list. Hand
// results over in config order (that is what the TypeScript pool preserves) rather than in
// completion order.
//
// It MUTATES the repos it is given — renaming a colliding slug, restamping that repo's
// warnings, moving its archive paths, and appending hosting warnings — exactly as the
// TypeScript does. Nothing else in the pipeline reads them afterwards.
func Assemble(cfg *config.Resolved, scanned []ScannedRepo) (AssembleResult, error) {
	siteWarnings := []model.Warning{}
	kept := make([]*ScannedRepo, 0, len(scanned))
	for i := range scanned {
		entry := &scanned[i]
		if entry.Result.Skipped || entry.Result.Repo == nil {
			siteWarnings = append(siteWarnings, entry.RemoteWarnings...)
			if entry.Result.Warning != nil {
				siteWarnings = append(siteWarnings, *entry.Result.Warning)
			}
			continue
		}
		if len(entry.RemoteWarnings) > 0 {
			// Prepended, not appended: an import problem explains everything below it.
			repo := entry.Result.Repo
			merged := make([]model.Warning, 0, len(entry.RemoteWarnings)+len(repo.Warnings))
			merged = append(merged, entry.RemoteWarnings...)
			repo.Warnings = append(merged, repo.Warnings...)
		}
		kept = append(kept, entry)
	}

	// Slug collisions: later entries (caller order) get -2, -3, … The winner is whichever repo
	// reached the assembly first, so this has to run before the sort below or the answer would
	// depend on nothing the user can see.
	taken := map[string]bool{}
	for _, entry := range kept {
		repo := entry.Result.Repo
		if !taken[repo.Slug] {
			taken[repo.Slug] = true
			continue
		}
		base := repo.Slug
		slug := ""
		for n := 2; ; n++ {
			slug = fmt.Sprintf("%s-%d", base, n)
			if !taken[slug] {
				break
			}
		}
		taken[slug] = true
		renamed := slug
		siteWarnings = append(siteWarnings, model.Warning{
			Code: "slug-collision",
			Repo: &renamed,
			Message: fmt.Sprintf("slug '%s' is used by more than one repo (%s); renamed to '%s'",
				base, repo.Source.Label(), slug),
		})
		repo.Slug = slug
		for i := range repo.Warnings {
			stamped := slug
			repo.Warnings[i].Repo = &stamped
		}
		// Archive paths embed the slug — move them under the renamed one, on the artifact
		// records and on the byte carriers alike, or writeArtifact would emit a file at one
		// path and an Archive.File pointing at another.
		move := func(file string) string {
			return strings.Replace(file, ArchiveDirname+"/"+base+"/", ArchiveDirname+"/"+slug+"/", 1)
		}
		for i := range repo.Archives {
			repo.Archives[i].File = move(repo.Archives[i].File)
		}
		for i := range entry.Result.Archives {
			entry.Result.Archives[i].File = move(entry.Result.Archives[i].File)
		}
	}

	sort.SliceStable(kept, func(i, j int) bool {
		return kept[i].Result.Repo.Slug < kept[j].Result.Repo.Slug
	})

	// Notes: a plain folder on disk, not a git repo — see notes.go. Their content goes into the
	// very same content-addressed map that is persisted to blobs/, so the note viewer reads
	// them with the same lookup the file viewer uses.
	noteRes, err := CollectNotes(cfg, CollectNotesOptions{})
	if err != nil {
		return AssembleResult{}, err
	}

	// Organizations: resolved after the collision pass, so membership names the slugs that
	// actually reach the artifact. `kept` is already slug-sorted, which makes the resolver's
	// repo-side iteration order deterministic.
	orgInputs := make([]OrgRepoInput, len(kept))
	for i, entry := range kept {
		orgInputs[i] = OrgRepoInput{Slug: entry.Result.Repo.Slug, Org: entry.Org}
	}
	orgRes := ResolveOrganizations(cfg, orgInputs)

	// Contributors (schema v8): every email that actually appears in this build's history, so a
	// configured entry matching none of them can be reported. Collected after scanning because
	// that is when the merged contributor lists exist.
	seenEmails := map[string]bool{}
	for _, entry := range kept {
		for _, c := range entry.Result.Repo.Contributors {
			seenEmails[strings.ToLower(c.Email)] = true
		}
	}
	contributorWarnings := []model.Warning{}
	for _, entry := range UnmatchedContributors(cfg.Contributors, seenEmails) {
		quoted := make([]string, len(entry.Emails))
		for i, email := range entry.Emails {
			quoted[i] = "'" + email + "'"
		}
		contributorWarnings = append(contributorWarnings, model.Warning{
			Code: "contributor-unknown-email",
			Repo: nil,
			Message: fmt.Sprintf("contributor '%s' lists %s, which no ingested repo has commits from; "+
				"the entry decorates nobody", entry.Name, strings.Join(quoted, ", ")),
		})
	}

	// Hosting (schema v7): resolved against the final slugs, like organizations. Runs before the
	// warning mirror below because it appends repo-scoped warnings to matched repos.
	repos := make([]*model.Repo, len(kept))
	for i, entry := range kept {
		repos[i] = entry.Result.Repo
	}
	hostingRes, err := ResolveHosting(cfg, repos)
	if err != nil {
		return AssembleResult{}, err
	}

	res := AssembleResult{Blobs: map[string][]byte{}, Archives: map[string][]byte{}}

	// Fixed warning order: site-level, then notes, then organizations, then contributors, then
	// hosting, then per repo in slug order.
	warnings := make([]model.Warning, 0,
		len(siteWarnings)+len(noteRes.Warnings)+len(orgRes.Warnings)+len(contributorWarnings)+len(hostingRes.Warnings))
	warnings = append(warnings, siteWarnings...)
	warnings = append(warnings, noteRes.Warnings...)
	warnings = append(warnings, orgRes.Warnings...)
	warnings = append(warnings, contributorWarnings...)
	warnings = append(warnings, hostingRes.Warnings...)
	for _, entry := range kept {
		repo := entry.Result.Repo
		for _, w := range repo.Warnings {
			// Restamped rather than trusted: a repo warning raised before the collision rename
			// still carries the old slug on its own copy.
			slug := repo.Slug
			w.Repo = &slug
			warnings = append(warnings, w)
		}
		for sha, buf := range entry.Result.Blobs {
			res.Blobs[sha] = buf
		}
		for _, a := range entry.Result.Archives {
			res.Archives[a.File] = a.Data
		}
	}
	// Notes last, which is why their keys are domain-separated from git object ids.
	for sha, buf := range noteRes.Blobs {
		res.Blobs[sha] = buf
	}

	repoValues := make([]model.Repo, len(kept))
	for i, entry := range kept {
		repoValues[i] = *entry.Result.Repo
	}
	res.Data = model.ForgeData{
		SchemaVersion: model.SchemaVersion,
		Repos:         repoValues,
		Notes:         noteRes.Notes,
		Organizations: orgRes.Organizations,
		Hosting:       hostingRes.Hosting,
		Warnings:      warnings,
	}
	return res, nil
}

// PreScanSlug is the slug a source is expected to get, worked out before it is scanned.
//
// The assembly needs one early for two reasons: warnings raised before the scan have to carry a
// slug some repo actually has, and `hosting.sites[].repo` is matched against it to decide which
// branches the scan must give cap-exempt trees. A remote source uses its configured name rather
// than its mirror directory, which carries a cache-key digest. A collision rename can still
// move the final slug — that is settled in Assemble, and hosting is re-bound there.
func PreScanSlug(src config.ResolvedSource) string {
	if src.IsRemote() {
		if src.Slug != "" {
			return src.Slug
		}
		return Slugify(preScanRepoName(src.RepoSourceConfig))
	}
	if slug, err := SlugFor(src.AbsPath, src.Slug); err == nil {
		return slug
	}
	// An unusable slug is a config error the scan will report properly; this is only so the
	// pre-scan warnings have something to say.
	if src.Slug != "" {
		return src.Slug
	}
	return filepath.Base(src.AbsPath)
}

// preScanRepoName is a remote source's repository name: the last path segment for GitLab, whose
// identity is a namespaced project path, and the plain `repo` field for everyone else.
func preScanRepoName(src config.RepoSourceConfig) string {
	if src.Type != "gitlab" {
		return src.Repo
	}
	segments := []string{}
	for _, seg := range strings.Split(src.Project, "/") {
		if seg != "" {
			segments = append(segments, seg)
		}
	}
	if len(segments) == 0 {
		return src.Project
	}
	return segments[len(segments)-1]
}

/* ---- writing -------------------------------------------------------------- */

// WriteArtifact writes forge.json, the blob store and the archive store to outDir.
//
// blobs/ and archives/ are made to MIRROR their maps exactly: missing or changed files are
// written and stale ones deleted, so a repo that lost a file does not leave its bytes behind
// for the next build to serve. Archive map keys are outDir-relative paths
// ("archives/<slug>/<ref-slug>.zip").
//
// The artifact is validated before a byte is written. A structurally invalid artifact is an
// ingest bug rather than a repo-state problem, so it fails the run instead of becoming a
// warning.
func WriteArtifact(data model.ForgeData, blobs, archives map[string][]byte, outDir string) error {
	if err := model.Validate(&data); err != nil {
		return fmt.Errorf("refusing to write an invalid artifact to %s: %w", outDir, err)
	}
	raw, err := model.Serialize(data)
	if err != nil {
		return err
	}

	blobDir := filepath.Join(outDir, BlobDirname)
	if err := os.MkdirAll(blobDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", blobDir, err)
	}
	artifactPath := filepath.Join(outDir, ArtifactFilename)
	if err := os.WriteFile(artifactPath, raw, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", artifactPath, err)
	}

	if err := mirrorDir(blobDir, blobs, func(sha string) string { return sha }); err != nil {
		return err
	}

	// archives/ mirrors the map, whose keys carry the "archives/" prefix the site uses.
	archiveDir := filepath.Join(outDir, ArchiveDirname)
	wanted := make(map[string][]byte, len(archives))
	for rel, buf := range archives {
		norm := filepath.ToSlash(rel)
		if !strings.HasPrefix(norm, ArchiveDirname+"/") {
			return fmt.Errorf("archive path outside %s/: %s", ArchiveDirname, rel)
		}
		wanted[norm[len(ArchiveDirname)+1:]] = buf
	}
	if err := mirrorDir(archiveDir, wanted, filepath.FromSlash); err != nil {
		return err
	}

	// Drop now-empty per-repo directories, so a removed repo does not leave an empty folder.
	entries, err := os.ReadDir(archiveDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read %s: %w", archiveDir, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sub := filepath.Join(archiveDir, entry.Name())
		remaining, err := os.ReadDir(sub)
		if err != nil {
			return fmt.Errorf("read %s: %w", sub, err)
		}
		if len(remaining) == 0 {
			if err := os.RemoveAll(sub); err != nil {
				return fmt.Errorf("remove empty %s: %w", sub, err)
			}
		}
	}
	return nil
}

// mirrorDir makes dir hold exactly the files in want, keyed by a forward-slash relative path
// that toPath turns into a real one. Content-addressed and archive files alike are only
// rewritten when their size differs, because both are effectively immutable for a given name
// and rewriting every blob on every build is the difference between a fast rebuild and a slow
// one.
func mirrorDir(dir string, want map[string][]byte, toPath func(string) string) error {
	existing, err := listFilesRecursive(dir)
	if err != nil {
		return err
	}
	// Sorted so the writes happen in a stable order — the bytes on disk do not depend on it,
	// but a log or a strace of two runs should still line up.
	keys := make([]string, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, rel := range keys {
		buf := want[rel]
		file := filepath.Join(dir, toPath(rel))
		if existing[rel] {
			delete(existing, rel)
			if st, err := os.Stat(file); err == nil && st.Mode().IsRegular() && st.Size() == int64(len(buf)) {
				continue
			}
		}
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(file), err)
		}
		if err := os.WriteFile(file, buf, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", file, err)
		}
	}
	stale := make([]string, 0, len(existing))
	for rel := range existing {
		stale = append(stale, rel)
	}
	sort.Strings(stale)
	for _, rel := range stale {
		file := filepath.Join(dir, toPath(rel))
		if err := os.RemoveAll(file); err != nil {
			return fmt.Errorf("remove stale %s: %w", file, err)
		}
	}
	return nil
}

// listFilesRecursive is every regular file under dir as a set of forward-slash relative paths.
// A directory that does not exist yet is empty rather than an error — the first build has no
// blobs/ to mirror.
func listFilesRecursive(dir string) (map[string]bool, error) {
	out := map[string]bool{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = true
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	return out, nil
}
