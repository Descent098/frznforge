// Port of tests/unit/scaffold.test.ts.
//
// Two things here are worth more than the rest of the file: the generated
// frznforge.config.jsonc is parsed with the REAL config loader, and the generated markdown with
// the REAL frontmatter parser. Copies of either would pass forever while the scaffold rotted.
//
// Nothing here touches the network, and every write happens under t.TempDir().
package scaffold

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"frznforge/internal/config"
	"frznforge/internal/frontmatter"
)

var expectedTree = []string{
	".gitignore",
	"README.md",
	"content/notes/welcome.md",
	"content/orgs/example-org.md.example",
	"content/profile.md",
	"frznforge.config.jsonc",
}

/* ---- helpers ------------------------------------------------------------- */

// listTree is every file under dir, POSIX-separated and relative to it, sorted.
func listTree(t *testing.T, dir string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		found = append(found, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	sort.Strings(found)
	return found
}

func fileNamed(t *testing.T, path string) File {
	t.Helper()
	for _, f := range Files() {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("no scaffolded file named %q", path)
	return File{}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

/* ---- the file set -------------------------------------------------------- */

func TestFilesIsTheTreeANewSiteStartsWith(t *testing.T) {
	var paths []string
	for _, f := range Files() {
		paths = append(paths, f.Path)
	}
	if got := sorted(paths); !equalStrings(got, expectedTree) {
		t.Errorf("scaffold tree\n got: %v\nwant: %v", got, expectedTree)
	}
}

func TestFilesAreLFTextWithUniquePaths(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range Files() {
		if seen[f.Path] {
			t.Errorf("%s is listed twice", f.Path)
		}
		seen[f.Path] = true
		if strings.Contains(f.Contents, "\r") {
			t.Errorf("%s has CR characters", f.Path)
		}
		if !strings.HasSuffix(f.Contents, "\n") {
			t.Errorf("%s has no trailing newline", f.Path)
		}
		if f.Purpose == "" {
			t.Errorf("%s has no purpose line", f.Path)
		}
	}
}

func TestGitignoreCoversEverythingTheBuildRegenerates(t *testing.T) {
	body := fileNamed(t, ".gitignore").Contents
	for _, entry := range []string{"/data/", "/dist/", "/.frznforge-cache/", ".env"} {
		if !strings.Contains(body, entry) {
			t.Errorf(".gitignore does not ignore %s", entry)
		}
	}
}

func TestReadmeIsTheOwnersOwn(t *testing.T) {
	body := fileNamed(t, "README.md").Contents
	// The three commands of the Go engine — no npm, no install step.
	for _, command := range []string{"frznforge ingest", "frznforge build", "frznforge dev"} {
		if !strings.Contains(body, command) {
			t.Errorf("README does not mention %q", command)
		}
	}
	if strings.Contains(body, "npm ") {
		t.Error("README still tells the user to run npm; 0.4.0's engine is one binary")
	}
	// Their site's README, not this project's: it must name the files they edit.
	for _, path := range []string{"frznforge.config.jsonc", "content/profile.md"} {
		if !strings.Contains(body, path) {
			t.Errorf("README does not name %s", path)
		}
	}
}

/* ---- the generated config ------------------------------------------------ */

func TestGeneratedConfigParsesUnderTheRealLoader(t *testing.T) {
	source := fileNamed(t, "frznforge.config.jsonc").Contents
	cfg, err := config.ParseBytes([]byte(source))
	if err != nil {
		t.Fatalf("the scaffolded config does not load: %v", err)
	}
	if len(cfg.Repos) != 0 {
		t.Errorf("repos should start empty (every example is commented out), got %d", len(cfg.Repos))
	}
	if cfg.Theme.Palette != "hearth" {
		t.Errorf("theme.palette = %q, want hearth", cfg.Theme.Palette)
	}
	if cfg.Ingest.OutDir != "./data" {
		t.Errorf("ingest.outDir = %q, want ./data", cfg.Ingest.OutDir)
	}
	// Determinism is the default a new site gets: mtimes would end byte-identical rebuilds.
	if cfg.Notes.UseMtime {
		t.Error("notes.useMtime should default to false")
	}
	// The scaffold ships a notes folder, so the site has opted in and must not warn about it.
	if !cfg.NotesConfigured() {
		t.Error("the generated config should declare a notes block")
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`).MatchString(cfg.Owner.Handle) {
		t.Errorf("owner.handle %q is not a slug", cfg.Owner.Handle)
	}
}

func TestGeneratedConfigCommentsOutOneExampleOfEverySourceType(t *testing.T) {
	source := fileNamed(t, "frznforge.config.jsonc").Contents
	for _, sourceType := range []string{"local", "github", "gitlab", "gitea", "forgejo"} {
		re := regexp.MustCompile(`(?m)^\s*//.*"type": "` + sourceType + `"`)
		if !re.MatchString(source) {
			t.Errorf("no commented %s example in the generated config", sourceType)
		}
	}
}

func TestGeneratedConfigNeverCarriesAToken(t *testing.T) {
	// Tokens are an environment concern; a scaffold that suggested otherwise would teach the
	// wrong habit on day one.
	source := fileNamed(t, "frznforge.config.jsonc").Contents
	if regexp.MustCompile(`(?i)"?token"?\s*:`).MatchString(source) {
		t.Error("the generated config has a token field in it")
	}
	if !strings.Contains(source, "read from the environment only") {
		t.Error("the generated config does not say where tokens come from")
	}
}

/* ---- the generated markdown ---------------------------------------------- */

func TestGeneratedProfileFrontmatterParses(t *testing.T) {
	fm := frontmatter.Parse(fileNamed(t, "content/profile.md").Contents)
	if fm.Data["bio"].Str == "" {
		t.Error("profile.md has no bio")
	}
	if len(fm.Data["sites"].List) == 0 {
		t.Error("profile.md lists no sites")
	}
	for _, key := range []string{"pinned", "identities"} {
		v, ok := fm.Data[key]
		if !ok || !v.IsList {
			t.Errorf("profile.md %s should parse as an (empty) list, got %+v", key, v)
		}
		if len(v.List) != 0 {
			t.Errorf("profile.md %s should start empty, got %v", key, v.List)
		}
	}
	// The body is the half a new owner is meant to rewrite, so it has to be there.
	if !strings.Contains(fm.Body, "# Hi, I") {
		t.Error("profile.md has no body to rewrite")
	}
}

func TestGeneratedOrgExampleIsFrontmatterFirstAndInert(t *testing.T) {
	source := fileNamed(t, "content/orgs/example-org.md.example").Contents
	// Frontmatter first, or it would not be frontmatter at all once the file is renamed — the
	// "how to use this" block therefore lives in the body.
	if !strings.HasPrefix(source, "---\n") {
		t.Error("the org example does not open with its frontmatter")
	}
	fm := frontmatter.Parse(source)
	if fm.Data["description"].Str == "" {
		t.Error("the org example has no description")
	}
	if len(fm.Maps["links"]) == 0 {
		t.Error("the org example has no links map")
	}
	// `.md.example`, so the orgs loader (which reads *.md) cannot pick it up and a fresh site
	// ships no organization it never asked for.
	if filepath.Ext("example-org.md.example") == ".md" {
		t.Error("the org example would be loaded as a real org page")
	}
}

func TestGeneratedNoteHasTheFrontmatterIngestReads(t *testing.T) {
	fm := frontmatter.Parse(fileNamed(t, "content/notes/welcome.md").Contents)
	if fm.Data["title"].Str == "" || fm.Data["description"].Str == "" {
		t.Error("the example note has no title or description")
	}
	// Fixed, not a clock: two builds of the same content stay identical.
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`).MatchString(fm.Data["date"].Str) {
		t.Errorf("the example note's date is %q, want YYYY-MM-DD", fm.Data["date"].Str)
	}
	if got := fm.Data["tags"].List; !equalStrings(got, []string{"frznforge"}) {
		t.Errorf("the example note's tags = %v", got)
	}
}

/* ---- writing ------------------------------------------------------------- */

func TestScaffoldCreatesTheDirectoryAndWritesTheTree(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fresh")
	result, err := Scaffold(Options{Dir: dir})
	if err != nil {
		t.Fatalf("scaffold: %v", err)
	}
	if !result.CreatedDir || result.DryRun {
		t.Errorf("createdDir=%v dryRun=%v", result.CreatedDir, result.DryRun)
	}
	if got := sorted(result.Written); !equalStrings(got, expectedTree) {
		t.Errorf("written = %v", got)
	}
	if len(result.Kept) != 0 {
		t.Errorf("kept = %v, want none", result.Kept)
	}
	if got := listTree(t, dir); !equalStrings(got, expectedTree) {
		t.Errorf("on disk = %v", got)
	}
	// What is on disk is byte-for-byte what the pure file set said it would be.
	for _, f := range Files() {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(f.Path)))
		if err != nil {
			t.Fatalf("read %s: %v", f.Path, err)
		}
		if string(got) != f.Contents {
			t.Errorf("%s on disk differs from Files()", f.Path)
		}
	}
}

func TestScaffoldWritesIntoAnExistingEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	result, err := Scaffold(Options{Dir: dir})
	if err != nil {
		t.Fatalf("scaffold: %v", err)
	}
	if result.CreatedDir {
		t.Error("createdDir should be false for a directory that was already there")
	}
	if got := sorted(result.Written); !equalStrings(got, expectedTree) {
		t.Errorf("written = %v", got)
	}
}

func TestScaffoldTreatsAGitOnlyDirectoryAsEmpty(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "git-init")
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := Scaffold(Options{Dir: dir})
	if err != nil {
		t.Fatalf("`git init` then scaffold should work: %v", err)
	}
	if len(result.Kept) != 0 {
		t.Errorf("kept = %v", result.Kept)
	}
}

func TestScaffoldRefusesANonEmptyDirectoryWithoutForce(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Scaffold(Options{Dir: dir})
	var refusal *Error
	if err == nil || !asError(err, &refusal) {
		t.Fatalf("want a *scaffold.Error, got %v", err)
	}
	if !strings.Contains(err.Error(), "not empty") || !strings.Contains(err.Error(), "--force") {
		t.Errorf("refusal does not say what to do: %v", err)
	}
	// The refusal is total: not one file was written before it gave up.
	if got := listTree(t, dir); !equalStrings(got, []string{"notes.txt"}) {
		t.Errorf("files were written anyway: %v", got)
	}
}

func TestScaffoldRefusesATargetThatIsNotADirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Scaffold(Options{Dir: file}); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("want a not-a-directory refusal, got %v", err)
	}
}

func TestForceWritesIntoANonEmptyDirectoryWithoutTouchingIt(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "notes.txt"), "mine\n")
	mustWrite(t, filepath.Join(dir, "README.md"), "# my own readme\n")

	result, err := Scaffold(Options{Dir: dir, Force: true})
	if err != nil {
		t.Fatalf("scaffold --force: %v", err)
	}
	if !equalStrings(result.Kept, []string{"README.md"}) {
		t.Errorf("kept = %v, want [README.md]", result.Kept)
	}
	var want []string
	for _, p := range expectedTree {
		if p != "README.md" {
			want = append(want, p)
		}
	}
	if got := sorted(result.Written); !equalStrings(got, want) {
		t.Errorf("written = %v", got)
	}
	if got := readFile(t, filepath.Join(dir, "README.md")); got != "# my own readme\n" {
		t.Errorf("README.md was overwritten: %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "notes.txt")); got != "mine\n" {
		t.Errorf("notes.txt was touched: %q", got)
	}
}

func TestDryRunWritesNothingAtAll(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "never")
	result, err := Scaffold(Options{Dir: dir, DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if len(result.Written) != 0 {
		t.Errorf("written = %v on a dry run", result.Written)
	}
	if got := sorted(result.Planned); !equalStrings(got, expectedTree) {
		t.Errorf("planned = %v", got)
	}
	if !result.CreatedDir {
		t.Error("createdDir should be true — the directory would be created")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("a dry run created the target directory")
	}
}

func TestDryRunStillReportsWhatItWouldLeaveAlone(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, ".gitignore"), "dist\n")

	result, err := Scaffold(Options{Dir: dir, Force: true, DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !equalStrings(result.Kept, []string{".gitignore"}) {
		t.Errorf("kept = %v", result.Kept)
	}
	for _, p := range result.Planned {
		if p == ".gitignore" {
			t.Error(".gitignore is both kept and planned")
		}
	}
	if got := listTree(t, dir); !equalStrings(got, []string{".gitignore"}) {
		t.Errorf("the dry run wrote something: %v", got)
	}
}

func TestScaffoldIsSafeToRunTwice(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "twice")
	if _, err := Scaffold(Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	before := map[string]string{}
	for _, p := range listTree(t, dir) {
		before[p] = readFile(t, filepath.Join(dir, filepath.FromSlash(p)))
	}
	// Edits survive a re-run — the whole point of never overwriting.
	edited := filepath.Join(dir, "content", "profile.md")
	mustWrite(t, edited, "---\nbio: mine\n---\n")

	second, err := Scaffold(Options{Dir: dir, Force: true})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(second.Written) != 0 {
		t.Errorf("the second run wrote %v", second.Written)
	}
	if got := sorted(second.Kept); !equalStrings(got, expectedTree) {
		t.Errorf("kept = %v", got)
	}
	if got := readFile(t, edited); got != "---\nbio: mine\n---\n" {
		t.Errorf("the edited profile was overwritten: %q", got)
	}
	for p, contents := range before {
		if p == "content/profile.md" {
			continue
		}
		if got := readFile(t, filepath.Join(dir, filepath.FromSlash(p))); got != contents {
			t.Errorf("%s changed on the second run", p)
		}
	}
}

/* ---- reporting ----------------------------------------------------------- */

func TestNextStepsNamesTheExactFilesToEdit(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "steps")
	result, err := Scaffold(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Join(NextSteps(result, tmp), "\n")
	for _, want := range []string{
		filepath.Join("steps", "frznforge.config.jsonc"),
		filepath.Join("steps", "content", "profile.md"),
		`"repos": [`,
		"frznforge build",
	} {
		if !strings.Contains(lines, want) {
			t.Errorf("next steps do not mention %q:\n%s", want, lines)
		}
	}
}

func TestNextStepsDropsTheEngineStepWhenTheEngineIsThere(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "in-a-checkout")
	if err := os.MkdirAll(filepath.Join(dir, "web", "css"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "web", "css", "global.css"), "/* pretend engine */\n")

	result, err := Scaffold(Options{Dir: dir, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.EngineReady {
		t.Fatal("web/css/global.css is there; engineReady should be true")
	}
	lines := NextSteps(result, tmp)
	if strings.Contains(strings.Join(lines, "\n"), "Put the frznforge engine") {
		t.Error("the engine step was printed for a directory that already has the engine")
	}
	// Renumbered, not left with a gap.
	if !strings.HasPrefix(lines[1], "  1. Edit ") {
		t.Errorf("first step is %q", lines[1])
	}
}

func TestRunPrintsEveryWrittenPathThenTheNextSteps(t *testing.T) {
	tmp := t.TempDir()
	var out []string
	if _, err := Run(Options{Dir: filepath.Join(tmp, "printed"), Cwd: tmp}, func(l string) { out = append(out, l) }); err != nil {
		t.Fatal(err)
	}
	text := strings.Join(out, "\n")
	for _, f := range Files() {
		if !strings.Contains(text, "+ "+f.Path) {
			t.Errorf("output does not report writing %s", f.Path)
		}
	}
	if !strings.Contains(text, "Next steps") {
		t.Error("output has no next steps")
	}
}

func TestADryRunSaysSoBeforeItSaysAnythingElse(t *testing.T) {
	tmp := t.TempDir()
	var out []string
	if _, err := Run(Options{Dir: filepath.Join(tmp, "dry"), DryRun: true, Cwd: tmp}, func(l string) { out = append(out, l) }); err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 || !strings.HasPrefix(out[0], "Dry run") {
		t.Fatalf("first line is %q", out)
	}
	if !strings.Contains(strings.Join(out, "\n"), "Re-run without --dry-run") {
		t.Error("a dry run does not say how to make it real")
	}
}

/* ---- the command --------------------------------------------------------- */

func TestCommandScaffoldsAndReportsSuccess(t *testing.T) {
	tmp := t.TempDir()
	var out []string
	if err := Command([]string{"my-site"}, tmp, func(l string) { out = append(out, l) }); err != nil {
		t.Fatalf("new my-site: %v", err)
	}
	if got := listTree(t, filepath.Join(tmp, "my-site")); !equalStrings(got, expectedTree) {
		t.Errorf("tree = %v", got)
	}
}

func TestCommandResolvesTheDirectoryAgainstTheCallersCwd(t *testing.T) {
	tmp := t.TempDir()
	if err := Command([]string{"./nested/site"}, tmp, func(string) {}); err != nil {
		t.Fatalf("new ./nested/site: %v", err)
	}
	if got := listTree(t, filepath.Join(tmp, "nested", "site")); !equalStrings(got, expectedTree) {
		t.Errorf("tree = %v", got)
	}
}

func TestCommandAcceptsDryRunAndForce(t *testing.T) {
	tmp := t.TempDir()
	if err := Command([]string{"peek", "--dry-run"}, tmp, func(string) {}); err != nil {
		t.Fatalf("new peek --dry-run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "peek")); !os.IsNotExist(err) {
		t.Error("--dry-run created the directory")
	}

	dir := filepath.Join(tmp, "occupied")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "mine.txt"), "x")

	err := Command([]string{"occupied"}, tmp, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Errorf("want a --force refusal, got %v", err)
	}
	if err := Command([]string{"occupied", "--force"}, tmp, func(string) {}); err != nil {
		t.Fatalf("new occupied --force: %v", err)
	}
	if got := listTree(t, dir); !equalStrings(got, sorted(append(append([]string{}, expectedTree...), "mine.txt"))) {
		t.Errorf("tree = %v", got)
	}
}

func TestCommandRejectsInitFlagsInsteadOfIgnoringThem(t *testing.T) {
	tmp := t.TempDir()
	err := Command([]string{"x", "--print"}, tmp, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "--print is an init option") {
		t.Errorf("want a message naming the right command, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(tmp, "x")); !os.IsNotExist(statErr) {
		t.Error("a rejected command line still scaffolded")
	}
}

func TestCommandAsksForADirectoryWhenNoneIsGiven(t *testing.T) {
	err := Command(nil, t.TempDir(), func(string) {})
	if err == nil || !strings.Contains(err.Error(), "new needs a directory") {
		t.Errorf("got %v", err)
	}
}

func TestCommandRejectsAStrayArgument(t *testing.T) {
	err := Command([]string{"one", "two"}, t.TempDir(), func(string) {})
	if err == nil || !strings.Contains(err.Error(), "unexpected argument: two") {
		t.Errorf("got %v", err)
	}
}

func TestCommandDocumentsItselfInHelp(t *testing.T) {
	var out []string
	if err := Command([]string{"--help"}, t.TempDir(), func(l string) { out = append(out, l) }); err != nil {
		t.Fatal(err)
	}
	usage := strings.Join(out, "\n")
	for _, want := range []string{"new <dir>", "--dry-run", "starting-a-site.md"} {
		if !strings.Contains(usage, want) {
			t.Errorf("usage does not mention %q", want)
		}
	}
	// The help has to describe the engine as it is now: a binary plus web/, no npm install.
	if !strings.Contains(usage, "web/") || strings.Contains(usage, "npm") {
		t.Errorf("usage does not name the 0.4.0 engine:\n%s", usage)
	}
}

/* ---- small helpers ------------------------------------------------------- */

func mustWrite(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// asError is errors.As, spelled locally so the test file needs no import for one call.
func asError(err error, target **Error) bool {
	e, ok := err.(*Error)
	if ok {
		*target = e
	}
	return ok
}
