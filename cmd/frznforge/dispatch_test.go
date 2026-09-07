package main

// Command dispatch — the port of cli.test.ts's `main` help/unknown-command cases, widened to the
// commands the Go binary answers that the TypeScript CLI never had.
//
// `new` is covered thoroughly in internal/scaffold, where the whole command is testable without
// a process. What is left for here is the wiring: that `frznforge new` reaches it at all, and
// that it resolves its directory against the Io's working directory rather than the process's.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"frznforge/internal/config"
	"frznforge/internal/model"
)

func TestRunPrintsUsageForHelpAndForNothingAtAll(t *testing.T) {
	// A bare `frznforge` is somebody finding out what this thing does. Answering with an error
	// would make the first contact with the tool a failure.
	for _, argv := range [][]string{nil, {}, {"help"}, {"--help"}, {"-h"}} {
		tio := newTestIo(t.TempDir())
		if err := run(argv, tio.Io); err != nil {
			t.Errorf("%v: %v", argv, err)
			continue
		}
		if tio.stdout() != usage {
			t.Errorf("%v printed something other than the usage", argv)
		}
		if tio.stderr() != "" {
			t.Errorf("%v wrote to stderr: %s", argv, tio.stderr())
		}
	}
}

func TestRunRejectsAnUnknownCommandWithTheUsage(t *testing.T) {
	// A typo'd command is the most likely way anyone meets this branch, so the answer has to be
	// the list of real commands rather than a bare refusal.
	tio := newTestIo(t.TempDir())
	err := run([]string{"frobnicate"}, tio.Io)
	if err == nil {
		t.Fatal("an unknown command ran")
	}
	mustContain(t, err.Error(), `unknown command "frobnicate"`, "the command is not quoted back")
	mustContain(t, err.Error(), "Usage", "the refusal does not carry the usage")
}

func TestUsageDocumentsEveryCommandAndTheFlagsThatChangeABuild(t *testing.T) {
	// The usage is the only documentation a user has in front of them at the moment they need it.
	// A command that dispatches but is not listed is a command nobody discovers.
	for _, want := range []string{
		"frznforge build", "frznforge ingest", "frznforge dev", "frznforge init",
		"frznforge new <dir>", "frznforge verify", "frznforge config migrate", "frznforge help",
		"--no-ingest", "--no-cache", "--backfill-metadata", "--postprocess=<cmd>", "--serial",
	} {
		mustContain(t, usage, want, "the usage does not document part of the CLI")
	}
	// The one behaviour a reader must not have to discover by trying it: --no-ingest refuses
	// rather than publishing an empty site.
	mustContain(t, usage, "refuses when there is none", "the usage does not warn about the --no-ingest refusal")
}

/* ---- new ------------------------------------------------------------------ */

func TestNewScaffoldsIntoADirectoryResolvedAgainstTheIoCwd(t *testing.T) {
	// The command takes a relative directory and the Io carries the working directory, so a run
	// driven from a test (or from the wizard, or from a different root) puts the site where the
	// caller meant rather than where the process happens to stand.
	cwd := t.TempDir()
	tio := newTestIo(cwd)
	if err := run([]string{"new", "my-site"}, tio.Io); err != nil {
		t.Fatalf("new my-site: %v", err)
	}

	dir := filepath.Join(cwd, "my-site")
	for _, name := range []string{config.Filename, "README.md", ".gitignore",
		filepath.Join("content", "profile.md"), filepath.Join("content", "notes", "welcome.md")} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("no %s in the scaffolded site: %v", name, err)
		}
	}
	// The config it wrote is the one the engine will read, so the loader is the assertion worth
	// making about it.
	if _, err := config.Load(dir); err != nil {
		t.Errorf("the scaffolded config does not load: %v", err)
	}
	mustContain(t, tio.stdout(), "my-site", "the run does not say where it wrote")
}

func TestNewDryRunCreatesNothing(t *testing.T) {
	// --dry-run is what someone runs when they are not sure the directory is right. Creating even
	// the directory would defeat it, and `new` refuses a non-empty target on the real run after.
	cwd := t.TempDir()
	tio := newTestIo(cwd)
	if err := run([]string{"new", "peek", "--dry-run"}, tio.Io); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cwd, "peek")); !os.IsNotExist(err) {
		t.Error("--dry-run created the directory")
	}
	mustContain(t, tio.stdout(), "Dry run", "a dry run does not announce itself")
}

func TestNewRefusesADirectoryThatIsMissingAndFlagsThatBelongToInit(t *testing.T) {
	cwd := t.TempDir()
	tio := newTestIo(cwd)
	if err := run([]string{"new"}, tio.Io); err == nil {
		t.Error("new with no directory was accepted")
	} else {
		mustContain(t, err.Error(), "new needs a directory", "the refusal does not say what is missing")
	}
	// Accepting an init flag that silently does nothing is the worst of the three options; the
	// message names the command it belongs to instead.
	if err := run([]string{"new", "x", "--select=all"}, tio.Io); err == nil {
		t.Error("an init flag was accepted by new")
	} else {
		mustContain(t, err.Error(), "--select is an init option", "the message does not redirect to the right command")
	}
}

/* ---- config migrate ------------------------------------------------------- */

func TestConfigNeedsASubcommandAndRejectsAnUnknownOne(t *testing.T) {
	tio := newTestIo(t.TempDir())
	if err := run([]string{"config"}, tio.Io); err == nil {
		t.Error("bare `config` ran")
	} else {
		mustContain(t, err.Error(), "config needs a subcommand", "the refusal does not say what is missing")
	}
	if err := run([]string{"config", "reticulate"}, tio.Io); err == nil {
		t.Error("an unknown config subcommand ran")
	} else {
		// There is exactly one subcommand, so naming it is more useful than listing "known
		// subcommands: migrate".
		mustContain(t, err.Error(), "did you mean `config migrate`", "the refusal does not suggest the real subcommand")
	}
	if err := run([]string{"config", "migrate", "--overwrite"}, tio.Io); err == nil {
		t.Error("an unknown flag was accepted by config migrate")
	} else {
		mustContain(t, err.Error(), "only --force", "the refusal does not name the flag that exists")
	}
}

/* ---- verify --------------------------------------------------------------- */

func TestVerifyReportsByteIdentityAndPointsAtTheFirstDifference(t *testing.T) {
	// verify is the oracle every claim about the Go ingest rests on, so its two answers have to
	// be unambiguous: identical, or here is the byte where it stopped being identical.
	root := t.TempDir()
	file := filepath.Join(root, "forge.json")
	raw := emptyArtifactJSON(t)
	if err := os.WriteFile(file, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	tio := newTestIo(root)
	if err := run([]string{"verify", file}, tio.Io); err != nil {
		t.Fatalf("a freshly serialized artifact did not verify: %v", err)
	}
	mustContain(t, tio.stdout(), "re-serialized byte for byte", "a clean verify does not say so")
	mustContain(t, tio.stdout(), "schema v"+itoa(model.SchemaVersion), "the report does not name the schema version")

	// An artifact that still parses but does not round-trip is exactly what this catches — an
	// editor that reformatted the file, or a serializer that drifted.
	if err := os.WriteFile(file, []byte(raw+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := run([]string{"verify", file}, tio.Io)
	if err == nil {
		t.Fatal("a file that does not round-trip verified clean")
	}
	mustContain(t, err.Error(), "first difference at byte", "the failure does not say where it stopped matching")
	mustContain(t, err.Error(), "lengths:", "the failure does not compare the two lengths")
}

func TestVerifySaysWhichFileItCouldNotRead(t *testing.T) {
	tio := newTestIo(t.TempDir())
	missing := filepath.Join(t.TempDir(), "nope.json")
	err := run([]string{"verify", missing}, tio.Io)
	if err == nil {
		t.Fatal("verifying a missing file succeeded")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("the error does not name the file: %v", err)
	}
}

// itoa avoids a strconv import for one call in one assertion.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
