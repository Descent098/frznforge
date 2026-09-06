// Package ingest turns git repositories into the model.ForgeData artifact — the Go port of
// src/lib/ingest/.
//
// The port has one acceptance bar and it is exact: for the same repositories at the same
// commits, this package must produce a byte-identical data/forge.json to the TypeScript
// ingest. Three rules follow from that, and every file here obeys them:
//
//   - **Nothing ordered comes out of a Go map.** Map iteration is randomised, so every slice
//     that reaches the artifact is sorted explicitly, always with a plain `<` on the string
//     (code-point order). Never a locale-aware compare: the TypeScript uses a bare comparator
//     for exactly this reason, and an ICU-dependent order would make the artifact a property
//     of the build machine.
//   - **No clock reaches the artifact.** Every date in the output comes from git. The one
//     time-dependent knob, ingest.maxCommitAgeDays, is anchored to the repo's own newest
//     commit rather than to now (see LoadBranches).
//   - **Only committed objects are read.** Every git invocation here is plumbing over the
//     object database; the working tree and the index are never consulted, so a dirty
//     checkout and a clean one produce the same artifact.
//
// This file is the git CLI wrapper (port of src/lib/ingest/git.ts). Everything else in the
// package sits on it.
package ingest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// GitError is a git process that exited non-zero. It carries the full argument vector so a
// failure can be reproduced by hand from the message alone.
type GitError struct {
	Args   []string
	Code   int
	Stderr string
}

func (e *GitError) Error() string {
	return fmt.Sprintf("git %s failed (exit %d): %s", strings.Join(e.Args, " "), e.Code, strings.TrimSpace(e.Stderr))
}

// GitOptions are the three knobs the extractors need from a git process.
type GitOptions struct {
	// Input is written to git's stdin (cat-file --batch, log --stdin). A write that fails
	// because git exited early is ignored deliberately — see GitRun.
	Input []byte
	// AllowFailure returns the result of a non-zero exit instead of an error, for
	// "does this exist?" queries.
	AllowFailure bool
	// MaxBytes stops reading stdout after this many bytes and kills git; 0 means unlimited.
	// A truncated run always reports success, since the kill is ours.
	MaxBytes int
}

// GitResult is one finished git process.
type GitResult struct {
	Stdout []byte
	Stderr string
	Code   int
}

// gitEnvOverrides is the non-interactive, locale-free environment every git call runs in.
//
// GIT_CONFIG_NOSYSTEM keeps /etc/gitconfig out of the run; GIT_CONFIG_GLOBAL is deliberately
// NOT set here, because it is the seam the test fixtures use to point git at an empty config
// (see internal/ingest/testsupport). LC_ALL/LANG=C pin git's own messages, and GIT_PAGER=cat
// stops git from reaching for a pager when stdout is not a terminal on some platforms.
var gitEnvOverrides = [][2]string{
	{"GIT_TERMINAL_PROMPT", "0"},
	{"GIT_CONFIG_NOSYSTEM", "1"},
	{"GIT_OPTIONAL_LOCKS", "0"},
	{"LC_ALL", "C"},
	{"LANG", "C"},
	{"GIT_PAGER", "cat"},
}

func gitEnv() []string {
	base := os.Environ()
	env := make([]string, 0, len(base)+len(gitEnvOverrides))
	for _, kv := range base {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			env = append(env, kv)
			continue
		}
		name := kv[:eq]
		overridden := false
		for _, o := range gitEnvOverrides {
			if name == o[0] {
				overridden = true
				break
			}
		}
		if !overridden {
			env = append(env, kv)
		}
	}
	for _, o := range gitEnvOverrides {
		env = append(env, o[0]+"="+o[1])
	}
	return env
}

// GitRun runs `git -C <repo> <args…>` and returns its stdout.
//
// Two details are load-bearing and both mirror the TypeScript:
//
//   - stdin is written from our own goroutine so an EPIPE (git exiting before it read the
//     whole request, which cat-file --batch does routinely) is ignored. Letting os/exec copy
//     stdin would surface that EPIPE from Wait and turn a successful run into an error.
//   - a MaxBytes truncation is reported as exit 0, because the kill is ours, not a failure of
//     the command.
func GitRun(ctx context.Context, repo string, args []string, opts GitOptions) (GitResult, error) {
	full := make([]string, 0, len(args)+2)
	full = append(full, "-C", repo)
	full = append(full, args...)

	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = gitEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return GitResult{}, gitStartError(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return GitResult{}, gitStartError(err)
	}
	if err := cmd.Start(); err != nil {
		return GitResult{}, gitStartError(err)
	}

	written := make(chan struct{})
	go func() {
		defer close(written)
		if len(opts.Input) > 0 {
			_, _ = stdin.Write(opts.Input)
		}
		_ = stdin.Close()
	}()

	out, truncated, readErr := readLimited(stdout, opts.MaxBytes)
	if truncated {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	<-written

	if readErr != nil && !truncated {
		return GitResult{}, fmt.Errorf("reading git %s output: %w", strings.Join(full, " "), readErr)
	}

	code := 0
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			code = exitErr.ExitCode()
		} else if !truncated {
			return GitResult{}, gitStartError(waitErr)
		}
	}
	if truncated {
		code = 0
	}

	res := GitResult{Stdout: out, Stderr: stderr.String(), Code: code}
	if truncated || code == 0 || opts.AllowFailure {
		return res, nil
	}
	return res, &GitError{Args: full, Code: code, Stderr: res.Stderr}
}

func gitStartError(err error) error {
	return fmt.Errorf("failed to run git (is it installed and on PATH?): %w", err)
}

// readLimited reads r to EOF, or to max bytes — reporting whether it stopped early.
func readLimited(r io.Reader, max int) (data []byte, truncated bool, err error) {
	var buf bytes.Buffer
	chunk := make([]byte, 64*1024)
	for {
		n, readErr := r.Read(chunk)
		if n > 0 {
			buf.Write(chunk[:n])
			if max > 0 && buf.Len() >= max {
				return buf.Bytes()[:max], true, nil
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return buf.Bytes(), false, nil
			}
			return buf.Bytes(), false, readErr
		}
	}
}

// GitOutput runs git and returns stdout as a string; a non-zero exit is an error.
func GitOutput(ctx context.Context, repo string, args ...string) (string, error) {
	r, err := GitRun(ctx, repo, args, GitOptions{})
	if err != nil {
		return "", err
	}
	return string(r.Stdout), nil
}

// GitOutputBytes runs git and returns stdout verbatim; a non-zero exit is an error.
func GitOutputBytes(ctx context.Context, repo string, args ...string) ([]byte, error) {
	r, err := GitRun(ctx, repo, args, GitOptions{})
	if err != nil {
		return nil, err
	}
	return r.Stdout, nil
}

// GitMaybe runs git and reports ok=false when it exits non-zero, for existence queries.
// The error return is reserved for git not running at all.
func GitMaybe(ctx context.Context, repo string, args ...string) (out string, ok bool, err error) {
	r, err := GitRun(ctx, repo, args, GitOptions{AllowFailure: true})
	if err != nil {
		return "", false, err
	}
	if r.Code != 0 {
		return "", false, nil
	}
	return string(r.Stdout), true, nil
}

// IsGitRepo reports whether absPath is the top level of a work tree or a bare repository.
// A path *inside* a work tree is not a repo — otherwise every subdirectory of a checkout
// would scan as its own repository.
//
// "Not a repository" is a false, not an error: the caller turns it into a repo-not-found
// warning. The error return is for git failing to run at all, which must NOT masquerade as
// "none of your repositories exist".
func IsGitRepo(ctx context.Context, absPath string) (bool, error) {
	st, err := os.Stat(absPath)
	if err != nil || !st.IsDir() {
		return false, nil
	}
	r, err := GitRun(ctx, absPath, []string{"rev-parse", "--is-bare-repository"}, GitOptions{AllowFailure: true})
	if err != nil {
		return false, err
	}
	if r.Code != 0 {
		return false, nil
	}
	if strings.TrimSpace(string(r.Stdout)) == "true" {
		return true, nil
	}
	top, ok, err := GitMaybe(ctx, absPath, "rev-parse", "--show-toplevel")
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	return sameDir(strings.TrimSpace(top), absPath), nil
}

// sameDir compares two directory paths as the filesystem sees them.
//
// os.SameFile is the reliable half — it looks through symlinks, 8.3 short names and the
// forward slashes git reports on Windows, all of which a string compare gets wrong. The
// string compare is kept as a fallback for the case where one side no longer exists.
func sameDir(a, b string) bool {
	sa, errA := os.Stat(a)
	sb, errB := os.Stat(b)
	if errA == nil && errB == nil {
		return os.SameFile(sa, sb)
	}
	norm := func(p string) string {
		r, err := filepath.Abs(p)
		if err != nil {
			r = p
		}
		r = strings.TrimRight(filepath.ToSlash(r), "/")
		if isWindows {
			r = strings.ToLower(r)
		}
		return r
	}
	return norm(a) == norm(b)
}

// SplitNUL splits NUL-separated output into non-empty records.
func SplitNUL(data []byte) []string {
	parts := strings.Split(string(data), "\x00")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// gitDateLayouts are the shapes git's `iso-strict` emits. RFC3339 covers every date git can
// actually produce; the second layout is there for the `+0000` spelling some older builds and
// hand-written objects carry, which Date.parse accepts on the TypeScript side.
var gitDateLayouts = []string{
	time.RFC3339,
	"2006-01-02T15:04:05Z0700",
	"2006-01-02 15:04:05 -0700",
}

// ToISOUTC turns a git `%aI` / `iso-strict` date into the artifact's profile,
// "YYYY-MM-DDTHH:MM:SSZ" — the same normalisation JavaScript's
// `new Date(x).toISOString()` minus milliseconds performs.
func ToISOUTC(value string) (string, error) {
	t := jsTrim(value)
	for _, layout := range gitDateLayouts {
		if parsed, err := time.Parse(layout, t); err == nil {
			return parsed.UTC().Format("2006-01-02T15:04:05Z"), nil
		}
	}
	return "", fmt.Errorf("unparseable git date: %q", value)
}

// StripAngleBrackets turns git's `<x@y>` (%(taggeremail)) into `x@y`.
func StripAngleBrackets(email string) string {
	t := jsTrim(email)
	if len(t) >= 2 && strings.HasPrefix(t, "<") && strings.HasSuffix(t, ">") {
		return t[1 : len(t)-1]
	}
	return t
}

// jsTrim trims exactly what JavaScript's String.prototype.trim() trims.
//
// It is not strings.TrimSpace: Go also strips U+0085 (NEL), which JavaScript keeps, and
// JavaScript also strips U+FEFF (BOM), which Go keeps. Both differences are reachable from
// real commit messages, and both would change artifact bytes.
func jsTrim(s string) string {
	return strings.TrimFunc(s, isJSWhitespace)
}

func isJSWhitespace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0x00a0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// splitJSWhitespace splits on runs of JavaScript whitespace, matching `split(/\s+/)`.
func splitJSWhitespace(s string) []string {
	return strings.FieldsFunc(s, isJSWhitespace)
}

// ReadBlobs reads several blobs in one `cat-file --batch`, returning sha → content for every
// sha that resolved to a blob. Missing or non-blob shas are simply absent, exactly as on the
// TypeScript side — a submodule gitlink that slips into the list must not fail the scan.
func ReadBlobs(ctx context.Context, repo string, shas []string) (map[string][]byte, error) {
	list := dedupeStrings(shas)
	result := make(map[string][]byte, len(list))
	if len(list) == 0 {
		return result, nil
	}
	input := strings.Join(list, "\n") + "\n"
	r, err := GitRun(ctx, repo, []string{"cat-file", "--batch"}, GitOptions{Input: []byte(input)})
	if err != nil {
		return nil, err
	}
	out := r.Stdout
	for pos := 0; pos < len(out); {
		nl := bytes.IndexByte(out[pos:], '\n')
		if nl == -1 {
			break
		}
		header := string(out[pos : pos+nl])
		pos += nl + 1
		parts := strings.Split(header, " ")
		if len(parts) < 3 {
			continue // "<sha> missing"
		}
		sha, objType := parts[0], parts[1]
		size, err := strconv.Atoi(parts[2])
		if err != nil || size < 0 {
			continue
		}
		end := pos + size
		if end > len(out) {
			end = len(out)
		}
		if objType == "blob" {
			content := make([]byte, end-pos)
			copy(content, out[pos:end])
			result[sha] = content
		}
		pos = end + 1 // the object is followed by a single LF
	}
	return result, nil
}

// ReadBlob reads one blob in full.
func ReadBlob(ctx context.Context, repo, sha string) ([]byte, error) {
	return GitOutputBytes(ctx, repo, "cat-file", "blob", sha)
}

// ReadBlobPrefix reads at most maxBytes of a blob, for sniffing an oversized file.
func ReadBlobPrefix(ctx context.Context, repo, sha string, maxBytes int) ([]byte, error) {
	r, err := GitRun(ctx, repo, []string{"cat-file", "blob", sha}, GitOptions{MaxBytes: maxBytes})
	if err != nil {
		return nil, err
	}
	return r.Stdout, nil
}

// LooksBinary applies git's own heuristic: a NUL in the first 8000 bytes.
func LooksBinary(buf []byte) bool {
	n := len(buf)
	if n > 8000 {
		n = 8000
	}
	return bytes.IndexByte(buf[:n], 0) != -1
}

// dedupeStrings keeps the first occurrence of each value, preserving order — the Go
// equivalent of `Array.from(new Set(xs))`.
func dedupeStrings(xs []string) []string {
	seen := make(map[string]bool, len(xs))
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
