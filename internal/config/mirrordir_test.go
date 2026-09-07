package config

import (
	"regexp"
	"strings"
	"testing"
)

// Two remote sources must never share a mirror directory.
//
// Sharing one is not a cosmetic problem. The mirror is the git content, so two sources that
// collide publish whichever of them fetched last — a repository appearing on the site under
// another repository's name, with another repository's history and another repository's files.
// Nothing downstream can detect it, because by then there is only one directory.
//
// The name is deliberately lossy (case folded, everything outside [a-z0-9._-] mapped to '-') so
// that it is a legal Windows filename and a person can recognise it in a cache listing. Lossy
// means collidable, which is why the digest exists — and why the Go port dropping it, while
// keeping a comment that asserted "two sources cannot collide", was worth a test rather than
// another comment.
func TestMirrorDirNamesCannotCollide(t *testing.T) {
	// Each pair sanitises to the same readable string and is a different repository.
	pairs := [][2]RepoSourceConfig{
		{
			// GitLab namespaced paths: a subgroup, and a project literally named with a dash.
			{Type: "gitlab", Host: "https://gitlab.com", Project: "group/sub/proj"},
			{Type: "gitlab", Host: "https://gitlab.com", Project: "group-sub/proj"},
		},
		{
			// Case. Forges differ on whether they fold it; the cache must not decide for them.
			{Type: "gitea", Host: "https://git.example.com", Owner: "Kieran", Repo: "Forge"},
			{Type: "gitea", Host: "https://git.example.com", Owner: "kieran", Repo: "forge"},
		},
		{
			// The separator itself: owner "a", repo "b-c" against owner "a-b", repo "c".
			{Type: "github", Host: "https://api.github.com", Owner: "a", Repo: "b-c"},
			{Type: "github", Host: "https://api.github.com", Owner: "a-b", Repo: "c"},
		},
		{
			// Characters that both sanitise to '-'.
			{Type: "gitea", Host: "https://git.example.com", Owner: "ka", Repo: "my repo"},
			{Type: "gitea", Host: "https://git.example.com", Owner: "ka", Repo: "my+repo"},
		},
		{
			// A long namespaced path truncated to the same readable prefix.
			{Type: "gitlab", Host: "https://gitlab.com", Project: strings.Repeat("x", 60) + "/one"},
			{Type: "gitlab", Host: "https://gitlab.com", Project: strings.Repeat("x", 60) + "/two"},
		},
		{
			// Same identity on two different hosts.
			{Type: "gitea", Host: "https://a.example.com", Owner: "k", Repo: "r"},
			{Type: "gitea", Host: "https://b.example.com", Owner: "k", Repo: "r"},
		},
	}

	for _, p := range pairs {
		a, b := MirrorDirName(p[0]), MirrorDirName(p[1])
		if a == b {
			t.Errorf("these two share the mirror %q, so each would be published with the other's git content:\n  %+v\n  %+v", a, p[0], p[1])
		}
	}
}

// TestMirrorDirNameIsStableAndLegal pins the two properties the digest must not cost.
func TestMirrorDirNameIsStableAndLegal(t *testing.T) {
	src := RepoSourceConfig{Type: "github", Host: "https://api.github.com", Owner: "Descent098", Repo: "frznforge"}

	// Stable: the mirror is reused across runs, so a name that varied would re-clone every build.
	if MirrorDirName(src) != MirrorDirName(src) {
		t.Fatal("MirrorDirName is not deterministic")
	}

	name := MirrorDirName(src)
	if !regexp.MustCompile(`^[a-z0-9._-]+$`).MatchString(name) {
		t.Errorf("%q is not a portable directory name", name)
	}
	// Bounded: the user's cache path sits above this, and Windows still has a path limit.
	if len(name) > 64 {
		t.Errorf("%q is %d characters; the readable half is meant to be capped", name, len(name))
	}
	// Still recognisable in a listing — the digest is a suffix, not a replacement.
	if !strings.HasPrefix(name, "github-api.github.com-descent098-frznforge-") {
		t.Errorf("%q no longer says which repository it holds", name)
	}
}
