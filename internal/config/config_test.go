package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// repoJSONC is the migrated config this package is checked against: the file
// `frznforge config migrate` produced from frznforge.config.ts, frozen into testdata.
//
// It used to read ../../frznforge.config.jsonc — the LIVE config of whoever owns the checkout —
// and that made these tests fail the moment the owner edited their own site. They did: adding
// their GitHub account took the file from one repository to 73, and two tests that assert
// "exactly the self-host demo repo" started failing for a reason that had nothing to do with the
// loader.
//
// The frozen copy is also the only correct subject now. testdata/ts-config.json is a dump of what
// the ZOD schema produced, and the thing it must be compared against is the config that existed
// when it was dumped — not a file that changes whenever someone publishes another repository. The
// pair is frozen together or it means nothing.
func repoJSONC(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "migrated-config.jsonc"))
	if err != nil {
		t.Fatalf("read the frozen migrated config: %v", err)
	}
	return raw
}

// TestRepoConfigParses is the smallest claim worth making about the migration: the file the
// converter wrote loads through the reader that will read it from now on.
func TestRepoConfigParses(t *testing.T) {
	cfg, err := ParseBytes(repoJSONC(t))
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	if !cfg.NotesConfigured() {
		t.Error("the config declares a notes block, so notesConfigured must be true")
	}
	if len(cfg.Repos) != 1 || cfg.Repos[0].Slug != "frznforge" {
		t.Errorf("expected the self-host demo repo, got %+v", cfg.Repos)
	}
}

// TestMatchesTypeScriptDefaults is the cross-language contract named in the package comment:
// every value the Go loader produces for this repository's config must equal what the zod
// schema produces for the TypeScript one — the values the file states AND the defaults it does
// not.
//
// testdata/ts-config.json is the zod side, dumped from the real implementation with:
//
//	npx tsx -e "import c from './frznforge.config.ts'; \
//	  import {FrznforgeConfigSchema} from './src/lib/config/schema.ts'; \
//	  console.log(JSON.stringify(FrznforgeConfigSchema.parse(c), null, 2))"
//
// Regenerate it whenever either config or the zod schema changes. It states every default
// explicitly, so running it back through ParseBytes fills the struct from stated values only —
// which is what makes "Go's default equals zod's default" a real comparison rather than a
// tautology.
func TestMatchesTypeScriptDefaults(t *testing.T) {
	tsRaw, err := os.ReadFile(filepath.Join("testdata", "ts-config.json"))
	if err != nil {
		t.Fatalf("read the TypeScript dump: %v", err)
	}
	want, err := ParseBytes(tsRaw)
	if err != nil {
		t.Fatalf("the TypeScript dump does not load: %v", err)
	}
	got, err := ParseBytes(repoJSONC(t))
	if err != nil {
		t.Fatalf("the migrated config does not load: %v", err)
	}

	// unstated marks a setting neither config file writes down: its value can only have come
	// from applyDefaults on the Go side and from zod's .default() on the TypeScript side, which
	// is the half of this test that would otherwise never be exercised.
	checks := []struct {
		name      string
		got, want any
		unstated  bool
	}{
		{name: "site.title", got: got.Site.Title, want: want.Site.Title},
		{name: "site.url", got: got.Site.URL, want: want.Site.URL},
		{name: "site.base", got: got.Site.Base, want: want.Site.Base},
		{name: "owner.name", got: got.Owner.Name, want: want.Owner.Name},
		{name: "owner.handle", got: got.Owner.Handle, want: want.Owner.Handle},
		{name: "owner.profile", got: got.Owner.Profile, want: want.Owner.Profile},
		{name: "owner.avatar", got: got.Owner.Avatar, want: want.Owner.Avatar},
		{name: "theme.palette", got: got.Theme.Palette, want: want.Theme.Palette},
		{name: "theme.heat.hot", got: got.Theme.Heat.Hot, want: want.Theme.Heat.Hot, unstated: true},
		{name: "theme.heat.warm", got: got.Theme.Heat.Warm, want: want.Theme.Heat.Warm, unstated: true},
		{name: "theme.heat.neutral", got: got.Theme.Heat.Neutral, want: want.Theme.Heat.Neutral, unstated: true},
		{name: "theme.heat.cool", got: got.Theme.Heat.Cool, want: want.Theme.Heat.Cool, unstated: true},
		{name: "markdown.mermaid", got: got.Markdown.Mermaid, want: want.Markdown.Mermaid, unstated: true},
		{name: "content.orgs", got: got.Content.Orgs, want: want.Content.Orgs, unstated: true},
		{name: "repos", got: got.Repos, want: want.Repos},
		{name: "notes.dir", got: got.Notes.Dir, want: want.Notes.Dir},
		{name: "notes.useMtime", got: got.Notes.UseMtime, want: want.Notes.UseMtime, unstated: true},
		{name: "notes.maxFileBytes", got: got.Notes.MaxFileBytes, want: want.Notes.MaxFileBytes, unstated: true},
		{name: "organizations", got: got.Organizations, want: want.Organizations},
		{name: "contributors", got: got.Contributors, want: want.Contributors, unstated: true},
		{name: "hosting.sites", got: got.Hosting.Sites, want: want.Hosting.Sites, unstated: true},
		{name: "hosting.maxFileBytes", got: got.Hosting.MaxFileBytes, want: want.Hosting.MaxFileBytes, unstated: true},
		{name: "ingest.outDir", got: got.Ingest.OutDir, want: want.Ingest.OutDir},
		{name: "ingest.maxBlobBytes", got: got.Ingest.MaxBlobBytes, want: want.Ingest.MaxBlobBytes},
		{name: "ingest.maxCommits", got: got.Ingest.MaxCommits, want: want.Ingest.MaxCommits},
		{name: "ingest.maxCommitAgeDays", got: got.Ingest.MaxCommitAgeDays, want: want.Ingest.MaxCommitAgeDays, unstated: true},
		{name: "ingest.concurrency", got: got.Ingest.Concurrency, want: want.Ingest.Concurrency},
		{name: "ingest.tagTrees", got: got.Ingest.TagTrees, want: want.Ingest.TagTrees, unstated: true},
		{name: "ingest.branchTrees", got: string(got.Ingest.BranchTrees), want: string(want.Ingest.BranchTrees), unstated: true},
		{name: "ingest.archives", got: got.Ingest.Archives, want: want.Ingest.Archives, unstated: true},
		{name: "ingest.cacheDir", got: got.Ingest.CacheDir, want: want.Ingest.CacheDir, unstated: true},
		{name: "ingest.fetch", got: got.Ingest.Fetch, want: want.Ingest.Fetch, unstated: true},
		{name: "ingest.failOnDegraded", got: got.Ingest.FailOnDegraded, want: want.Ingest.FailOnDegraded, unstated: true},
		{name: "ingest.reuse.enabled", got: got.Ingest.Reuse.Enabled, want: want.Ingest.Reuse.Enabled, unstated: true},
		{name: "ingest.reuse.maxAgeMinutes", got: got.Ingest.Reuse.MaxAgeMinutes, want: want.Ingest.Reuse.MaxAgeMinutes, unstated: true},
		{name: "ingest.reuse.skipUnchanged", got: got.Ingest.Reuse.SkipUnchanged, want: want.Ingest.Reuse.SkipUnchanged, unstated: true},
		{name: "ingest.reuse.cooldownSeconds", got: got.Ingest.Reuse.CooldownSeconds, want: want.Ingest.Reuse.CooldownSeconds, unstated: true},
		{name: "ingest.insights.enabled", got: got.Ingest.Insights.Enabled, want: want.Ingest.Insights.Enabled, unstated: true},
		{name: "ingest.insights.samples", got: got.Ingest.Insights.Samples, want: want.Ingest.Insights.Samples, unstated: true},
		{name: "ingest.insights.maxBytesPerSample", got: got.Ingest.Insights.MaxBytesPerSample, want: want.Ingest.Insights.MaxBytesPerSample, unstated: true},
		{name: "listing.pageSize", got: got.Listing.PageSize, want: want.Listing.PageSize},
		{name: "notesConfigured", got: got.notesConfigured, want: want.notesConfigured},
	}

	// Comments are stripped first so a setting merely *mentioned* in prose does not count as
	// stated — `useMtime: true` appears in the notes block's documentation, for instance.
	stated := string(StripJSONC(repoJSONC(t)))
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s: Go has %#v, TypeScript has %#v", c.name, c.got, c.want)
		}
		if c.unstated {
			key := c.name[strings.LastIndexByte(c.name, '.')+1:]
			if strings.Contains(stated, `"`+key+`"`) {
				t.Errorf("%s is marked as unstated but %s does write it — move it out of the defaults group", c.name, Filename)
			}
		}
	}

	// postprocess is a 0.4.0 addition with no zod counterpart, so testdata/ts-config.json can
	// never contain it and the backstop below would fail the day this repository configures its
	// own hook. Cleared here rather than left as a trap; postprocess_test.go covers the block.
	got.Postprocess, want.Postprocess = PostprocessConfig{}, PostprocessConfig{}

	// Backstop: catches a field added to Config that the table above forgot.
	if !reflect.DeepEqual(got, want) {
		t.Errorf("a field outside the table differs\n Go: %s\n TS: %s", mustJSON(t, got), mustJSON(t, want))
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestJSONCDialect pins the edge cases the dialect exists for. Each one has bitten a
// hand-written config format somewhere: the URL is the common case, the escaped quote is the
// one that corrupts a file silently, and the trailing comma is the most frequent parse failure.
func TestJSONCDialect(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want func(*testing.T, *Config)
	}{
		{
			name: "a // inside a URL string is not a comment",
			src: `{
  "owner": { "name": "K", "handle": "kieran" },
  "site": { "url": "https://example.com//deep/path" } // but this one is
}`,
			want: func(t *testing.T, c *Config) {
				if c.Site.URL != "https://example.com//deep/path" {
					t.Errorf("URL lost its slashes: %q", c.Site.URL)
				}
			},
		},
		{
			name: "an escaped quote does not end the string early",
			src: `{
  "owner": { "name": "say \" // not a comment", "handle": "kieran" }
}`,
			want: func(t *testing.T, c *Config) {
				if c.Owner.Name != `say " // not a comment` {
					t.Errorf("name is %q", c.Owner.Name)
				}
			},
		},
		{
			name: "a block comment may contain braces, quotes and commas",
			src: `{
  /* disabled for now:
     "hosting": { "sites": [{ "repo": "x" }], },
  */
  "owner": { "name": "K", "handle": "kieran" }
}`,
			want: func(t *testing.T, c *Config) {
				if len(c.Hosting.Sites) != 0 {
					t.Errorf("the commented-out block was read: %+v", c.Hosting.Sites)
				}
			},
		},
		{
			name: "trailing commas are allowed in objects and arrays",
			src: `{
  "owner": { "name": "K", "handle": "kieran", },
  "organizations": [
    { "slug": "acme", "name": "Acme", },
  ],
}`,
			want: func(t *testing.T, c *Config) {
				if len(c.Organizations) != 1 || c.Organizations[0].Slug != "acme" {
					t.Errorf("organizations: %+v", c.Organizations)
				}
			},
		},
		{
			name: "a trailing comma followed only by a comment still parses",
			src: `{
  "owner": { "name": "K", "handle": "kieran" },
  "repos": [
    { "type": "local", "path": "." }, // the last one
  ]
}`,
			want: func(t *testing.T, c *Config) {
				if len(c.Repos) != 1 {
					t.Errorf("repos: %+v", c.Repos)
				}
			},
		},
		{
			name: "a UTF-8 BOM is tolerated",
			src:  "\ufeff{ \"owner\": { \"name\": \"K\", \"handle\": \"kieran\" } }",
			want: func(t *testing.T, c *Config) {
				if c.Owner.Name != "K" {
					t.Errorf("name: %q", c.Owner.Name)
				}
			},
		},
		{
			name: "CRLF line endings survive comment stripping",
			src:  "{\r\n  // a comment\r\n  \"owner\": { \"name\": \"K\", \"handle\": \"kieran\" }\r\n}\r\n",
			want: func(t *testing.T, c *Config) {
				if c.Owner.Handle != "kieran" {
					t.Errorf("handle: %q", c.Owner.Handle)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ParseBytes([]byte(tc.src))
			if err != nil {
				t.Fatalf("ParseBytes: %v", err)
			}
			tc.want(t, cfg)
		})
	}
}

// TestStripJSONCKeepsOffsets — comments are replaced with spaces rather than removed, so the
// byte offset in a decoder error still points into the file the user wrote.
func TestStripJSONCKeepsOffsets(t *testing.T) {
	src := []byte("{\n  // gone\n  \"a\": 1 /* also gone */\n}")
	out := StripJSONC(src)
	if len(out) != len(src) {
		t.Fatalf("stripping changed the length: %d → %d", len(src), len(out))
	}
	if got, want := strings.Count(string(out), "\n"), strings.Count(string(src), "\n"); got != want {
		t.Errorf("newline count changed: %d → %d", want, got)
	}
	var v map[string]int
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("stripped output is not JSON: %v", err)
	}
	if v["a"] != 1 {
		t.Errorf("value lost: %+v", v)
	}
}

func TestBOMHelpers(t *testing.T) {
	withBOM := append([]byte{0xEF, 0xBB, 0xBF}, []byte("{}")...)
	if !HasBOM(withBOM) {
		t.Error("HasBOM missed a BOM")
	}
	if got := string(TrimBOM(withBOM)); got != "{}" {
		t.Errorf("TrimBOM: %q", got)
	}
	if got := string(TrimBOM([]byte("{}"))); got != "{}" {
		t.Errorf("TrimBOM on clean input: %q", got)
	}
}

// TestValidateRejectsAuthoringMistakes — config problems are authoring errors, so they must
// fail the load rather than becoming warnings the way repo-state problems do.
func TestValidateRejectsAuthoringMistakes(t *testing.T) {
	cases := map[string]string{
		"missing owner":        `{"site":{"title":"x"}}`,
		"handle is not a slug": `{"owner":{"name":"K","handle":"Kieran Wood"}}`,
		"unknown palette":      `{"owner":{"name":"K","handle":"k"},"theme":{"palette":"neon"}}`,
		"heat out of order":    `{"owner":{"name":"K","handle":"k"},"theme":{"heat":{"hot":30,"warm":7,"neutral":180,"cool":365}}}`,
		"unknown setting":      `{"owner":{"name":"K","handle":"k"},"ingest":{"maxBlobBytez":10}}`,
		"avatar is a URL":      `{"owner":{"name":"K","handle":"k","avatar":"https://cdn.example.com/me.png"}}`,
		"reserved host slug":   `{"owner":{"name":"K","handle":"k"},"hosting":{"sites":[{"repo":"repos"}]}}`,
		"branchTrees string":   `{"owner":{"name":"K","handle":"k"},"ingest":{"branchTrees":"most"}}`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseBytes([]byte(src)); err == nil {
				t.Error("expected the load to fail")
			}
		})
	}
}

// TestResolveNormalisesOwnerAvatar pins the leading slash on owner.avatar.
//
// It is the one PublicPath the renderer reads straight off the config — organization and
// contributor pictures reach it through the artifact, where ingest has already normalised them —
// so it was also the one that lost the zod schema's transform in the port. The symptom was
// invisible for a whole phase: `src="logo.png"` is correct on the index page and a 404 on every
// deeper one, and under a deploy base it concatenated into `/mysitelogo.png`.
//
// The parsed Config must keep the value the user typed: `frznforge init --web` edits that file
// by key path and writing "/logo.png" back into it would rewrite input nobody changed.
func TestResolveNormalisesOwnerAvatar(t *testing.T) {
	cfg, err := ParseBytes([]byte(`{"owner":{"name":"K","handle":"k","avatar":"images/me.png"}}`))
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	r, err := Resolve(cfg, t.TempDir())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Owner.Avatar != "/images/me.png" {
		t.Errorf("resolved owner.avatar = %q, want %q", r.Owner.Avatar, "/images/me.png")
	}
	if cfg.Owner.Avatar != "images/me.png" {
		t.Errorf("the parsed config was rewritten to %q; the wizard writes that value back", cfg.Owner.Avatar)
	}

	// An unset avatar stays unset, or the sidebar would ask for "/" and lose the initials.
	bare, err := ParseBytes([]byte(`{"owner":{"name":"K","handle":"k"}}`))
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	plain, err := Resolve(bare, t.TempDir())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if plain.Owner.Avatar != "" {
		t.Errorf("resolved owner.avatar = %q, want empty", plain.Owner.Avatar)
	}
}
