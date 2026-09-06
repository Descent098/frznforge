// Command frznforge is the 0.4.0 engine: one binary that ingests repositories and renders the
// static site.
//
// It is being built up phase by phase (docs/dev/plans/version-0.4.0-phased.md). Today it
// carries two commands:
//
//   - `verify`, the wedge the rest of the rewrite is checked against: it proves Go reads and
//     re-emits the artifact byte for byte, so every later claim about the Go ingest being
//     correct is a diff rather than an opinion.
//   - `config migrate`, which converts a site's frznforge.config.ts into the JSONC the Go
//     loader reads — comments and all, since they are the configuration's documentation.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"frznforge/internal/config"
	"frznforge/internal/model"
)

const usage = `frznforge — static forge site generator

Usage
  frznforge verify [<forge.json>]   Read an artifact, validate it, re-serialize it, and
                                    compare byte for byte with the file on disk.
                                    Defaults to data/forge.json.

  frznforge config migrate [--force]
                                    Convert ./frznforge.config.ts into
                                    ./frznforge.config.jsonc, comments and all. Refuses to
                                    overwrite an existing .jsonc unless --force is given.

  frznforge help                    This message.
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Print(usage)
		return nil
	}
	switch args[0] {
	case "verify":
		path := filepath.Join("data", "forge.json")
		if len(args) > 1 {
			path = args[1]
		}
		return verify(path)
	case "config":
		return configCmd(args[1:])
	default:
		return fmt.Errorf("unknown command %q\n\n%s", args[0], usage)
	}
}

func configCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("config needs a subcommand\n\n%s", usage)
	}
	switch args[0] {
	case "migrate":
		force := false
		for _, a := range args[1:] {
			switch a {
			case "--force", "-f":
				force = true
			default:
				return fmt.Errorf("unknown flag %q for `config migrate` (only --force)", a)
			}
		}
		return migrateConfig(force)
	default:
		return fmt.Errorf("unknown config subcommand %q — did you mean `config migrate`?", args[0])
	}
}

// migrateConfig converts the config in the current directory.
//
// It never overwrites without --force and never deletes the TypeScript file: for one phase the
// two coexist (the Astro build still reads the .ts), and a migration that destroys its own input
// leaves the user with nothing to compare against when a value looks wrong.
func migrateConfig(force bool) error {
	src, err := os.ReadFile(config.TSFilename)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("no ./%s here — run this from the directory holding your config", config.TSFilename)
		}
		return fmt.Errorf("read ./%s: %w", config.TSFilename, err)
	}
	if _, err := os.Stat(config.Filename); err == nil && !force {
		return fmt.Errorf("./%s already exists — pass --force to overwrite it", config.Filename)
	}

	out, err := config.MigrateTS(src)
	if err != nil {
		return fmt.Errorf("./%s: %w", config.TSFilename, err)
	}
	if err := os.WriteFile(config.Filename, out, 0o644); err != nil {
		return fmt.Errorf("write ./%s: %w", config.Filename, err)
	}

	// The comment counts are the headline number: this converter exists to carry documentation
	// across, so a run that quietly halved it should be visible without opening the file.
	before, _ := config.CountCommentLines(src)
	after, _ := config.CountCommentLines(out)
	fmt.Printf("wrote ./%s — %d lines, %d of them carrying comments (%d in ./%s)\n",
		config.Filename, countLines(out), after, before, config.TSFilename)

	// Parsing what was just written is the only claim worth making about it: the file is not a
	// migration until the loader that will read it from now on accepts it.
	cfg, err := config.ParseBytes(out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: the converted file does not load yet:\n  %v\nEdit ./%s and re-check with `frznforge config migrate --force`.\n", err, config.Filename)
		return nil
	}
	fmt.Printf("loads cleanly — %d repo(s), %d organization(s), %d contributor(s), palette %q\n",
		len(cfg.Repos), len(cfg.Organizations), len(cfg.Contributors), cfg.Theme.Palette)
	fmt.Printf("./%s is left in place; both files are read this version, so keep them in step until the TypeScript build goes away.\n", config.TSFilename)
	return nil
}

// countLines counts the lines in b. The final newline terminates the last line rather than
// starting an empty one, so a 83-line file that ends in \n is not reported as 84.
func countLines(b []byte) int {
	n := bytes.Count(b, []byte("\n"))
	if len(b) > 0 && b[len(b)-1] != '\n' {
		n++
	}
	return n
}

// verify is the byte-identity oracle described in the package comment.
//
// It reports the FIRST differing byte with context rather than just "differs", because the
// failures this catches are things like a unicode escape or a dropped key — invisible in a
// diff of two 5 MB files unless something points at them.
func verify(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	data, err := model.Parse(raw)
	if err != nil {
		return err
	}
	out, err := model.Serialize(data)
	if err != nil {
		return err
	}
	if bytes.Equal(raw, out) {
		fmt.Printf("ok %s — %d bytes, schema v%d, %d repos, %d notes, %d orgs, re-serialized byte for byte\n",
			path, len(raw), data.SchemaVersion, len(data.Repos), len(data.Notes), len(data.Organizations))
		return nil
	}
	return fmt.Errorf("re-serialized artifact differs from %s\n%s", path, describeDiff(raw, out))
}

// describeDiff points at the first differing byte with a window of context on both sides.
func describeDiff(want, got []byte) string {
	i := 0
	for i < len(want) && i < len(got) && want[i] == got[i] {
		i++
	}
	line := 1 + bytes.Count(want[:i], []byte("\n"))
	window := func(b []byte) string {
		lo := max(0, i-60)
		hi := min(len(b), i+60)
		return fmt.Sprintf("%q", string(b[lo:hi]))
	}
	return fmt.Sprintf("  first difference at byte %d (line %d)\n  on disk:  %s\n  ours:     %s\n  lengths:  %d on disk, %d ours",
		i, line, window(want), window(got), len(want), len(got))
}
