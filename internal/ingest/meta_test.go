package ingest

import (
	"context"
	"strings"
	"testing"

	"frznforge/internal/config"
	"frznforge/internal/ingest/testsupport"
)

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"my_Repo Name": "my-repo-name",
		"UPPER":        "upper",
		"--edges--":    "edges",
		"!!!":          "repo",
		"":             "repo",
		"a1":           "a1",
	} {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRepoBasenameDropsDotGit(t *testing.T) {
	if got := RepoBasename("/tmp/parent/thing.git"); got != "thing" {
		t.Errorf("bare repo basename = %q", got)
	}
	// ".git" itself is four characters, so there is nothing to strip.
	if got := RepoBasename("/tmp/parent/.git"); got != ".git" {
		t.Errorf("basename = %q", got)
	}
}

func TestSlugForRejectsAnUnusableSlug(t *testing.T) {
	if _, err := SlugFor("/tmp/x", "Not A Slug"); err == nil {
		t.Error("an explicit slug that is not URL-safe was accepted")
	}
	got, err := SlugFor("/tmp/My Repo", "")
	if err != nil || got != "my-repo" {
		t.Errorf("SlugFor = %q, %v", got, err)
	}
}

// Truncation counts UTF-16 code units, because that is what the artifact's limit counts, and
// it must never cut a surrogate pair in half.
func TestTruncateDescription(t *testing.T) {
	short := "still fine"
	if got := truncateDescription(short); got != short {
		t.Errorf("a short description was altered: %q", got)
	}

	plain := strings.Repeat("a", 400)
	got := truncateDescription(plain)
	if utf16Len(got) != MaxDescription {
		t.Errorf("truncated length = %d units, want %d", utf16Len(got), MaxDescription)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncation did not end with an ellipsis: %q", got[len(got)-8:])
	}

	// The 299th unit — the last one a plain cut would keep — is the HIGH half of a pair, so
	// the cut steps back rather than emitting a lone surrogate.
	emoji := string(rune(0x1f600))
	mid := strings.Repeat("a", 298) + emoji + strings.Repeat("b", 20)
	cut := truncateDescription(mid)
	if strings.Contains(cut, emoji) {
		t.Errorf("the emoji should have been dropped whole, got %q", cut)
	}
	if utf16Len(cut) != MaxDescription-1 {
		t.Errorf("stepped-back length = %d units, want %d", utf16Len(cut), MaxDescription-1)
	}
	for _, r := range cut {
		if r == 0xfffd {
			t.Fatalf("truncation produced a replacement character: %q", cut)
		}
	}
}

func TestReadRepoMetaFile(t *testing.T) {
	ctx := context.Background()
	repo := testsupport.Create(t, "", "")

	// No file at all: no metadata, and — importantly — no warning.
	repo.WriteAndCommit(map[string]string{"a.txt": "a\n"}, "first", testsupport.CommitOptions{})
	res, err := ReadRepoMetaFile(ctx, repo.Dir, repo.Head())
	if err != nil {
		t.Fatal(err)
	}
	if res.Meta != nil || len(res.Warnings) != 0 {
		t.Fatalf("missing meta file produced %+v", res)
	}

	repo.WriteAndCommit(map[string]string{".frznforge.json": `{
		"name": "Nice Name",
		"tags": ["one"],
		"links": { "homepage": "https://example.com/" },
		"unknown": "ignored the way zod strips unknown keys"
	}`}, "meta", testsupport.CommitOptions{})
	res, err = ReadRepoMetaFile(ctx, repo.Dir, repo.Head())
	if err != nil {
		t.Fatal(err)
	}
	if res.Meta == nil || res.Meta.Name == nil || *res.Meta.Name != "Nice Name" {
		t.Fatalf("meta = %+v", res.Meta)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("valid metadata warned: %+v", res.Warnings)
	}

	// A link that is not a URL fails validation, and a failed file contributes nothing.
	repo.WriteAndCommit(map[string]string{".frznforge.json": `{"links": {"homepage": "not a url"}}`},
		"bad link", testsupport.CommitOptions{})
	res, err = ReadRepoMetaFile(ctx, repo.Dir, repo.Head())
	if err != nil {
		t.Fatal(err)
	}
	if res.Meta != nil {
		t.Errorf("invalid metadata was kept: %+v", res.Meta)
	}
	if len(res.Warnings) != 1 || res.Warnings[0].Code != "repo-meta-invalid" {
		t.Fatalf("warnings = %+v", res.Warnings)
	}
}

// Precedence is config overrides > the repo's own file > provider metadata > derived
// defaults, and `links` merges per key at every layer instead of being replaced wholesale.
func TestMergeMetaLayers(t *testing.T) {
	str := func(s string) *string { return &s }

	provider := &config.RepoMetaInput{
		Description: str("from the provider"),
		Links:       &config.RepoLinks{Homepage: str("https://provider.example/")},
	}
	file := &config.RepoMetaInput{
		Name:  str("from the repo"),
		Links: &config.RepoLinks{Issues: str("https://repo.example/issues")},
	}
	overrides := &config.RepoMetaInput{
		Links: &config.RepoLinks{Donations: str("https://donate.example/")},
	}

	meta, err := mergeMeta("directory-name", layerMeta(file, provider), overrides)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Name != "from the repo" {
		t.Errorf("name = %q", meta.Name)
	}
	if meta.Description == nil || *meta.Description != "from the provider" {
		t.Errorf("description = %v", meta.Description)
	}
	if meta.Links.Homepage == nil || meta.Links.Issues == nil || meta.Links.Donations == nil {
		t.Errorf("links lost a layer: %+v", meta.Links)
	}
	if meta.Links.Upstream != nil {
		t.Errorf("upstream appeared from nowhere: %v", *meta.Links.Upstream)
	}
	// Absent everywhere means the artifact's defaults, not zero values from the wrong layer.
	if meta.Template || meta.ReleaseMode != "tags" || len(meta.Tags) != 0 {
		t.Errorf("defaults = %+v", meta)
	}
}

func TestMergeMetaRejectsAnOverLongOverride(t *testing.T) {
	long := strings.Repeat("a", 400)
	_, err := mergeMeta("x", nil, &config.RepoMetaInput{Description: &long})
	if err == nil {
		t.Fatal("an over-long overrides.description was accepted")
	}
	// A config mistake is the author's to fix, so it says so rather than truncating silently.
	if !strings.Contains(err.Error(), "overrides.description") {
		t.Errorf("error should name the setting: %v", err)
	}
}
