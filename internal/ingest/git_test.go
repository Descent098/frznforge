package ingest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"frznforge/internal/ingest/testsupport"
)

func TestToISOUTC(t *testing.T) {
	cases := []struct{ in, want string }{
		{"2024-01-01T00:00:00+00:00", "2024-01-01T00:00:00Z"},
		{"2024-06-30T23:59:59Z", "2024-06-30T23:59:59Z"},
		// An offset is folded into UTC rather than kept — the artifact has exactly one clock.
		{"2024-03-04T10:00:00-05:00", "2024-03-04T15:00:00Z"},
		{"  2024-03-04T10:00:00+02:00\n", "2024-03-04T08:00:00Z"},
	}
	for _, c := range cases {
		got, err := ToISOUTC(c.in)
		if err != nil {
			t.Fatalf("ToISOUTC(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("ToISOUTC(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if _, err := ToISOUTC("not a date"); err == nil {
		t.Error("ToISOUTC accepted a non-date")
	}
}

// jsTrim is not strings.TrimSpace, and the two differ on characters that can reach the
// artifact through commit bodies and tag messages. The runes are built rather than typed so
// this file stays pure ASCII and the intent stays visible.
func TestJSTrimMatchesJavaScript(t *testing.T) {
	nel := string(rune(0x0085))  // NEL: Go trims it, JavaScript does not
	bom := string(rune(0xfeff))  // BOM: JavaScript trims it, Go does not
	nbsp := string(rune(0x00a0)) // both trim this one

	if got := jsTrim(nel + "x" + nel); got != nel+"x"+nel {
		t.Errorf("jsTrim stripped U+0085 (NEL), which JavaScript keeps: %q", got)
	}
	if got := jsTrim(bom + "x" + bom); got != "x" {
		t.Errorf("jsTrim kept U+FEFF (BOM), which JavaScript strips: %q", got)
	}
	if got := jsTrim(nbsp + " \t\n x \n"); got != "x" {
		t.Errorf("jsTrim did not strip a leading U+00A0: %q", got)
	}
}

func TestStripAngleBrackets(t *testing.T) {
	for in, want := range map[string]string{
		"<a@b.example>":   "a@b.example",
		" <a@b.example> ": "a@b.example",
		"a@b.example":     "a@b.example",
		"":                "",
	} {
		if got := StripAngleBrackets(in); got != want {
			t.Errorf("StripAngleBrackets(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLooksBinary(t *testing.T) {
	if LooksBinary([]byte("plain text\n")) {
		t.Error("text classified as binary")
	}
	if !LooksBinary([]byte("head\x00tail")) {
		t.Error("NUL byte not detected")
	}
	// Only the first 8000 bytes are examined — git's own rule.
	late := append(bytesRepeat('a', 8000), 0)
	if LooksBinary(late) {
		t.Error("a NUL past 8000 bytes should not count")
	}
}

func bytesRepeat(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

func TestIsGitRepo(t *testing.T) {
	ctx := context.Background()
	repo := testsupport.Create(t, "", "")
	repo.WriteAndCommit(map[string]string{"a.txt": "a\n", "sub/b.txt": "b\n"}, "first", testsupport.CommitOptions{})

	if ok, err := IsGitRepo(ctx, repo.Dir); err != nil || !ok {
		t.Errorf("work tree root not recognised as a repo (err=%v)", err)
	}
	// A directory INSIDE a work tree is not a repo — otherwise every subdirectory of a
	// checkout would scan as its own repository.
	for _, path := range []string{"sub", "a.txt", "does-not-exist"} {
		if ok, err := IsGitRepo(ctx, filepath.Join(repo.Dir, path)); err != nil || ok {
			t.Errorf("%s was recognised as a repo (err=%v)", path, err)
		}
	}

	bare := testsupport.CreateBare(t, "", "")
	if ok, err := IsGitRepo(ctx, bare.Dir); err != nil || !ok {
		t.Errorf("bare repository not recognised (err=%v)", err)
	}

	plain, err := os.MkdirTemp("", "frznforge-plain-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(plain)
	if ok, err := IsGitRepo(ctx, plain); err != nil || ok {
		t.Errorf("a plain directory was recognised as a repo (err=%v)", err)
	}
}

func TestReadBlobsAndPrefix(t *testing.T) {
	ctx := context.Background()
	repo := testsupport.Create(t, "", "")
	repo.WriteAndCommit(map[string]string{
		"one.txt": "one\n",
		"two.txt": "two\n",
	}, "first", testsupport.CommitOptions{})

	one := repo.Git("rev-parse", "HEAD:one.txt")
	two := repo.Git("rev-parse", "HEAD:two.txt")

	blobs, err := ReadBlobs(ctx, repo.Dir, []string{one, two, one, strings.Repeat("0", 40)})
	if err != nil {
		t.Fatalf("ReadBlobs: %v", err)
	}
	if len(blobs) != 2 {
		t.Fatalf("want 2 blobs (the missing sha is simply absent), got %d", len(blobs))
	}
	if string(blobs[one]) != "one\n" || string(blobs[two]) != "two\n" {
		t.Fatalf("wrong blob contents: %q / %q", blobs[one], blobs[two])
	}

	prefix, err := ReadBlobPrefix(ctx, repo.Dir, one, 2)
	if err != nil {
		t.Fatalf("ReadBlobPrefix: %v", err)
	}
	if string(prefix) != "on" {
		t.Fatalf("ReadBlobPrefix = %q, want %q", prefix, "on")
	}
}

// A non-zero exit is an error with the arguments in it; GitMaybe turns the same run into a
// plain "no".
func TestGitFailureModes(t *testing.T) {
	ctx := context.Background()
	repo := testsupport.Create(t, "", "")
	repo.WriteAndCommit(map[string]string{"a.txt": "a\n"}, "first", testsupport.CommitOptions{})

	if _, err := GitOutput(ctx, repo.Dir, "rev-parse", "--verify", "refs/heads/nope"); err == nil {
		t.Fatal("expected an error for a missing ref")
	} else if !strings.Contains(err.Error(), "rev-parse") {
		t.Errorf("error should name the failing command: %v", err)
	}
	if _, ok, err := GitMaybe(ctx, repo.Dir, "rev-parse", "--verify", "refs/heads/nope"); err != nil || ok {
		t.Errorf("GitMaybe on a missing ref = (ok=%v, err=%v), want (false, nil)", ok, err)
	}
}

func TestSplitNUL(t *testing.T) {
	got := SplitNUL([]byte("a\x00b\x00\x00c\x00"))
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("SplitNUL = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SplitNUL = %q, want %q", got, want)
		}
	}
}
