package logging

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `frznforge dev` must not overwrite the log of the build it is previewing.
//
// The log is truncated per run and build-then-preview is the documented loop, so sharing one file
// meant the command you run to LOOK at the site erased the record of the command that made it —
// and the run you want to debug is always the one before the one you are running now.
//
// The first attempt at this fix added the name parameter and then ignored it: SetupFileNamed took
// a name and opened LogPath(outDir) anyway, so `dev` still clobbered frznforge.log and every doc
// that said otherwise was wrong. Hence a test that opens both files rather than trusting the
// signature.
func TestDevLogDoesNotOverwriteTheBuildLog(t *testing.T) {
	dir := t.TempDir()

	if err := SetupFileNamed(dir, LogName, os.Stderr); err != nil {
		t.Fatalf("build log: %v", err)
	}
	slog.Debug("pretend build record")
	if err := Close(); err != nil {
		t.Fatalf("close build log: %v", err)
	}

	if err := SetupFileNamed(dir, DevLogName, os.Stderr); err != nil {
		t.Fatalf("dev log: %v", err)
	}
	slog.Debug("pretend dev record")
	if err := Close(); err != nil {
		t.Fatalf("close dev log: %v", err)
	}

	build := read(t, filepath.Join(dir, LogName))
	dev := read(t, filepath.Join(dir, DevLogName))

	if !strings.Contains(build, "pretend build record") {
		t.Errorf("the build log lost its own record — dev truncated it:\n%s", build)
	}
	if strings.Contains(build, "pretend dev record") {
		t.Errorf("dev wrote into the build's log:\n%s", build)
	}
	if !strings.Contains(dev, "pretend dev record") {
		t.Errorf("the dev log is missing its record:\n%s", dev)
	}
	if LogPathNamed(dir, DevLogName) == LogPathNamed(dir, LogName) {
		t.Error("the two names resolve to one path")
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}
