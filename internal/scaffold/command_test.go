package scaffold

// `frznforge new`'s argument parsing, beyond what scaffold_test.go's port of scaffold.test.ts
// covers. These are the cases cli.test.ts holds `init`'s parser to and this one has to meet as
// well: a mistake is named with the flag the user actually typed, every mistake is reported at
// once, and nothing is written by a command line that was refused.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandNamesTheFlagRatherThanTheWholeArgument(t *testing.T) {
	// `--port=9` is a user reaching for init's browser UI from the wrong command. Quoting
	// `--port=9` back at them would suggest the value was the problem; naming `--port` and the
	// command it belongs to is a line they can act on.
	err := Command([]string{"site", "--port=9"}, t.TempDir(), func(string) {})
	if err == nil {
		t.Fatal("--port=9 was accepted by new")
	}
	if !strings.Contains(err.Error(), "--port is an init option: frznforge init --port") {
		t.Errorf("message = %q", err.Error())
	}
	if strings.Contains(err.Error(), "--port=9") {
		t.Error("the message quotes the value back instead of the flag")
	}
}

func TestCommandReportsEveryMistakeAtOnce(t *testing.T) {
	// Reporting one mistake at a time turns a wrong command line into a sequence of failed runs.
	err := Command([]string{"site", "--nope", "--yes", "spare"}, t.TempDir(), func(string) {})
	if err == nil {
		t.Fatal("three mistakes were accepted")
	}
	for _, want := range []string{
		"unknown option: --nope (new takes --force, --dry-run)",
		"--yes is an init option",
		"unexpected argument: spare",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%s", want, err.Error())
		}
	}
}

func TestCommandAnswersTheShortHelpFlagToo(t *testing.T) {
	// `-h` is what people type. Falling through to "unknown option" would answer a request for
	// help with an error, which is the one moment a CLI cannot afford to.
	var out []string
	if err := Command([]string{"-h"}, t.TempDir(), func(l string) { out = append(out, l) }); err != nil {
		t.Fatalf("-h: %v", err)
	}
	if !strings.Contains(strings.Join(out, "\n"), "new <dir>") {
		t.Errorf("-h printed something other than the usage:\n%s", strings.Join(out, "\n"))
	}
}

func TestCommandHelpWinsOverEverythingElseOnTheLine(t *testing.T) {
	// Someone who adds --help to a command they are unsure about wants the help, not a refusal
	// for the argument that made them unsure — and certainly not a scaffolded directory.
	cwd := t.TempDir()
	var out []string
	if err := Command([]string{"site", "--help", "--nope"}, cwd, func(l string) { out = append(out, l) }); err != nil {
		t.Fatalf("--help alongside a bad flag: %v", err)
	}
	if !strings.Contains(strings.Join(out, "\n"), "new <dir>") {
		t.Error("the usage was not printed")
	}
	if _, err := os.Stat(filepath.Join(cwd, "site")); !os.IsNotExist(err) {
		t.Error("a --help run scaffolded anyway")
	}
}

func TestCommandCarriesBothFlagsThroughToTheScaffold(t *testing.T) {
	// --force and --dry-run together is "show me what a forced run would do to this directory".
	// If either flag were dropped on the way through, the run would refuse (no --force) or write
	// for real (no --dry-run) — and the second of those is unrecoverable.
	cwd := t.TempDir()
	dir := filepath.Join(cwd, "occupied")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "mine.txt"), "x")

	var out []string
	if err := Command([]string{"occupied", "--force", "--dry-run"}, cwd, func(l string) { out = append(out, l) }); err != nil {
		t.Fatalf("--force --dry-run: %v", err)
	}
	if got := listTree(t, dir); !equalStrings(got, []string{"mine.txt"}) {
		t.Errorf("the dry run wrote %v", got)
	}
	if !strings.Contains(strings.Join(out, "\n"), "Dry run") {
		t.Error("the run did not announce itself as a dry run")
	}
}

func TestCommandTakesAnAbsoluteDirectoryAsGiven(t *testing.T) {
	// The relative case is covered in scaffold_test.go; this is the other half. Joining an
	// absolute path onto the cwd would produce a nonsense path on Windows and a wrong one
	// everywhere else.
	cwd := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := Command([]string{target}, cwd, func(string) {}); err != nil {
		t.Fatalf("absolute target: %v", err)
	}
	if got := listTree(t, target); len(got) == 0 {
		t.Error("nothing was written to the absolute target")
	}
	if entries, _ := os.ReadDir(cwd); len(entries) != 0 {
		t.Errorf("the working directory gained %d entries", len(entries))
	}
}
