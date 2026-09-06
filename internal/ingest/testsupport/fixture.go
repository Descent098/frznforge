// Package testsupport builds throwaway git repositories for tests, with fixed identities and
// fixed dates so that object ids — and therefore every snapshot taken of them — are the same
// on every machine and every run. It is the Go port of tests/unit/helpers/fixture-repo.ts, and
// the two produce byte-identical repositories from the same script of calls, which is what
// lets a Go result be compared against a TypeScript one.
//
// Two kinds of isolation matter here and both are easy to lose:
//
//   - The developer's own git config. Signing, hooks, templates, commit.gpgsign, init
//     defaultBranch and core.autocrlf would all change what these repos contain. The package
//     points GIT_CONFIG_GLOBAL at an empty file and sets GIT_CONFIG_NOSYSTEM once per process.
//   - The clock. Every commit and tag takes an explicit author and committer date, defaulting
//     to a fixed epoch plus a per-repo sequence number, so a repo built twice has the same
//     shas.
package testsupport

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// Epoch is 2024-01-01T00:00:00Z, the base for every generated date.
var Epoch = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

// At is the ISO date n seconds after Epoch — the same value tests/unit/helpers' `at(n)` gives.
func At(n int) string {
	return Epoch.Add(time.Duration(n) * time.Second).Format("2006-01-02T15:04:05Z")
}

var isolateOnce sync.Once

// isolateGit points git at an empty global config for the rest of the process.
//
// The temp file is deliberately not cleaned up: it has to outlive every individual test (Go
// runs them in one process, and a later test would otherwise inherit the developer's real
// config), and there is no process-exit hook to hang the removal on. It is one empty file in
// the system temp directory — the TypeScript helper leaks the same one.
func isolateGit(t testing.TB) {
	isolateOnce.Do(func() {
		dir, err := os.MkdirTemp("", "frznforge-gitconfig-")
		if err != nil {
			t.Fatalf("create isolated git config dir: %v", err)
		}
		path := filepath.Join(dir, "gitconfig")
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatalf("write isolated git config: %v", err)
		}
		for k, v := range map[string]string{
			"GIT_CONFIG_GLOBAL":   path,
			"GIT_CONFIG_NOSYSTEM": "1",
			"GIT_TERMINAL_PROMPT": "0",
		} {
			if err := os.Setenv(k, v); err != nil {
				t.Fatalf("set %s: %v", k, err)
			}
		}
	})
}

// Repo is a fixture repository on disk.
type Repo struct {
	t testing.TB
	// Dir is the repository directory — the path to hand to a scanner.
	Dir string
	// seq numbers the generated dates, so consecutive commits are a minute apart.
	seq int
}

// Create makes a work-tree repository under a fresh temp directory, removed when the test
// ends. Empty name or branch mean "repo" and "main".
func Create(t testing.TB, name, branch string) *Repo {
	r := newRepo(t, name, "repo", branch)
	r.Git("init", "-q", "-b", orDefault(branch, "main"))
	r.Git("config", "user.name", "Test User")
	r.Git("config", "user.email", "test@example.com")
	r.Git("config", "commit.gpgsign", "false")
	r.Git("config", "tag.gpgsign", "false")
	// Without this, a Windows checkout rewrites line endings on the way into the index and the
	// fixture's blobs — and so its shas — stop matching the ones built on Linux.
	r.Git("config", "core.autocrlf", "false")
	return r
}

// CreateBare makes a bare repository (no work tree). Empty name or branch mean "repo.git" and
// "main".
func CreateBare(t testing.TB, name, branch string) *Repo {
	r := newRepo(t, name, "repo.git", branch)
	r.Git("init", "-q", "--bare", "-b", orDefault(branch, "main"))
	return r
}

func newRepo(t testing.TB, name, fallbackName, branch string) *Repo {
	t.Helper()
	isolateGit(t)
	// Not t.TempDir(): its cleanup is a plain RemoveAll, and git writes loose objects
	// read-only, which RemoveAll refuses to delete on Windows. forceRemoveAll clears the bit
	// first — the same reason the TypeScript helper passes `force` and `maxRetries`.
	parent, err := os.MkdirTemp("", "frznforge-fixture-")
	if err != nil {
		t.Fatalf("create fixture parent dir: %v", err)
	}
	t.Cleanup(func() { forceRemoveAll(parent) })
	dir := filepath.Join(parent, orDefault(name, fallbackName))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create fixture dir: %v", err)
	}
	return &Repo{t: t, Dir: dir}
}

// forceRemoveAll deletes a tree that contains read-only files, which every git repository
// does. Failures are ignored: a leftover temp directory is not worth failing a green test for.
func forceRemoveAll(root string) {
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		_ = os.Chmod(path, 0o700)
		return nil
	})
	_ = os.RemoveAll(root)
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// Git runs git in the fixture and returns trimmed stdout, failing the test on error.
func (r *Repo) Git(args ...string) string {
	return r.GitWith(nil, args...)
}

// GitWith runs git with extra environment variables — the seam the date and author overrides
// go through, since git reads those from the environment rather than from flags.
func (r *Repo) GitWith(env map[string]string, args ...string) string {
	r.t.Helper()
	full := append([]string{"-C", r.Dir}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	for _, k := range sortedKeys(env) {
		cmd.Env = append(cmd.Env, k+"="+env[k])
	}
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		r.t.Fatalf("git %s failed: %v\n%s", strings.Join(full, " "), err, stderr)
	}
	return strings.TrimSpace(string(out))
}

// Write puts files into the work tree (no git operations). Parent directories are created.
func (r *Repo) Write(files map[string]string) {
	r.t.Helper()
	raw := make(map[string][]byte, len(files))
	for path, content := range files {
		raw[path] = []byte(content)
	}
	r.WriteBytes(raw)
}

// WriteBytes is Write for content that is not text — a binary fixture, say.
func (r *Repo) WriteBytes(files map[string][]byte) {
	r.t.Helper()
	for _, rel := range sortedByteKeys(files) {
		abs := filepath.Join(r.Dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			r.t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(abs, files[rel], 0o644); err != nil {
			r.t.Fatalf("write %s: %v", rel, err)
		}
	}
}

// Add stages the given paths, or everything when none are given.
func (r *Repo) Add(paths ...string) {
	args := []string{"add", "-A"}
	if len(paths) > 0 {
		args = append(args, "--")
		args = append(args, paths...)
	}
	r.Git(args...)
}

// Rm removes tracked paths.
func (r *Repo) Rm(paths ...string) {
	r.Git(append([]string{"rm", "-r", "-q", "--"}, paths...)...)
}

// CommitOptions are the per-commit overrides. The zero value gives the next generated date and
// the fixture's default author.
type CommitOptions struct {
	// Date is the author date; empty means the next generated one.
	Date string
	// CommitterDate defaults to Date, which is what a plain `git commit` produces. Set it to
	// model a rebase or a cherry-pick: an old patch replayed onto a new tip keeps its author
	// date and gets a fresh committer date.
	CommitterDate string
	// AuthorName and AuthorEmail override the fixture's default identity for this commit.
	AuthorName  string
	AuthorEmail string
}

// Commit commits whatever is staged (allowing an empty commit) and returns the new sha.
func (r *Repo) Commit(message string, opts CommitOptions) string {
	r.t.Helper()
	date := opts.Date
	if date == "" {
		date = At(r.seq * 60)
		r.seq++
	}
	committerDate := opts.CommitterDate
	if committerDate == "" {
		committerDate = date
	}
	env := map[string]string{"GIT_AUTHOR_DATE": date, "GIT_COMMITTER_DATE": committerDate}
	if opts.AuthorName != "" {
		env["GIT_AUTHOR_NAME"] = opts.AuthorName
	}
	if opts.AuthorEmail != "" {
		env["GIT_AUTHOR_EMAIL"] = opts.AuthorEmail
	}
	r.GitWith(env, "commit", "-q", "--allow-empty", "--no-verify", "-m", message)
	return r.Git("rev-parse", "HEAD")
}

// WriteAndCommit writes, stages and commits in one call.
func (r *Repo) WriteAndCommit(files map[string]string, message string, opts CommitOptions) string {
	r.t.Helper()
	r.Write(files)
	r.Add()
	return r.Commit(message, opts)
}

// TagOptions selects between a lightweight and an annotated tag.
type TagOptions struct {
	Annotated bool
	// Message is the annotated tag's message; empty means "tag <name>".
	Message string
	// Date is the tagger date; empty means the next generated one.
	Date string
}

// Tag creates a tag at HEAD.
func (r *Repo) Tag(name string, opts TagOptions) {
	r.t.Helper()
	date := opts.Date
	if date == "" {
		date = At(r.seq * 60)
		r.seq++
	}
	env := map[string]string{"GIT_COMMITTER_DATE": date, "GIT_AUTHOR_DATE": date}
	if opts.Annotated {
		message := opts.Message
		if message == "" {
			message = "tag " + name
		}
		r.GitWith(env, "tag", "-a", name, "-m", message)
		return
	}
	r.GitWith(env, "tag", name)
}

// Branch creates a branch, optionally at a given start point.
func (r *Repo) Branch(name, start string) {
	args := []string{"branch", name}
	if start != "" {
		args = append(args, start)
	}
	r.Git(args...)
}

// Checkout switches branches, creating the branch first when create is set.
func (r *Repo) Checkout(name string, create bool) {
	args := []string{"checkout", "-q"}
	if create {
		args = append(args, "-b")
	}
	r.Git(append(args, name)...)
}

// Head is the current HEAD sha.
func (r *Repo) Head() string { return r.Git("rev-parse", "HEAD") }

// Rev resolves any revision to a sha.
func (r *Repo) Rev(ref string) string { return r.Git("rev-parse", ref) }

// SetHead points HEAD at a branch that need not exist yet, without touching the work tree —
// the way to build the unborn-HEAD case the default-branch fallback has to handle.
func (r *Repo) SetHead(branch string) { r.Git("symbolic-ref", "HEAD", "refs/heads/"+branch) }

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedByteKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
