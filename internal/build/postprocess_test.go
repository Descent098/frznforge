package build_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"frznforge/internal/build"
	"frznforge/internal/config"
	"frznforge/internal/model"
)

// The postprocess hook is the one place a user's own tooling touches the output, so the claims
// worth testing are about *when* it runs, not what it does: never by default, after a build that
// succeeded, never after one that failed, and loudly when the command itself fails. A hook that
// quietly half-processed dist/ would publish a site that is neither the old build nor the new
// one, and would look like a success while doing it.

/* ---- fixtures ------------------------------------------------------------ */

// newHookFixture writes the smallest project build.Run accepts: a valid config and an artifact with
// no repositories. The site it renders is deliberately empty — an empty artifact still emits the
// profile, the repos listing, 404 and the search index, which is all these tests need in order
// to say "dist/ was written".
func newHookFixture(t *testing.T, hook *config.PostprocessConfig) string {
	t.Helper()
	root := t.TempDir()

	cfg := struct {
		Owner       map[string]string         `json:"owner"`
		Postprocess *config.PostprocessConfig `json:"postprocess,omitempty"`
	}{
		Owner:       map[string]string{"name": "Fixture Owner", "handle": "fixture"},
		Postprocess: hook,
	}
	// Marshalled rather than templated: a Windows command line is full of backslashes, and a
	// hand-built JSON string would need them escaped by hand in every test that uses one.
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeHookFile(t, filepath.Join(root, config.Filename), raw)

	artifact, err := model.Serialize(model.EmptyForgeData())
	if err != nil {
		t.Fatal(err)
	}
	writeHookFile(t, filepath.Join(root, "data", "forge.json"), artifact)
	return root
}

func writeHookFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

// distMarkerCmd is a shell line that creates <name> inside $FRZNFORGE_DIST_DIR.
//
// A marker written *there* is the ordering evidence these tests turn on: Run wipes the output
// directory before it emits anything, so a marker that survives can only have been written after
// the build finished.
//
// The two shells share no dialect for this — cmd.exe spells expansion %VAR% and sh spells it
// $VAR — so the line is chosen per platform. The alternative, a helper binary compiled in
// TestMain, costs a `go build` on every run to prove the same one thing.
func distMarkerCmd(name string) string {
	if runtime.GOOS == "windows" {
		return `echo done >"%FRZNFORGE_DIST_DIR%\` + name + `"`
	}
	return `echo done > "$FRZNFORGE_DIST_DIR/` + name + `"`
}

// envCaptureCmd is a shell line that writes $name's value into file.
//
// The space before `>` is load-bearing on Windows: cmd expands variables before it parses
// redirections, so a value ending in a digit (t.TempDir() hands out paths ending in `001`) would
// otherwise read as a file-handle redirect.
func envCaptureCmd(name, file string) string {
	if runtime.GOOS == "windows" {
		return `echo %` + name + `% >"` + file + `"`
	}
	return `printf '%s' "$` + name + `" > "` + file + `"`
}

// runHookBuild runs a build and returns whatever the hook printed.
func runHookBuild(t *testing.T, root, outDir, override string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	_, err := build.Run(build.Options{
		Root:           root,
		OutDir:         outDir,
		Now:            time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC),
		Postprocess:    override,
		PostprocessOut: &out,
	})
	return out.String(), err
}

func hookFileExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

/* ---- when the hook runs -------------------------------------------------- */

// TestPostprocessDefaultRunsNothing — the documented default, and the one every existing site
// depends on: no block, no flag, no variable, therefore no process and not a word of output.
func TestPostprocessDefaultRunsNothing(t *testing.T) {
	t.Setenv(build.PostprocessEnvVar, "") // the developer's own shell must not decide this
	root := newHookFixture(t, nil)
	out := filepath.Join(t.TempDir(), "dist")

	printed, err := runHookBuild(t, root, out, "")
	if err != nil {
		t.Fatalf("the build itself must succeed: %v", err)
	}
	if !hookFileExists(t, filepath.Join(out, "index.html")) {
		t.Fatal("the fixture build emitted no index.html, so the rest of this file proves nothing")
	}
	if printed != "" {
		t.Errorf("an unconfigured hook must be silent, printed:\n%s", printed)
	}
}

// TestPostprocessRunsAfterASuccessfulBuild — the marker lands inside dist/, which Run empties
// before it writes anything. If the hook fired earlier than it does, the wipe would have taken
// the marker with it.
func TestPostprocessRunsAfterASuccessfulBuild(t *testing.T) {
	t.Setenv(build.PostprocessEnvVar, "")
	root := newHookFixture(t, &config.PostprocessConfig{Command: distMarkerCmd("hook-ran.txt")})
	out := filepath.Join(t.TempDir(), "dist")

	printed, err := runHookBuild(t, root, out, "")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !hookFileExists(t, filepath.Join(out, "index.html")) {
		t.Error("the site is missing: the hook must not replace the build")
	}
	if !hookFileExists(t, filepath.Join(out, "hook-ran.txt")) {
		t.Fatal("no marker in dist/ — the hook either did not run or ran before the output directory was cleared")
	}
	if !strings.Contains(printed, "postprocess:") {
		t.Errorf("the hook must name itself in the build log, printed:\n%s", printed)
	}
}

// TestPostprocessDoesNotRunWhenTheBuildFails — a failed build leaves a partial dist/, and
// handing a partial directory to somebody's minifier is the failure mode this hook is most able
// to cause. The artifact is removed to fail the build the way a user most often does: by
// building before ingesting.
func TestPostprocessDoesNotRunWhenTheBuildFails(t *testing.T) {
	t.Setenv(build.PostprocessEnvVar, "")
	root := newHookFixture(t, &config.PostprocessConfig{Command: distMarkerCmd("hook-ran.txt")})
	if err := os.Remove(filepath.Join(root, "data", "forge.json")); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "dist")

	printed, err := runHookBuild(t, root, out, "")
	if err == nil {
		t.Fatal("a build with no artifact must fail")
	}
	if hookFileExists(t, filepath.Join(out, "hook-ran.txt")) {
		t.Error("the hook ran over the output of a build that failed")
	}
	if printed != "" {
		t.Errorf("the hook printed something on a failed build:\n%s", printed)
	}
}

// TestPostprocessFailureFailsTheBuild — exit status 3 has to reach the caller, carrying the
// command's own output. Swallowing it would ship a half-processed dist/ under a green build.
func TestPostprocessFailureFailsTheBuild(t *testing.T) {
	t.Setenv(build.PostprocessEnvVar, "")
	// `echo … && exit 3` is the same line in both shells, which is why the failure case needs no
	// per-platform spelling.
	root := newHookFixture(t, &config.PostprocessConfig{Command: "echo boom-from-the-hook && exit 3"})
	out := filepath.Join(t.TempDir(), "dist")

	printed, err := runHookBuild(t, root, out, "")
	if err == nil {
		t.Fatal("a hook that exits non-zero must fail the build")
	}
	if !strings.Contains(err.Error(), "exited 3") {
		t.Errorf("the error must carry the exit status, got: %v", err)
	}
	if !strings.Contains(printed, "boom-from-the-hook") {
		t.Errorf("the command's own output must be shown — it is the only diagnosis available:\n%s", printed)
	}
	if !hookFileExists(t, filepath.Join(out, "index.html")) {
		t.Error("the error promises the site was left as the command found it, so it must still be there")
	}
}

// TestPostprocessEnvironmentReachesTheCommand — the output directory travels in the environment
// rather than as an argument, so the command stays a line the user can paste into a terminal.
// Both variables are absolute: a hook that resolved a relative path would resolve it against
// postprocess.dir, which is not where the site is.
func TestPostprocessEnvironmentReachesTheCommand(t *testing.T) {
	t.Setenv(build.PostprocessEnvVar, "")
	root := t.TempDir()
	outDir := filepath.Join(t.TempDir(), "dist")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	capture := t.TempDir()
	distFile := filepath.Join(capture, "dist.txt")
	rootFile := filepath.Join(capture, "root.txt")

	hook := build.Postprocess{
		Command: envCaptureCmd("FRZNFORGE_DIST_DIR", distFile) + " && " + envCaptureCmd("FRZNFORGE_ROOT", rootFile),
	}
	if err := hook.Run(root, outDir, &bytes.Buffer{}); err != nil {
		t.Fatalf("hook: %v", err)
	}

	for _, c := range []struct{ name, file, want string }{
		{"FRZNFORGE_DIST_DIR", distFile, outDir},
		{"FRZNFORGE_ROOT", rootFile, root},
	} {
		raw, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatalf("%s was never written by the command: %v", c.name, err)
		}
		// cmd's `echo` appends a trailing space and CRLF; the value is what is left.
		if got := strings.TrimSpace(string(raw)); got != c.want {
			t.Errorf("%s reached the command as %q, want %q", c.name, got, c.want)
		}
	}
}

/* ---- precedence ---------------------------------------------------------- */

// TestPostprocessPrecedence pins the order the package comment states: the flag beats the
// environment beats the config block, and an empty value at either of the first two levels is
// not a value — it must never silence a configured hook, because `--postprocess=` with nothing
// after it is a typo and an unset variable exported as "" is a shell accident.
//
// Both resolvers are checked against every case. They are two entry points to one rule (a build
// has a parsed config; `--postprocess` in a half-configured directory does not), and a rule
// implemented twice is a rule that eventually disagrees with itself.
func TestPostprocessPrecedence(t *testing.T) {
	cases := []struct {
		name             string
		block, env, flag string
		want             string
	}{
		{name: "nothing configured", want: ""},
		{name: "the config block alone", block: "cfg", want: "cfg"},
		{name: "the environment alone", env: "env", want: "env"},
		{name: "the flag alone", flag: "flag", want: "flag"},
		{name: "the environment beats the block", block: "cfg", env: "env", want: "env"},
		{name: "the flag beats the block", block: "cfg", flag: "flag", want: "flag"},
		{name: "the flag beats the environment", env: "env", flag: "flag", want: "flag"},
		{name: "the flag beats both", block: "cfg", env: "env", flag: "flag", want: "flag"},
		{name: "an empty flag does not silence the block", block: "cfg", flag: "", want: "cfg"},
		{name: "a blank flag does not silence the block", block: "cfg", flag: "   ", want: "cfg"},
		{name: "an empty variable does not silence the block", block: "cfg", env: "", want: "cfg"},
		{name: "a blank variable does not silence the block", block: "cfg", env: "  ", want: "cfg"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(build.PostprocessEnvVar, tc.env)

			// dir travels with the block and is never overridden: it says where this project's
			// tools run, which does not stop being true because someone changed the command.
			block := &config.PostprocessConfig{Command: tc.block, Dir: "tools"}
			if tc.block == "" {
				block = nil
			}
			root := newHookFixture(t, block)

			loaded, err := build.LoadPostprocess(root, tc.flag)
			if err != nil {
				t.Fatalf("LoadPostprocess: %v", err)
			}
			parsed, err := config.ParseBytes(readHookFile(t, filepath.Join(root, config.Filename)))
			if err != nil {
				t.Fatalf("ParseBytes: %v", err)
			}
			forCfg := build.PostprocessFor(parsed, tc.flag)

			wantDir := ""
			if block != nil {
				wantDir = block.Dir
			}
			for _, r := range []struct {
				name string
				got  build.Postprocess
			}{{"LoadPostprocess", loaded}, {"PostprocessFor", forCfg}} {
				if strings.TrimSpace(r.got.Command) != tc.want {
					t.Errorf("%s resolved %q, want %q", r.name, r.got.Command, tc.want)
				}
				if r.got.Configured() != (tc.want != "") {
					t.Errorf("%s: Configured() is %v for command %q", r.name, r.got.Configured(), r.got.Command)
				}
				if r.got.Dir != wantDir {
					t.Errorf("%s resolved dir %q, want %q", r.name, r.got.Dir, wantDir)
				}
			}
		})
	}
}

/* ---- the file-reading entry point ---------------------------------------- */

// TestLoadPostprocessWithoutAConfigFile — `--postprocess` has to work in a directory that has no
// config at all, which is the whole reason this entry point reads the file itself instead of
// going through config.Load.
func TestLoadPostprocessWithoutAConfigFile(t *testing.T) {
	t.Setenv(build.PostprocessEnvVar, "")
	hook, err := build.LoadPostprocess(t.TempDir(), "echo hi")
	if err != nil {
		t.Fatalf("a missing config file is not an error here: %v", err)
	}
	if hook.Command != "echo hi" {
		t.Errorf("the flag was lost: %+v", hook)
	}
}

// TestLoadPostprocessValidatesTheBlock — the lenient reader ignores every key that belongs to
// config.Load, but it must not become the path where a broken block runs anyway.
func TestLoadPostprocessValidatesTheBlock(t *testing.T) {
	t.Setenv(build.PostprocessEnvVar, "")
	root := newHookFixture(t, &config.PostprocessConfig{Dir: "tools"}) // dir, no command: nothing would run
	_, err := build.LoadPostprocess(root, "")
	if err == nil {
		t.Fatal("a block that could never run must be reported, not ignored")
	}
	if !strings.Contains(err.Error(), "postprocess.command") {
		t.Errorf("the message must name the field to fix, got: %v", err)
	}
}

func readHookFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
