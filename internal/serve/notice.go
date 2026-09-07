package serve

import (
	"os"
	"path/filepath"
	"strings"
)

// Paths is what the notice and the preflight talk about. Everything is absolute; both report
// project-relative.
type Paths struct {
	// Root is the project directory — what paths are reported relative to.
	Root string
	// Dir is the directory being served (the build output).
	Dir string
	// ArtifactFile is <ingest.outDir>/forge.json, the file the pages were rendered from. Empty
	// when the caller pointed --dir at a directory of its own: then there is no project artifact
	// to have an opinion about, only files to serve.
	ArtifactFile string
}

// Notice is printed before the server starts, on every run.
//
// Its whole job is to stop the reader wondering why an edit did not show up. This server
// renders nothing, and the commands that do are named. The stale-`dist` surprise is the most
// common confusion this tool produces, and it is cheaper to pre-empt in eight lines than to
// debug once.
func Notice(p Paths) []string {
	dir := rel(p.Root, p.Dir)
	from := "the artifact"
	if p.ArtifactFile != "" {
		from = rel(p.Root, p.ArtifactFile)
	}
	return []string{
		"frznforge dev — serving " + dir + "/ from the most recent `frznforge build`.",
		"",
		"  Nothing is rebuilt here. These are the static files already in " + dir + "/, rendered",
		"  from " + from + " as it stood at that build. Editing a template, a style, content/",
		"  or the config changes nothing you see until you build again — no file is watched and",
		"  no page is re-rendered while this runs.",
		"",
		"    frznforge build     re-render the artifact you already have → " + dir + "/",
		"    frznforge ingest    refresh " + from + " from the repositories first",
		"",
	}
}

// PreflightResult is the exit decision plus what to print.
type PreflightResult struct {
	OK bool
	// Missing lists what is absent, in the order a reader would fix it: the artifact comes
	// before the site rendered from it.
	Missing []string
	Lines   []string
}

// Preflight checks the things the server would otherwise fail on obscurely.
//
// Serving a project that has never been built is the case worth handling well: an empty
// directory answers every request with "not found" and says nothing about why, so both inputs
// are checked up front and a missing one prints the command that produces it. An existing
// directory with no index.html counts as missing too — that is a build that was interrupted,
// and it fails in exactly the same puzzling way.
func Preflight(p Paths, exists func(string) bool) PreflightResult {
	var missing []string
	if p.ArtifactFile != "" && !exists(p.ArtifactFile) {
		missing = append(missing, rel(p.Root, p.ArtifactFile))
	}
	switch {
	case !exists(p.Dir):
		missing = append(missing, rel(p.Root, p.Dir))
	case !exists(filepath.Join(p.Dir, "index.html")):
		missing = append(missing, rel(p.Root, filepath.Join(p.Dir, "index.html")))
	}
	if len(missing) == 0 {
		return PreflightResult{OK: true}
	}

	artifact := "the artifact"
	if p.ArtifactFile != "" {
		artifact = rel(p.Root, p.ArtifactFile)
	}
	lines := []string{"frznforge dev: there is no built site to serve yet.", ""}
	for _, m := range missing {
		lines = append(lines, "  missing: "+m)
	}
	lines = append(lines,
		"",
		"  Build it first — that is both halves:",
		"",
		"    frznforge ingest    scan the repositories into "+artifact,
		"    frznforge build     render that artifact into "+rel(p.Root, p.Dir)+"/",
		"",
		"  Then `frznforge dev` again.",
	)
	return PreflightResult{Missing: missing, Lines: lines}
}

// Exists is the real filesystem answer Preflight takes. It is a parameter so the messages can
// be tested without building a site to look at.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// rel reports a path the way a reader can paste it into a shell: project-relative and
// forward-slashed, or absolute when it lives outside the project (where there is nothing to be
// relative to, but the path is still actionable).
func rel(root, target string) string {
	if root == "" || target == "" {
		return filepath.ToSlash(target)
	}
	r, err := filepath.Rel(root, target)
	if err != nil || r == "" || r == "." || strings.HasPrefix(r, "..") {
		return filepath.ToSlash(target)
	}
	return filepath.ToSlash(r)
}
