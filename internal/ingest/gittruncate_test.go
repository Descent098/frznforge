package ingest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"frznforge/internal/ingest/testsupport"
)

// The truncation path, which deadlocked in shipped code and hung forever.
//
// `ReadBlobPrefix` reads the first maxBytes of a blob to decide whether it is text, then stops.
// Stopping used to mean killing git — and on Windows that is not enough. `git` on the PATH that
// cmd.exe and PowerShell give you is a 46 KB wrapper in Git\cmd that re-execs the real 4 MB
// binary in Git\mingw64\bin, so TerminateProcess ends the wrapper and the real git carries on
// holding the write end of the stderr pipe. os/exec's stderr copier then waits for an EOF that
// can never arrive, and `cmd.Wait()` never returns.
//
// The symptom was a build that printed the repository's name and then sat there, with no error,
// no output and no way to tell what it was doing — on any repository containing a file larger
// than `ingest.maxBlobBytes`, which is to say very nearly all of them. It did not reproduce
// under Git Bash, whose PATH points straight at the real binary, which is exactly why it took
// a goroutine dump to find.
//
// These tests are written with a deadline rather than as plain assertions because the failure
// mode is a HANG: an assertion that never runs never fails, and a test suite that hangs reads
// as a slow machine. Ten seconds is far beyond the ~100 ms this work takes.
func TestGitRunTruncationDoesNotHang(t *testing.T) {
	repo := testsupport.Create(t, "big", "main")
	// Comfortably larger than the limit below, and larger than a pipe buffer, so git genuinely
	// has more to write when we stop reading. A small file would return before the interesting
	// path is reached and the test would pass without testing anything.
	big := strings.Repeat("0123456789abcdef", 96*1024) // 1.5 MiB
	repo.WriteAndCommit(map[string]string{"big.bin": big}, "add a large file", testsupport.CommitOptions{})

	sha := strings.TrimSpace(repo.Git("rev-parse", "HEAD:big.bin"))
	if sha == "" {
		t.Fatal("could not resolve the blob")
	}

	const limit = 64 * 1024
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, err := ReadBlobPrefix(context.Background(), repo.Dir, sha, limit)
		done <- result{data, err}
	}()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("ReadBlobPrefix: %v", got.err)
		}
		if len(got.data) != limit {
			t.Errorf("read %d bytes, want exactly the %d-byte limit", len(got.data), limit)
		}
		if !strings.HasPrefix(string(got.data), "0123456789abcdef") {
			t.Error("the prefix is not the start of the blob")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ReadBlobPrefix did not return within 10s — the truncation path is deadlocked again.\n" +
			"Look at GitRun: stopping the read has to close the pipe, because killing the process\n" +
			"does not reach a re-exec'd git and Wait will block on the copier that outlives it.")
	}
}

// TestGitRunTruncationRepeats is the same claim under repetition.
//
// One oversized blob leaking a git process is survivable; a repository full of them is a build
// that gets slower every file until it stops. Scanning a real repository calls this hundreds of
// times, so a leak that only shows up on the twentieth call is still a shipped hang.
func TestGitRunTruncationRepeats(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns 20 git processes")
	}
	repo := testsupport.Create(t, "many", "main")
	files := map[string]string{}
	for i := range 5 {
		files[string(rune('a'+i))+".bin"] = strings.Repeat("x", 300*1024)
	}
	repo.WriteAndCommit(files, "several large files", testsupport.CommitOptions{})

	shas := make([]string, 0, len(files))
	for name := range files {
		shas = append(shas, strings.TrimSpace(repo.Git("rev-parse", "HEAD:"+name)))
	}

	done := make(chan error, 1)
	go func() {
		for round := 0; round < 4; round++ {
			for _, sha := range shas {
				if _, err := ReadBlobPrefix(context.Background(), repo.Dir, sha, 16*1024); err != nil {
					done <- err
					return
				}
			}
		}
		done <- nil
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ReadBlobPrefix: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("20 truncated blob reads did not finish within 30s — each one is leaving something behind")
	}
}

// TestGitRunTruncationThroughTheWindowsWrapper is the test that would actually have caught it.
//
// The two above pass on a PATH where `git` is the real binary — which is every CI runner, every
// Linux box, and the Git Bash shell this project is usually developed in. They would have passed
// on the broken code too. The bug needed the OTHER git: the 46 KB wrapper in Git\cmd that
// cmd.exe and PowerShell put on the PATH, which re-execs the real binary as a separate process
// that a Kill on the wrapper never reaches.
//
// So this one goes looking for that wrapper and puts it first on the PATH. It skips where the
// wrapper does not exist, which is honest: the failure is specific to a Windows install layout,
// and pretending otherwise would be a test that claims more than it checks.
func TestGitRunTruncationThroughTheWindowsWrapper(t *testing.T) {
	wrapper := findGitWrapperDir(t)
	if wrapper == "" {
		t.Skip("no Git-for-Windows cmd\\git.exe wrapper on this machine")
	}
	t.Setenv("PATH", wrapper+string(os.PathListSeparator)+os.Getenv("PATH"))
	resolved, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("git vanished from PATH: %v", err)
	}
	t.Logf("running through %s", resolved)

	repo := testsupport.Create(t, "wrapped", "main")
	repo.WriteAndCommit(map[string]string{"big.bin": strings.Repeat("y", 900*1024)}, "big", testsupport.CommitOptions{})
	sha := strings.TrimSpace(repo.Git("rev-parse", "HEAD:big.bin"))

	done := make(chan error, 1)
	go func() {
		_, err := ReadBlobPrefix(context.Background(), repo.Dir, sha, 32*1024)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ReadBlobPrefix through the wrapper: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("truncating a blob through the Git-for-Windows wrapper hung — this is the exact\n" +
			"failure that shipped: Kill ends the wrapper, the real git keeps the stderr pipe open,\n" +
			"and os/exec's copier waits for an EOF that never comes.")
	}
}

// findGitWrapperDir returns the directory holding Git for Windows' small git.exe shim, or "".
//
// Identified by size rather than by path alone: the point is to run through a binary that
// re-execs another one, and a one-megabyte git.exe at that path would not be that.
func findGitWrapperDir(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		return ""
	}
	for _, dir := range []string{`C:\Program Files\Git\cmd`, `C:\Program Files (x86)\Git\cmd`} {
		info, err := os.Stat(filepath.Join(dir, "git.exe"))
		if err == nil && info.Size() < 1<<20 {
			return dir
		}
	}
	return ""
}
