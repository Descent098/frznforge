package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseArgs(t *testing.T) {
	got, err := parseArgs([]string{"--dir=data", "--run=all", "--level=warn", "--grep=beta", "--port=8080", "--web"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if got.Dir != "data" || !got.DirSet || got.Run != AllRuns || got.Level != "warn" || !got.LevelSet ||
		got.Grep != "beta" || got.Port != 8080 || !got.Web {
		t.Errorf("parsed = %+v", got)
	}
	if def, _ := parseArgs(nil); def.Root != "." || def.Port != 0 || def.DirSet {
		t.Errorf("defaults = %+v; --port=0 asks the OS for a free one", def)
	}
}

func TestParseArgsRefusesNonsense(t *testing.T) {
	for _, argv := range [][]string{
		{"--nope"},
		{"--port=nine"},
		{"--port=70000"},
		{"--port=-1"},
		{"--web", "--plain"},
	} {
		if _, err := parseArgs(argv); err == nil {
			t.Errorf("%v was accepted", argv)
		}
	}
}

func TestHelpPrintsTheUsageAndNothingElse(t *testing.T) {
	var out, errOut bytes.Buffer
	a := &app{Args: []string{"--help"}, Stdout: &out, Stderr: &errOut}
	if err := a.run(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "frzndebugger") || !strings.Contains(out.String(), "--web") {
		t.Errorf("usage = %q", out.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q", errOut.String())
	}
}

func TestRunWithoutAConsolePrintsTheListing(t *testing.T) {
	var out bytes.Buffer
	a := &app{Args: []string{"--dir=" + fixtureDir(t)}, Stdout: &out, Stderr: &bytes.Buffer{}}
	if err := a.run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"1 UNFINISHED step", // the reason the program exists, first
		"#3",
		"git.fetch kieran/beta",
		"== timings",
		"build site",
		"ingest.repo kieran/alpha", // fully expanded: a listing has no keyboard to open rows with
		"== log",
		"clone failed",
		"???", // the line that is not a record is shown, not dropped
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the listing is missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "ghs_supersecretvalue") {
		t.Fatal("a credential reached stdout")
	}
}

func TestListingHonoursTheFilterFlags(t *testing.T) {
	var out bytes.Buffer
	a := &app{
		Args:   []string{"--dir=" + fixtureDir(t), "--level=error", "--grep=clone"},
		Stdout: &out, Stderr: &bytes.Buffer{},
	}
	if err := a.run(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "clone failed") {
		t.Errorf("the matching record is missing:\n%s", text)
	}
	if strings.Contains(text, "cache miss") || strings.Contains(text, "git start") {
		t.Errorf("a record outside the filter survived:\n%s", text)
	}
	if !strings.Contains(text, `level>=ERROR`) || !strings.Contains(text, `matching "clone"`) {
		t.Errorf("the listing should say what it filtered by:\n%s", text)
	}
}

func TestListingScopesToARun(t *testing.T) {
	dir := fixtureDir(t)
	var newest, older, all bytes.Buffer
	for _, c := range []struct {
		run string
		out *bytes.Buffer
	}{{"", &newest}, {"20260905T101010Z-100", &older}, {AllRuns, &all}} {
		a := &app{Args: []string{"--dir=" + dir, "--run=" + c.run}, Stdout: c.out, Stderr: &bytes.Buffer{}}
		if err := a.run(); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(newest.String(), "1 UNFINISHED step") {
		t.Error("the default run is the newest, which is the one that failed")
	}
	if !strings.Contains(older.String(), "no unfinished steps") {
		t.Error("the older run finished cleanly and should say so")
	}
	if !strings.Contains(all.String(), "run: all (2 in file)") {
		t.Errorf("--run=all should widen:\n%s", all.String())
	}
	// The hint about widening belongs only on the view that is narrow.
	if strings.Contains(all.String(), "to widen") {
		t.Error("--run=all should not offer to widen further")
	}
	if !strings.Contains(newest.String(), "2 runs in the file") {
		t.Errorf("a single-run view should say there is more in the file:\n%s", newest.String())
	}
}

func TestListingSaysWhenThereIsNothingThere(t *testing.T) {
	var out bytes.Buffer
	a := &app{Args: []string{"--dir=" + t.TempDir()}, Stdout: &out, Stderr: &bytes.Buffer{}}
	if err := a.run(); err != nil {
		t.Fatalf("an empty directory is ordinary, not an error: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "not there, or empty") {
		t.Errorf("the listing should name the files it did not find:\n%s", text)
	}
	if !strings.Contains(text, "no unfinished steps") {
		t.Errorf("nothing recorded is not the same as something wrong:\n%s", text)
	}
}

func TestResolveDirPrefersTheFlagThenTheConfigThenData(t *testing.T) {
	explicit, err := resolveDir(args{Dir: "somewhere", DirSet: true, Root: "."})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(explicit) != "somewhere" || !filepath.IsAbs(explicit) {
		t.Errorf("--dir resolved to %q", explicit)
	}

	// A directory with no config falls back to <root>/data, which is the documented default.
	root := t.TempDir()
	fallback, err := resolveDir(args{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if fallback != filepath.Join(root, "data") {
		t.Errorf("fallback = %q, want %q", fallback, filepath.Join(root, "data"))
	}
}

func TestArmRedactionTakesValuesFromSecretLookingNames(t *testing.T) {
	armRedaction([]string{
		"GITHUB_TOKEN=this-is-a-long-secret-value",
		"PATH=/usr/bin",
		"SHORT_TOKEN=abc", // under the floor: turning "abc" into *** would ruin the file
	})
	dir := t.TempDir()
	write(t, filepath.Join(dir, "frznforge.log"),
		"time=x level=INFO msg=\"cloned with this-is-a-long-secret-value\" p=/usr/bin\n")

	var out bytes.Buffer
	a := &app{Args: []string{"--dir=" + dir}, Stdout: &out, Stderr: &bytes.Buffer{}}
	if err := a.run(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "this-is-a-long-secret-value") {
		t.Fatal("a literal from this process's environment survived into the listing")
	}
	if !strings.Contains(out.String(), "/usr/bin") {
		t.Error("an ordinary environment value must not be redacted")
	}
}
