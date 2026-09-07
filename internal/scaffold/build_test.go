package scaffold

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// The whole point of the scaffold: a directory it wrote, plus the engine, really builds.
//
// Opt-in (FRZNFORGE_SCAFFOLD_BUILD=1) because it compiles the binary and runs two child
// processes — a few seconds the rest of the unit suite should not pay on every run:
//
//	FRZNFORGE_SCAFFOLD_BUILD=1 go test ./internal/scaffold/ -run ScaffoldedDirectory -v
//
// It is the one test that proves the three claims the scaffold makes at once: the generated
// config loads, the generated note is ingested, and the generated profile reaches a page.
func TestAScaffoldedDirectoryReallyBuilds(t *testing.T) {
	if os.Getenv("FRZNFORGE_SCAFFOLD_BUILD") != "1" {
		t.Skip("set FRZNFORGE_SCAFFOLD_BUILD=1 to compile the engine and build a scaffolded site")
	}

	tmp := t.TempDir()
	dir := filepath.Join(tmp, "real-site")
	if _, err := Scaffold(Options{Dir: dir}); err != nil {
		t.Fatalf("scaffold: %v", err)
	}

	// The engine's two halves: the binary, and web/ + public/ beside the content. Copying them
	// is exactly what the printed next steps tell a user to do, which is the point of doing it
	// here rather than pointing the build at the repository root.
	root := repoRoot(t)
	binary := filepath.Join(tmp, "frznforge")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, "frznforge/cmd/frznforge")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	for _, name := range []string{"web", "public"} {
		if err := os.CopyFS(filepath.Join(dir, name), os.DirFS(filepath.Join(root, name))); err != nil {
			t.Fatalf("copy %s: %v", name, err)
		}
	}

	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(binary, args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("frznforge %v: %v\n%s", args, err, out)
		}
		return string(out)
	}

	if out := run("ingest"); !contains(out, "1 note(s)") {
		t.Errorf("ingest did not report the scaffolded note:\n%s", out)
	}
	var artifact struct {
		Repos    []json.RawMessage `json:"repos"`
		Notes    []json.RawMessage `json:"notes"`
		Warnings []json.RawMessage `json:"warnings"`
	}
	raw, err := os.ReadFile(filepath.Join(dir, "data", "forge.json"))
	if err != nil {
		t.Fatalf("read the artifact: %v", err)
	}
	if err := json.Unmarshal(raw, &artifact); err != nil {
		t.Fatalf("parse the artifact: %v", err)
	}
	if len(artifact.Repos) != 0 || len(artifact.Notes) != 1 || len(artifact.Warnings) != 0 {
		t.Errorf("artifact has %d repos, %d notes, %d warnings; want 0/1/0",
			len(artifact.Repos), len(artifact.Notes), len(artifact.Warnings))
	}

	run("build")
	for _, page := range []string{"index.html", "repos/index.html", "notes/welcome/index.html"} {
		if _, err := os.Stat(filepath.Join(dir, "dist", filepath.FromSlash(page))); err != nil {
			t.Errorf("no %s in dist/: %v", page, err)
		}
	}
	// The profile page really rendered the scaffolded markdown, not a placeholder.
	home, err := os.ReadFile(filepath.Join(dir, "dist", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(home), "Your Name") {
		t.Error("the home page does not carry the scaffolded owner name")
	}
}

// repoRoot walks up from this test's own directory to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
