// Package scaffold writes a fresh site's hand-authored files — the port of
// scripts/lib/scaffold.ts, and the whole of `frznforge new <dir>`.
//
// What this is *not*: an installer. `new` writes every file a site owner actually authors
// (config, profile, notes, orgs, .gitignore, their own README) into a directory, correct on the
// first build, with one commented example of every source type. The engine is the `frznforge`
// binary plus `web/`, and both come from a frznforge checkout; docs/user/starting-a-site.md
// spells out that half.
//
// Three rules the whole package is built around:
//
//  1. **Never clobber.** An existing file is reported and left exactly as it was, --force or
//     not. --force only relaxes the "the directory must be empty" precondition.
//  2. **Say everything out loud.** Every path written, every path kept, and next steps naming
//     the files to edit. A scaffold the user has to go spelunking through is not a scaffold.
//  3. **Everything written must be valid.** The generated frznforge.config.jsonc parses under
//     the REAL config loader and the generated markdown under the REAL frontmatter parser —
//     scaffold_test.go asserts both against the genuine implementations rather than copies,
//     because a copy passes forever while the scaffold rots.
//
// The file set is a pure function (Files) so the tests, the dry run and the writer all agree by
// construction, and --dry-run cannot drift from what a real run would produce.
package scaffold

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

/* ---- the file set -------------------------------------------------------- */

// File is one file the scaffold writes.
type File struct {
	// Path is relative to the target directory, POSIX-separated.
	Path string
	// Contents is the full text: LF line endings, always ending in a newline.
	Contents string
	// Purpose is the one line printed beside the path, explaining what the file is for.
	Purpose string
}

// Files is every file a fresh site starts with, in the order they are written and printed.
//
// Pure: no filesystem, no clock, no environment. The dry run prints exactly this list and a real
// run writes exactly this list, so the two cannot disagree.
func Files() []File {
	return []File{
		{Path: "frznforge.config.jsonc", Contents: configJSONC, Purpose: "site config — start here"},
		{Path: "content/profile.md", Contents: profileMarkdown, Purpose: "your profile page"},
		{Path: "content/notes/welcome.md", Contents: noteMarkdown, Purpose: "an example note (safe to delete)"},
		{Path: "content/orgs/example-org.md.example", Contents: orgExample, Purpose: "an example org page (inert until renamed)"},
		{Path: ".gitignore", Contents: gitignore, Purpose: "ignores data/, dist/, the cache, the binary"},
		{Path: "README.md", Contents: readmeMarkdown, Purpose: "your site’s README, not frznforge’s"},
	}
}

// engineMarker is the file whose presence means "the engine is already in this directory".
//
// web/ is the half of the engine that has to sit beside the content — the build copies it into
// dist/ verbatim — so finding it is what tells the scaffold not to print "copy the engine in".
var engineMarker = []string{"web", "css", "global.css"}

/* ---- options and results ------------------------------------------------- */

// Options steer one scaffold run.
type Options struct {
	// Dir is the target directory, already resolved by the caller against its own cwd.
	Dir string
	// Force allows a directory that already has files in it. It never allows overwriting one.
	Force bool
	// DryRun prints what would happen and touches nothing.
	DryRun bool
	// Cwd is what paths in the printed next steps are made relative to. Empty means os.Getwd.
	Cwd string
}

// Result reports what a run did, or would have done.
type Result struct {
	// Dir is the absolute target directory.
	Dir string
	// Written are the relative paths written (empty on a dry run).
	Written []string
	// Kept are the relative paths that already existed and were left alone.
	Kept []string
	// Planned are the relative paths a real run would write — the same on a dry run and a real
	// one.
	Planned []string
	DryRun  bool
	// CreatedDir is true when the target directory did not exist and was (or would be) created.
	CreatedDir bool
	// EngineReady is true when web/ is already sitting in the target, i.e. the user scaffolded
	// into a frznforge checkout. "Copy the engine in" is then not a next step, and printing it
	// anyway is how a good instruction list teaches people to skim.
	EngineReady bool
}

// Error is a refusal the CLI reports as `error: …` and exit 1, with no stack trace. Every
// refusal in this package is one, so a caller can tell "you asked for something I will not do"
// from "the disk failed".
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

// Usage is `frznforge new`'s own help. main.go's usage lists the command; this is the detail,
// printed for `frznforge new --help` and for a bad flag.
const Usage = `frznforge new — scaffold the files you author into a new directory

Usage
  frznforge new <dir> [--force] [--dry-run]

Options
  --force      Write into a directory that already has files in it. Existing files are never
               overwritten — they are reported and left alone.
  --dry-run    Print the files that would be created; write nothing, not even the directory.
  --help, -h   Show this message.

It writes six files: frznforge.config.jsonc, content/profile.md, content/notes/welcome.md,
content/orgs/example-org.md.example, .gitignore and a README.md of your own.

It does NOT write the engine. That is the frznforge binary — put it on your PATH — plus web/,
which is copied into dist/ on every build and belongs beside your content. Both come from a
frznforge checkout. See docs/user/starting-a-site.md.`

/* ---- writing ------------------------------------------------------------- */

// Scaffold writes the file set into opts.Dir.
//
// It refuses (with *Error) when the target exists, is non-empty and Force was not given, or when
// it exists and is not a directory. Existing files are NEVER overwritten, with or without Force:
// they come back in Kept so the caller can print them.
func Scaffold(opts Options) (Result, error) {
	dir, err := filepath.Abs(opts.Dir)
	if err != nil {
		return Result{}, err
	}

	createdDir := false
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		createdDir = true
	case err != nil:
		return Result{}, err
	case !info.IsDir():
		return Result{}, &Error{fmt.Sprintf("%s exists and is not a directory.", dir)}
	default:
		empty, err := isEmptyEnough(dir)
		if err != nil {
			return Result{}, err
		}
		if !empty && !opts.Force {
			return Result{}, &Error{fmt.Sprintf(
				"%s is not empty. Re-run with --force to add the missing files to it "+
					"(existing files are never overwritten), or pick an empty directory.", dir)}
		}
	}

	result := Result{Dir: dir, DryRun: opts.DryRun, CreatedDir: createdDir}
	for _, file := range Files() {
		target := filepath.Join(dir, filepath.FromSlash(file.Path))
		// Stat rather than a plain create-exclusive write: the caller has to be able to *say*
		// which files it left alone, and on a dry run there is no write to learn it from.
		exists := false
		if !createdDir {
			if _, err := os.Stat(target); err == nil {
				exists = true
			}
		}
		if exists {
			result.Kept = append(result.Kept, file.Path)
			continue
		}
		result.Planned = append(result.Planned, file.Path)
		if opts.DryRun {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return Result{}, err
		}
		// O_EXCL closes the gap between the stat above and the write: if something else created
		// the file in between, we keep our promise never to clobber rather than winning the race.
		written, err := writeNew(target, file.Contents)
		if err != nil {
			return Result{}, err
		}
		if !written {
			result.Kept = append(result.Kept, file.Path)
			result.Planned = result.Planned[:len(result.Planned)-1]
			continue
		}
		result.Written = append(result.Written, file.Path)
	}

	if !createdDir {
		if _, err := os.Stat(filepath.Join(append([]string{dir}, engineMarker...)...)); err == nil {
			result.EngineReady = true
		}
	}
	return result, nil
}

// writeNew creates path with contents, reporting written=false when the file already exists.
func writeNew(path, contents string) (bool, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, nil
		}
		return false, err
	}
	if _, err := f.WriteString(contents); err != nil {
		f.Close()
		return false, err
	}
	return true, f.Close()
}

// isEmptyEnough reports whether a directory is empty enough to scaffold into.
//
// A lone .git does not count: `git init my-site && cd my-site` before scaffolding is a normal
// way to start, and refusing it would send people to --force for no reason. Anything else —
// including a stray dotfile — makes the directory non-empty, because the whole point of the
// check is "I am about to write into a directory you may care about".
func isEmptyEnough(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if e.Name() != ".git" {
			return false, nil
		}
	}
	return true, nil
}

/* ---- reporting ----------------------------------------------------------- */

// NextSteps is the block printed after a successful run: the exact files to edit, in the order
// they matter, and the commands that turn them into a site.
//
// Named files only — "configure your site" is not a next step, "open frznforge.config.jsonc and
// fill in "repos": [ … ]" is.
func NextSteps(result Result, cwd string) []string {
	where := result.Dir
	if cwd != "" {
		if rel, err := filepath.Rel(cwd, result.Dir); err == nil && !strings.HasPrefix(rel, "..") {
			where = rel
		}
	}
	if where == "" {
		where = "."
	}

	var steps [][]string
	if !result.EngineReady {
		steps = append(steps, []string{
			"Put the frznforge engine where this site can reach it:",
			"- the `frznforge` binary on your PATH (go build -o frznforge ./cmd/frznforge in a",
			"  frznforge checkout)",
			"- that checkout's web/ copied into " + where + " — it is served verbatim, and without it",
			"  the site builds fine and then has no styles and a dead listing",
			"(docs/user/starting-a-site.md — there is no package to install.)",
		})
	}
	steps = append(steps, []string{
		"Edit " + filepath.Join(where, "frznforge.config.jsonc"),
		`- site.title, owner.name, owner.handle`,
		`- "repos": [ … ]  — uncomment one of the five examples and point it at a repository`,
	})
	steps = append(steps, []string{
		"Edit " + filepath.Join(where, "content", "profile.md") +
			" — bio, sites, pinned, and the body under the frontmatter",
	})
	steps = append(steps, []string{
		"Replace " + filepath.Join(where, "content", "notes", "welcome.md") +
			" with a note of your own, or delete it",
	})
	steps = append(steps, []string{"frznforge ingest, then frznforge build   (then serve dist/ anywhere)"})

	out := []string{"Next steps"}
	for i, step := range steps {
		out = append(out, fmt.Sprintf("  %d. %s", i+1, step[0]))
		for _, line := range step[1:] {
			out = append(out, "     "+line)
		}
	}
	return out
}

// Run scaffolds and prints the whole command. Every refusal is an *Error the caller reports
// without a stack trace.
func Run(opts Options, log func(string)) (Result, error) {
	result, err := Scaffold(opts)
	if err != nil {
		return result, err
	}

	purpose := map[string]string{}
	for _, f := range Files() {
		purpose[f.Path] = f.Purpose
	}

	if result.DryRun {
		created := ""
		if result.CreatedDir {
			created = " (would be created)"
		}
		log(fmt.Sprintf("Dry run — nothing was written to %s%s.", result.Dir, created))
		log("")
		log("Would create:")
		for _, p := range result.Planned {
			log(fmt.Sprintf("  + %s %s", pad(p), purpose[p]))
		}
		for _, p := range result.Kept {
			log(fmt.Sprintf("  = %s already there, would be left alone", pad(p)))
		}
		log("")
		log("Re-run without --dry-run to write these files.")
		return result, nil
	}

	created := ""
	if result.CreatedDir {
		created = " (created)"
	}
	log(fmt.Sprintf("Scaffolded a frznforge site in %s%s.", result.Dir, created))
	log("")
	for _, p := range result.Written {
		log(fmt.Sprintf("  + %s %s", pad(p), purpose[p]))
	}
	for _, p := range result.Kept {
		log(fmt.Sprintf("  = %s already existed — left untouched", pad(p)))
	}
	if len(result.Written) == 0 {
		log("  (nothing to do — every file was already there)")
	}
	log("")
	for _, line := range NextSteps(result, opts.Cwd) {
		log(line)
	}
	return result, nil
}

// pad left-aligns a path in the column the purpose text starts at.
func pad(s string) string {
	const width = 38
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}
