package wizard

// The whole-config editor, the profile editor and the upload endpoint — the second half of
// tests/unit/web-init.test.ts.
//
// Everything that writes the config file is held to the same rule the splice tests state: after
// a save, every byte outside the edited field is identical. The assertions here therefore
// compare the file on disk against the fixture with one change applied by hand, and only then
// look at what the endpoint answered.

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

/* ------------------------------------------------------------------ /api/config */

func TestConfigReportsTheFilesOwnValuesItsRawSourcesAndTheDefaults(t *testing.T) {
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	body := r.get("/api/config").wantStatus(http.StatusOK, "GET /api/config").json()

	wantEqual(t, body["configPath"], r.config, "configPath")
	wantEqual(t, body["readable"], true, "readable")
	if issues, _ := body["issues"].([]any); len(issues) != 0 {
		t.Errorf("issues = %v for a config that loads", issues)
	}
	wantEqual(t, at(t, body, "current", "site", "title"), "My Forge", "current.site.title")
	wantEqual(t, at(t, body, "current", "owner", "name"), "Kieran Wood", "current.owner.name")
	// Applied by the schema, not written in the file — the page shows defaults as current values.
	wantEqual(t, at(t, body, "current", "theme", "palette"), "hearth", "current.theme.palette")
	wantEqual(t, at(t, body, "current", "ingest", "maxBlobBytes"), float64(524288), "current.ingest.maxBlobBytes")
	wantEqual(t, at(t, body, "defaults", "listing", "pageSize"), float64(50), "defaults.listing.pageSize")

	sites, _ := at(t, body, "current", "hosting", "sites").([]any)
	if len(sites) != 1 {
		t.Fatalf("hosting.sites = %v, want the one the file declares", sites)
	}
	wantEqual(t, sites[0].(map[string]any)["repo"], "frznforge", "hosting.sites[0].repo")
	wantEqual(t, sites[0].(map[string]any)["branch"], "gh-pages", "hosting.sites[0].branch")

	palettes, _ := body["palettes"].([]any)
	if len(palettes) != 2 || palettes[0] != "hearth" || palettes[1] != "frost" {
		t.Errorf("palettes = %v, want the two the schema accepts", palettes)
	}

	// The raw string fields as the FILE wrote them, in array order. Read from the file's own
	// input rather than the parsed config: the parsed one has a per-provider host filled in, and
	// an `expect` carrying a host the file never wrote would fail the very check it exists to pass.
	wantSources(t, body["sources"], []map[string]string{
		{"type": "local", "path": ".", "slug": "frznforge"},
		{"type": "github", "owner": "me", "repo": "old"},
	})

	r.finish("/api/cancel")
}

// wantSources compares the sources list field by field, so a stray `host` shows up as a failure
// rather than passing under a looser check.
func wantSources(t *testing.T, got any, want []map[string]string) {
	t.Helper()
	list, _ := got.([]any)
	if len(list) != len(want) {
		t.Fatalf("sources = %v, want %d entries", got, len(want))
	}
	for i, entry := range list {
		record, _ := entry.(map[string]any)
		if len(record) != len(want[i]) {
			t.Errorf("sources[%d] = %v, want exactly %v", i, record, want[i])
			continue
		}
		for key, value := range want[i] {
			if record[key] != value {
				t.Errorf("sources[%d].%s = %v, want %q", i, key, record[key], value)
			}
		}
	}
}

func TestConfigSaysSoWhenTheFileCannotBeLoaded(t *testing.T) {
	// The page must never be shown invented values for a file it could not read.
	r := startWizard(t, wizardOptions{config: brokenConfig})
	body := r.get("/api/config").wantStatus(http.StatusOK, "GET /api/config").json()
	wantEqual(t, body["readable"], false, "readable")
	wantEqual(t, body["current"], nil, "current")

	issues, _ := body["issues"].([]any)
	if len(issues) < 2 {
		t.Fatalf("issues = %v, want one entry per problem — the page joins them with '; '", issues)
	}
	joined := fmt.Sprint(issues...)
	for _, want := range []string{"owner.name", "owner.handle"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the issues do not mention %s: %v", want, issues)
		}
	}
	r.finish("/api/cancel")
}

/* ------------------------------------------------------------------ /api/config/write */

func TestConfigWriteEditsTheNamedFieldsAndKeepsEveryComment(t *testing.T) {
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	body := r.post("/api/config/write", map[string]any{"operations": []any{
		map[string]any{"op": "set", "path": "site.title", "value": "New Name"},
		map[string]any{"op": "set", "path": "theme.heat.hot", "value": 3},
		map[string]any{"op": "set", "path": "ingest.maxCommits", "value": 200},
	}}).wantStatus(http.StatusOK, "POST /api/config/write").json()
	wantEqual(t, body["changed"], true, "changed")

	// The whole file, byte for byte: one value replaced in place, one absent chain created at the
	// root, and one field appended inside a block that already existed. Nothing else moves — and
	// note where the new comma lands on the outDir line: before the comment, not after it.
	want := fixtureConfig
	want = strings.Replace(want, `"title": "My Forge"`, `"title": "New Name"`, 1)
	want = strings.Replace(want, "  }\n}\n", "  },\n  \"theme\": { \"heat\": { \"hot\": 3 } },\n}\n", 1)
	want = strings.Replace(want,
		`"outDir": "./data" // keep this comment`,
		"\"outDir\": \"./data\", // keep this comment\n    \"maxCommits\": 200,", 1)
	wantFile(t, r.configText(), want, "the config after three settings ops")

	backup, _ := body["backup"].(string)
	raw, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("read the backup: %v", err)
	}
	wantFile(t, string(raw), fixtureConfig, "the backup's contents")

	// The written file still loads, and the next read reflects the change: every handler
	// re-reads the file, so there is no memo to serve the pre-edit config from.
	after := r.get("/api/config").json()
	wantEqual(t, at(t, after, "current", "site", "title"), "New Name", "site.title on the next read")
	wantEqual(t, at(t, after, "current", "theme", "heat", "hot"), float64(3), "theme.heat.hot on the next read")
	wantEqual(t, at(t, after, "current", "ingest", "maxCommits"), float64(200), "ingest.maxCommits on the next read")

	r.finish("/api/done")
}

func TestConfigWriteTakesOneBackupPerFilePerSession(t *testing.T) {
	// However many saves, "the state before the wizard touched anything" stays one file away
	// instead of becoming a breadcrumb trail.
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	for _, title := range []string{"A", "B"} {
		r.post("/api/config/write", map[string]any{"operations": []any{
			map[string]any{"op": "set", "path": "site.title", "value": title},
		}}).wantStatus(http.StatusOK, "save "+title)
	}
	backups := r.backups(r.dir)
	if len(backups) != 1 {
		t.Fatalf("%d backups after two saves, want exactly one", len(backups))
	}
	raw, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	// The PRE-WIZARD file, not the intermediate state the first save left.
	wantFile(t, string(raw), fixtureConfig, "the one backup")
	wantContains(t, r.configText(), `"title": "B" // shown in the sidebar`, "the second save")

	wantEqual(t, r.finish("/api/done")["writes"], float64(2), "writes")
	if !strings.Contains(r.out.String(), "Done — 2 writes") {
		t.Errorf("the summary does not report both writes:\n%s", r.out.String())
	}
}

func TestConfigWriteReportsANoOpWithoutWritingOrBackingUp(t *testing.T) {
	// Saving a form nobody changed must not count as a write, take a backup, or touch the file.
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	body := r.post("/api/config/write", map[string]any{"operations": []any{
		map[string]any{"op": "set", "path": "site.title", "value": "My Forge"},
	}}).wantStatus(http.StatusOK, "a save that changes nothing").json()
	wantEqual(t, body["changed"], false, "changed")
	wantEqual(t, body["backup"], nil, "backup")
	wantFile(t, r.configText(), fixtureConfig, "the config after a no-op save")
	if backups := r.backups(r.dir); len(backups) != 0 {
		t.Errorf("a no-op save took a backup: %v", backups)
	}
	wantEqual(t, r.finish("/api/done")["writes"], float64(0), "writes")
}

func TestConfigWriteRefusesAChangeTheSchemaRefuses(t *testing.T) {
	// hot=400 breaks "strictly ascending" against the default warm=30. The judgement happens on
	// a copy, so the file is never touched at all — not written and rolled back, never written.
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	res := r.post("/api/config/write", map[string]any{"operations": []any{
		map[string]any{"op": "set", "path": "theme.heat.hot", "value": 400},
	}}).wantStatus(http.StatusBadRequest, "heat boundaries out of order")
	if !strings.Contains(res.body, "ascending") {
		t.Errorf("the refusal does not use the schema's own words: %q", res.body)
	}
	wantFile(t, r.configText(), fixtureConfig, "the config after a refused change")
	if backups := r.backups(r.dir); len(backups) != 0 {
		t.Errorf("a refused change took a backup: %v", backups)
	}
	r.finish("/api/cancel")
}

func TestConfigWriteRefusesEveryPathOffTheAllowList(t *testing.T) {
	// The browser proposes; the allow-list disposes. This is the property that keeps "arbitrary
	// JSON from a local web page" from becoming "arbitrary edits to the file this machine builds
	// from".
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	cases := []struct {
		what string
		op   map[string]any
	}{
		{"an indexed path that is not a settable field", map[string]any{"op": "set", "path": "repos.0.type", "value": "github"}},
		{"prototype pollution", map[string]any{"op": "set", "path": "__proto__.polluted", "value": true}},
		{"a constructor key", map[string]any{"op": "set", "path": "constructor", "value": "x"}},
		{"adding to a list the picker owns", map[string]any{"op": "add", "path": "repos", "item": map[string]any{"type": "github"}}},
		{"removing from a list that does not exist", map[string]any{"op": "removeAt", "path": "nope", "index": 0}},
		{"a negative index", map[string]any{"op": "removeAt", "path": "organizations", "index": -1}},
		{"an index that is not a number", map[string]any{"op": "removeAt", "path": "organizations", "index": "x"}},
		{"matching on a field that is not an identity", map[string]any{
			"op": "removeAt", "path": "repos", "index": 0, "expect": map[string]any{"releases": "provider"}}},
		{"an op nobody implements", map[string]any{"op": "rename", "path": "site.title", "value": "x"}},
		{"a string where a list of strings belongs", map[string]any{
			"op": "set", "path": "content.orgs", "value": map[string]any{"nested": true}}},
		{"a control character in a value", map[string]any{"op": "set", "path": "site.title", "value": "a\nb"}},
	}
	for _, c := range cases {
		r.post("/api/config/write", map[string]any{"operations": []any{c.op}}).
			wantStatus(http.StatusBadRequest, c.what)
	}
	// An empty request is refused too, rather than counting as a successful save of nothing.
	r.post("/api/config/write", map[string]any{"operations": []any{}}).
		wantStatus(http.StatusBadRequest, "an empty operations list")

	wantFile(t, r.configText(), fixtureConfig, "the config after every refusal")
	r.finish("/api/cancel")
}

func TestConfigWriteAddsAndRemovesOrganizationsAndHostedSites(t *testing.T) {
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	r.post("/api/config/write", map[string]any{"operations": []any{
		map[string]any{"op": "add", "path": "organizations", "item": map[string]any{"slug": "new-org", "name": "New Org"}},
		map[string]any{"op": "add", "path": "hosting.sites", "item": map[string]any{"repo": "frznforge", "slug": "docs"}},
	}}).wantStatus(http.StatusOK, "two adds")

	afterAdd := fixtureConfig
	afterAdd = strings.Replace(afterAdd,
		"    { \"slug\": \"cc\", \"name\": \"Canadian Coding\" }\n",
		"    { \"slug\": \"cc\", \"name\": \"Canadian Coding\" },\n    { \"slug\": \"new-org\", \"name\": \"New Org\" },\n", 1)
	afterAdd = strings.Replace(afterAdd,
		"      { \"repo\": \"frznforge\", \"branch\": \"gh-pages\" }\n",
		"      { \"repo\": \"frznforge\", \"branch\": \"gh-pages\" },\n      { \"repo\": \"frznforge\", \"slug\": \"docs\" },\n", 1)
	wantFile(t, r.configText(), afterAdd, "the config after two adds")

	// cc is index 0; new-org was appended after it.
	r.post("/api/config/write", map[string]any{"operations": []any{
		map[string]any{"op": "removeAt", "path": "organizations", "index": 0,
			"expect": map[string]any{"slug": "cc"}},
	}}).wantStatus(http.StatusOK, "remove organizations[0]")

	afterRemove := strings.Replace(afterAdd, "\n    { \"slug\": \"cc\", \"name\": \"Canadian Coding\" },", "", 1)
	wantFile(t, r.configText(), afterRemove, "the config after the removal")
	r.finish("/api/done")
}

func TestConfigWriteRemovesOnlyTheIntendedRowWhenTwoShareARepo(t *testing.T) {
	// The subset trap: [{repo}, {repo, slug}] — a content match on {repo} would delete both.
	// Positional removal deletes exactly the row the page pointed at.
	source := `{
  "owner": { "name": "K", "handle": "k" },
  "repos": [{ "type": "local", "path": ".", "slug": "docs" }],
  "hosting": {
    "sites": [
      { "repo": "docs" },
      { "repo": "docs", "slug": "documentation", "branch": "docs" }
    ]
  }
}
`
	r := startWizard(t, wizardOptions{config: source})
	r.post("/api/config/write", map[string]any{"operations": []any{
		map[string]any{"op": "removeAt", "path": "hosting.sites", "index": 0,
			"expect": map[string]any{"repo": "docs"}},
	}}).wantStatus(http.StatusOK, "remove the slug-less row")

	wantFile(t, r.configText(), strings.Replace(source, "\n      { \"repo\": \"docs\" },", "", 1),
		"the config after removing hosting.sites[0]")
	r.finish("/api/done")
}

func TestConfigWriteEditsPastACommentedOutBlock(t *testing.T) {
	// A commented-out copy of the real block sits above it. The comment-aware walker has to find
	// the real one and leave the comment exactly as it is.
	source := `// old: { "site": { "title": "OLD" } }
{
  "site": { "title": "Real" },
  "owner": { "name": "K", "handle": "k" }
}
`
	r := startWizard(t, wizardOptions{config: source})
	r.post("/api/config/write", map[string]any{"operations": []any{
		map[string]any{"op": "set", "path": "site.title", "value": "New"},
	}}).wantStatus(http.StatusOK, "edit past a commented-out block")

	wantFile(t, r.configText(), strings.Replace(source, `"title": "Real"`, `"title": "New"`, 1),
		"the config after editing past a comment")
	wantContains(t, r.configText(), `// old: { "site": { "title": "OLD" } }`, "the untouched comment")
	r.finish("/api/done")
}

func TestConfigWriteRollsBackAnEditThatCannotBeAppliedFaithfully(t *testing.T) {
	// A duplicated key is the shape the walker cannot follow: JSON keeps the LAST "site", the
	// splice edits the FIRST. The schema pre-check passes on the clone, so only the post-write
	// re-read catches it — and when it does, the file goes back exactly as it was.
	source := `{
  // written twice by accident; the loader keeps the last one
  "site": { "title": "First", "url": "https://example.com" },
  "owner": { "name": "K", "handle": "k" },
  "site": { "title": "Second" }
}
`
	r := startWizard(t, wizardOptions{config: source})
	res := r.post("/api/config/write", map[string]any{"operations": []any{
		map[string]any{"op": "set", "path": "site.title", "value": "New"},
	}}).wantStatus(http.StatusConflict, "an edit the walker cannot follow")
	if !strings.Contains(res.body, "did not change the config the way it should") {
		t.Errorf("the refusal does not say what went wrong: %q", res.body)
	}
	wantFile(t, r.configText(), source, "the config after a rolled-back edit")

	// The pre-wizard bytes are still one file away, whatever happened to the write.
	backups := r.backups(r.dir)
	if len(backups) != 1 {
		t.Fatalf("%d backups after a rolled-back write, want one holding the pre-wizard bytes", len(backups))
	}
	raw, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	wantFile(t, string(raw), source, "the backup after a rolled-back edit")
	wantEqual(t, r.finish("/api/cancel")["writes"], float64(0), "writes")
}

func TestConfigWriteRefusesAnAdditionTheSchemaRefuses(t *testing.T) {
	// "repos" is a top-level path the build itself emits; a hosted site claiming it would shadow
	// the whole forge.
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	res := r.post("/api/config/write", map[string]any{"operations": []any{
		map[string]any{"op": "add", "path": "hosting.sites", "item": map[string]any{"repo": "frznforge", "slug": "repos"}},
	}}).wantStatus(http.StatusBadRequest, "a reserved hosting slug")
	if !strings.Contains(res.body, "the build itself owns") {
		t.Errorf("the refusal does not explain the collision: %q", res.body)
	}
	wantFile(t, r.configText(), fixtureConfig, "the config after a refused addition")
	r.finish("/api/cancel")
}

func TestConfigWriteRefusesAnAdditionMissingARequiredField(t *testing.T) {
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	for _, item := range []map[string]any{
		{"name": "No Slug"},
		{"slug": "no-name"},
		{"slug": "x", "name": "X", "descriptoin": "a typo the user can see"},
	} {
		r.post("/api/config/write", map[string]any{"operations": []any{
			map[string]any{"op": "add", "path": "organizations", "item": item},
		}}).wantStatus(http.StatusBadRequest, fmt.Sprintf("add %v", item))
	}
	wantFile(t, r.configText(), fixtureConfig, "the config after three refused additions")
	r.finish("/api/cancel")
}

func TestConfigWriteRemovesAReposEntryByItsRawFields(t *testing.T) {
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	r.post("/api/config/write", map[string]any{"operations": []any{
		map[string]any{"op": "removeAt", "path": "repos", "index": 1,
			"expect": map[string]any{"type": "github", "owner": "me", "repo": "old"}},
	}}).wantStatus(http.StatusOK, "remove repos[1]")

	// The last element takes the preceding comma with it, so the closing bracket moves up.
	want := strings.Replace(fixtureConfig,
		",\n    { \"type\": \"github\", \"owner\": \"me\", \"repo\": \"old\" }\n  ]", "]", 1)
	wantFile(t, r.configText(), want, "the config after removing a source")
	wantContains(t, r.configText(), "// Self-host demo: the frznforge repo itself.", "the comment inside the array")

	after := r.get("/api/config").json()
	wantSources(t, after["sources"], []map[string]string{{"type": "local", "path": ".", "slug": "frznforge"}})
	r.finish("/api/done")
}

func TestConfigWriteRefusesToEditAConfigThatDoesNotLoad(t *testing.T) {
	// Editing a file the build cannot read would leave the user with two problems instead of one.
	r := startWizard(t, wizardOptions{config: brokenConfig})
	res := r.post("/api/config/write", map[string]any{"operations": []any{
		map[string]any{"op": "set", "path": "site.title", "value": "X"},
	}}).wantStatus(http.StatusConflict, "editing an unloadable config")
	if !strings.Contains(res.body, "does not load cleanly") {
		t.Errorf("the refusal does not say why: %q", res.body)
	}
	wantFile(t, r.configText(), brokenConfig, "the unloadable config")
	r.finish("/api/cancel")
}

func TestConfigWriteRefusesWhenThereIsNoConfigAtAll(t *testing.T) {
	r := startWizard(t, wizardOptions{})
	r.post("/api/config/write", map[string]any{"operations": []any{
		map[string]any{"op": "set", "path": "site.title", "value": "X"},
	}}).wantStatus(http.StatusConflict, "editing a config that does not exist")
	r.finish("/api/cancel")
}

func TestUnsetWritesNullWhichTheLoaderReadsAsAbsent(t *testing.T) {
	// JSONC has no `undefined`, so clearing a field writes a literal null. There is deliberately
	// no "delete this line" operation: deleting lines is how the comment beside a field dies.
	source := `{
  "site": { "title": "T", "url": "https://example.com" }, // the site block
  "owner": { "name": "K", "handle": "k" }
}
`
	r := startWizard(t, wizardOptions{config: source})
	r.post("/api/config/write", map[string]any{"operations": []any{
		map[string]any{"op": "unset", "path": "site.url"},
	}}).wantStatus(http.StatusOK, "unset site.url")

	wantFile(t, r.configText(), strings.Replace(source, `"url": "https://example.com"`, `"url": null`, 1),
		"the config after an unset")
	// null and absent read the same, which is what makes the post-write comparison agree.
	after := r.get("/api/config").json()
	if _, present := at(t, after, "current", "site").(map[string]any)["url"]; present {
		t.Error("site.url is still set after being cleared")
	}
	r.finish("/api/done")
}

/* ------------------------------------------------------------------ setAt */

func TestSetAtChangesOneFieldAndLeavesTheFileOtherwiseIdentical(t *testing.T) {
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	r.post("/api/config/write", map[string]any{"operations": []any{
		map[string]any{"op": "setAt", "path": "organizations", "index": 0, "key": "name",
			"value": "Renamed Co", "expect": map[string]any{"slug": "cc"}},
	}}).wantStatus(http.StatusOK, "setAt organizations[0].name")

	wantFile(t, r.configText(), strings.Replace(fixtureConfig, `"Canadian Coding"`, `"Renamed Co"`, 1),
		"the config after an in-place edit")
	r.finish("/api/done")
}

func TestSetAtRefusesAFieldThatIsNotEditable(t *testing.T) {
	// Narrower than the add spec on purpose: an organization's slug is its identity and is what
	// repos point at, so renaming it in place would silently orphan every member.
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	cases := []struct {
		what string
		op   map[string]any
	}{
		{"an identity field", map[string]any{"op": "setAt", "path": "organizations", "index": 0, "key": "slug", "value": "other"}},
		{"a prototype key", map[string]any{"op": "setAt", "path": "organizations", "index": 0, "key": "__proto__", "value": "x"}},
		{"a constructor key", map[string]any{"op": "setAt", "path": "organizations", "index": 0, "key": "constructor", "value": "x"}},
		{"a list that cannot be edited", map[string]any{"op": "setAt", "path": "nope", "index": 0, "key": "name", "value": "x"}},
	}
	for _, c := range cases {
		r.post("/api/config/write", map[string]any{"operations": []any{c.op}}).
			wantStatus(http.StatusBadRequest, c.what)
	}
	// An index past the end is well-formed but unappliable, so it fails later — as a conflict.
	r.post("/api/config/write", map[string]any{"operations": []any{
		map[string]any{"op": "setAt", "path": "organizations", "index": 99, "key": "name", "value": "x"},
	}}).wantStatus(http.StatusConflict, "an index past the end")

	wantFile(t, r.configText(), fixtureConfig, "the config after every refusal")
	r.finish("/api/cancel")
}

func TestSetAtRefusesWhenExpectDoesNotDescribeTheEntry(t *testing.T) {
	// The page working from a stale list must not edit whatever happens to sit at that index now.
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	r.post("/api/config/write", map[string]any{"operations": []any{
		map[string]any{"op": "setAt", "path": "organizations", "index": 0, "key": "name",
			"value": "x", "expect": map[string]any{"slug": "not-cc"}},
	}}).wantStatus(http.StatusConflict, "a stale expect")
	wantFile(t, r.configText(), fixtureConfig, "the config after a stale expect")
	r.finish("/api/cancel")
}

func TestSetAtRejectsAValueTheSchemaRefusesBeforeTouchingTheFile(t *testing.T) {
	// An avatar must be a path inside public/, never a URL: frznforge pages load no third-party
	// assets, and an avatar pointing at a forge's CDN would break that for every visitor.
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	res := r.post("/api/config/write", map[string]any{"operations": []any{
		map[string]any{"op": "setAt", "path": "organizations", "index": 0, "key": "avatar",
			"value": "https://evil.example/u.png"},
	}}).wantStatus(http.StatusBadRequest, "an avatar that is a URL")
	if !strings.Contains(res.body, "public/") {
		t.Errorf("the refusal does not say where an avatar has to live: %q", res.body)
	}
	wantFile(t, r.configText(), fixtureConfig, "the config after a refused avatar")
	r.finish("/api/cancel")
}

/* ------------------------------------------------------------------ /api/profile */

// profileFile is the profile the fixture config points at by default.
func profileFile(dir string) string { return filepath.Join(dir, "content", "profile.md") }

func writeProfile(t *testing.T, dir, content string) string {
	t.Helper()
	file := profileFile(dir)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestProfileRoundTripsTheBodyWhileFrontmatterKeepsItsExactBytes(t *testing.T) {
	// The frontmatter block rides along as BYTES, not as parsed YAML — CRLF included. The wizard
	// edits the body and has no business reformatting metadata it does not read.
	const original = "---\r\ntitle: Me\r\nkind: profile\r\n---\r\n# Hi\r\n\r\nold body\r\n"
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	file := writeProfile(t, r.dir, original)

	got := r.get("/api/profile").wantStatus(http.StatusOK, "GET /api/profile").json()
	wantEqual(t, got["available"], true, "available")
	wantEqual(t, got["exists"], true, "exists")
	wantEqual(t, got["path"], file, "path")
	wantEqual(t, got["frontmatter"], "---\r\ntitle: Me\r\nkind: profile\r\n---\r\n", "frontmatter")
	wantEqual(t, got["body"], "# Hi\r\n\r\nold body\r\n", "body")

	body := r.post("/api/profile/write", map[string]any{"body": "# New\n\nfresh body\n"}).
		wantStatus(http.StatusOK, "POST /api/profile/write").json()
	wantEqual(t, body["changed"], true, "changed")
	wantEqual(t, body["path"], file, "path")

	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	wantFile(t, string(raw), "---\r\ntitle: Me\r\nkind: profile\r\n---\r\n# New\n\nfresh body\n", "the saved profile")

	backup, _ := body["backup"].(string)
	saved, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("read the profile backup: %v", err)
	}
	wantFile(t, string(saved), original, "the profile backup")
	r.finish("/api/done")
}

func TestProfileCreatesAMissingFileAndItsFolder(t *testing.T) {
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	before := r.get("/api/profile").json()
	wantEqual(t, before["available"], true, "available")
	wantEqual(t, before["exists"], false, "exists")
	wantEqual(t, before["body"], "", "body")

	r.post("/api/profile/write", map[string]any{"body": "# Hello\n"}).
		wantStatus(http.StatusOK, "the first profile save")
	raw, err := os.ReadFile(profileFile(r.dir))
	if err != nil {
		t.Fatalf("the profile file was not created: %v", err)
	}
	wantFile(t, string(raw), "# Hello\n", "a created profile")
	r.finish("/api/done")
}

func TestProfileDoesNotBackUpAFileTheWizardItselfCreated(t *testing.T) {
	// A file this session created has no pre-wizard state to keep, so a second save must not
	// snapshot the wizard's own first write as if it were it.
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	for _, body := range []string{"# One\n", "# Two\n"} {
		r.post("/api/profile/write", map[string]any{"body": body}).wantStatus(http.StatusOK, "save "+body)
	}
	for _, dir := range []string{r.dir, filepath.Join(r.dir, "content")} {
		if backups := r.backups(dir); len(backups) != 0 {
			t.Errorf("a wizard-created file was backed up: %v", backups)
		}
	}
	raw, err := os.ReadFile(profileFile(r.dir))
	if err != nil {
		t.Fatal(err)
	}
	wantFile(t, string(raw), "# Two\n", "the profile after two saves")
	r.finish("/api/done")
}

func TestProfileKeepsTheTerminatorOnItsOwnLine(t *testing.T) {
	// The closing `---` is the file's last line with no newline after it. Gluing the body
	// straight on would fuse them into `---# Hello`, and on the next read that line no longer
	// closes the block — the whole frontmatter would be swallowed into the body.
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	writeProfile(t, r.dir, "---\ntitle: Me\n---")
	r.post("/api/profile/write", map[string]any{"body": "# Hello\n"}).wantStatus(http.StatusOK, "save over a bare terminator")

	raw, err := os.ReadFile(profileFile(r.dir))
	if err != nil {
		t.Fatal(err)
	}
	wantFile(t, string(raw), "---\ntitle: Me\n---\n# Hello\n", "the profile after a bare-terminator save")
	r.finish("/api/done")
}

func TestProfilePreviewsWithTheSitesOwnRenderer(t *testing.T) {
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	body := r.post("/api/profile/preview", map[string]any{"body": "# Hello\n\n```mermaid\ngraph TD;\n```\n"}).
		wantStatus(http.StatusOK, "POST /api/profile/preview").json()
	html, _ := body["html"].(string)
	// The site's renderer demotes `#` one level, because a page already carries its own <h1>.
	wantContains(t, html, "<h2>Hello</h2>", "the demoted heading")
	// No diagram bundle ships with the wizard page, so the fence stays an honest code block.
	if strings.Contains(html, "hf-mermaid") {
		t.Errorf("the preview emitted a mermaid container the wizard page cannot render:\n%s", html)
	}
	wantContains(t, html, "graph TD;", "the fence's contents")
	r.finish("/api/cancel")
}

func TestProfileRefusesABodyWithANULByte(t *testing.T) {
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	r.post("/api/profile/write", map[string]any{"body": "a\x00b"}).
		wantStatus(http.StatusBadRequest, "a body carrying a NUL")
	if _, err := os.Stat(profileFile(r.dir)); err == nil {
		t.Error("a refused body still created the profile file")
	}
	r.finish("/api/cancel")
}

func TestProfileRefusesATargetOutsideTheProjectOrNotMarkdown(t *testing.T) {
	// owner.profile is a browser-settable config field, so the profile writer must refuse a
	// target that escapes the project, is not markdown, or is the config file itself — which the
	// wizard then reads back.
	cases := []struct{ what, profile string }{
		{"an escape with ..", "../escape.md"},
		{"the config file itself", "./frznforge.config.jsonc"},
		{"a file that is not markdown", "./evil.ts"},
		// Built from the platform's own temp directory so it really is absolute on Windows too,
		// where a leading slash is not.
		{"an absolute path elsewhere", filepath.ToSlash(filepath.Join(os.TempDir(), "absolute.md"))},
	}
	for _, c := range cases {
		t.Run(c.what, func(t *testing.T) {
			source := "{\n  \"owner\": { \"name\": \"K\", \"handle\": \"k\", \"profile\": \"" + c.profile + "\" }\n}\n"
			r := startWizard(t, wizardOptions{config: source})
			got := r.get("/api/profile").wantStatus(http.StatusOK, "GET /api/profile").json()
			wantEqual(t, got["available"], false, "available")

			res := r.post("/api/profile/write", map[string]any{"body": "pwned\n"})
			if res.status < 400 {
				t.Errorf("the write was accepted (status %d): %s", res.status, res.body)
			}
			wantFile(t, r.configText(), source, "the config after a refused profile target")
			for _, escape := range []string{
				filepath.Join(filepath.Dir(r.dir), "escape.md"),
				filepath.Join(os.TempDir(), "absolute.md"),
			} {
				if _, err := os.Stat(escape); err == nil {
					t.Errorf("a profile write escaped the project and landed at %s", escape)
				}
			}
			r.finish("/api/cancel")
		})
	}
}

/* ------------------------------------------------------------------ /api/upload */

// pngBytes is a real PNG signature plus filler, so the magic-byte check runs against genuine
// bytes rather than a string that happens to start with the right letters.
var pngBytes = append([]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a},
	[]byte("the rest does not need to be a valid image for a magic-byte check")...)

func b64(raw []byte) string { return base64.StdEncoding.EncodeToString(raw) }

func TestUploadWritesToAPathTheServerChose(t *testing.T) {
	// The browser sends WHICH avatar this is and the bytes; it never sends a path.
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	body := r.post("/api/upload", map[string]any{
		"target": map[string]any{"kind": "owner"}, "data": b64(pngBytes),
	}).wantStatus(http.StatusOK, "POST /api/upload").json()
	wantEqual(t, body["path"], "images/owner.png", "path")
	wantEqual(t, body["bytes"], float64(len(pngBytes)), "bytes")

	onDisk, err := os.ReadFile(filepath.Join(r.dir, "public", "images", "owner.png"))
	if err != nil {
		t.Fatalf("the image was not written: %v", err)
	}
	if string(onDisk) != string(pngBytes) {
		t.Error("the stored image is not the bytes that were uploaded")
	}
	// The config is untouched: the page still has to save the field through the ordinary
	// validated operation, so there is exactly one code path that writes config.
	wantFile(t, r.configText(), fixtureConfig, "the config after an upload")
	r.finish("/api/cancel")
}

func TestUploadAcceptsADataURLAndNamesTheFileFromTheSniffedType(t *testing.T) {
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	body := r.post("/api/upload", map[string]any{
		"target": map[string]any{"kind": "org", "slug": "acme"},
		"data":   "data:image/png;base64," + b64(pngBytes), // what FileReader produces
	}).wantStatus(http.StatusOK, "an upload as a data: URL").json()
	wantEqual(t, body["path"], "images/orgs/acme.png", "path")

	// Claims PNG, carries JPEG: the extension comes from the sniff, never from the claim.
	jpeg := append([]byte{0xff, 0xd8, 0xff}, []byte("jpeg-ish")...)
	body = r.post("/api/upload", map[string]any{
		"target": map[string]any{"kind": "owner"},
		"data":   "data:image/png;base64," + b64(jpeg),
	}).wantStatus(http.StatusOK, "a JPEG claiming to be a PNG").json()
	wantEqual(t, body["path"], "images/owner.jpg", "path")
	r.finish("/api/cancel")
}

func TestUploadRefusesAnythingThatIsNotAnImage(t *testing.T) {
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	for _, payload := range []string{
		b64([]byte("#!/bin/sh\nrm -rf /\n")),
		b64([]byte("MZ\x90\x00")), // a Windows executable
		b64([]byte("<html><svg></svg></html>")),
		"",
		b64(nil),
		"not base64 at all!!",
	} {
		res := r.post("/api/upload", map[string]any{"target": map[string]any{"kind": "owner"}, "data": payload})
		if res.status < 400 {
			t.Errorf("%.20q was accepted as an image (status %d)", payload, res.status)
		}
	}
	if entries, err := os.ReadDir(filepath.Join(r.dir, "public", "images")); err == nil && len(entries) > 0 {
		t.Errorf("a refused upload still wrote something: %v", entries)
	}
	r.finish("/api/cancel")
}

func TestUploadRefusesATargetThatTriesToSteerThePath(t *testing.T) {
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	for _, target := range []map[string]any{
		{"kind": "org", "slug": "../../etc/passwd"},
		{"kind": "org", "slug": "a/b"},
		{"kind": "org", "slug": ""},
		{"kind": "contributor", "index": -1},
		{"kind": "contributor", "index": 1.5},
		{"kind": "contributor", "index": 1000},
		{"kind": "file", "path": "anywhere.png"},
	} {
		res := r.post("/api/upload", map[string]any{"target": target, "data": b64(pngBytes)})
		if res.status < 400 {
			t.Errorf("target %v was accepted (status %d)", target, res.status)
		}
	}
	// An extra key is ignored, not honoured: the path stays the server's decision.
	body := r.post("/api/upload", map[string]any{
		"target": map[string]any{"kind": "owner", "path": "../escape.png"}, "data": b64(pngBytes),
	}).wantStatus(http.StatusOK, "an owner upload carrying a stray path").json()
	wantEqual(t, body["path"], "images/owner.png", "path")

	if _, err := os.Stat(filepath.Join(r.dir, "escape.png")); err == nil {
		t.Error("an upload escaped public/")
	}
	r.finish("/api/cancel")
}

func TestUploadBacksUpAnOverwrittenImageByteForByte(t *testing.T) {
	// The bug this guards: the config/profile backup helper writes a string, which would mangle
	// a PNG. Uploads copy the bytes instead.
	r := startWizard(t, wizardOptions{config: fixtureConfig})
	dir := filepath.Join(r.dir, "public", "images")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	original := append([]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a},
		[]byte{0x00, 0x80, 0xff, 0xfe, 0x01}...) // bytes a utf8 round-trip would destroy
	if err := os.WriteFile(filepath.Join(dir, "owner.png"), original, 0o644); err != nil {
		t.Fatal(err)
	}

	r.post("/api/upload", map[string]any{"target": map[string]any{"kind": "owner"}, "data": b64(pngBytes)}).
		wantStatus(http.StatusOK, "overwrite an existing avatar")

	backups := r.backups(dir)
	if len(backups) != 1 {
		t.Fatalf("%d backups beside the image, want one", len(backups))
	}
	restored, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(original) {
		t.Errorf("the backup is %v, want the original bytes %v", restored, original)
	}
	r.finish("/api/cancel")
}

/* ------------------------------------------------------------------ page ↔ server */

// The page offers a field, the server decides whether it may be written — and the two are
// separate files, so they can drift. They did: the Settings card shipped a "Repos per page"
// input for `listing.pageSize` that the allow-list did not carry, so touching it failed the
// whole save with a 400. This is the only place that drift is visible without opening a browser.

var (
	groupFieldRe = regexp.MustCompile(`\{\s*path:\s*'([^']+)'([^}]*)\}`)
	unsetRe      = regexp.MustCompile(`unset:\s*true`)
	editRowRe    = regexp.MustCompile(`editRow\(\s*row,\s*'([^']+)',[\s\S]*?\[([\s\S]*?)\],`)
	editKeyRe    = regexp.MustCompile(`key:\s*'([^']+)'`)
)

// pageFields reads the `{ path: '…' }` entries of the page's GROUPS table with their unset flag.
func pageFields(t *testing.T) map[string]bool {
	t.Helper()
	start := strings.Index(pageHTML, "var GROUPS = [")
	if start < 0 {
		t.Fatal("the embedded page has no GROUPS table — this test is no longer reading anything")
	}
	end := strings.Index(pageHTML[start:], "\n  ];")
	if end < 0 {
		t.Fatal("the GROUPS table is not closed the way this test expects")
	}
	fields := map[string]bool{}
	for _, match := range groupFieldRe.FindAllStringSubmatch(pageHTML[start:start+end], -1) {
		fields[match[1]] = unsetRe.MatchString(match[2])
	}
	return fields
}

func TestThePageOffersExactlyTheSettingsTheServerWillWrite(t *testing.T) {
	fields := pageFields(t)
	if len(fields) < 30 {
		t.Fatalf("only %d fields were read out of the page's GROUPS table — the regex has stopped matching", len(fields))
	}
	// Left to right: a field the page offers and the allow-list omits fails the whole save with
	// a 400 the moment the user touches it.
	for path, unset := range fields {
		if !setPaths[path] {
			t.Errorf("the page offers %s but the server will not set it", path)
		}
		if unset && !unsetPaths[path] {
			t.Errorf("the page offers to clear %s but the server will not unset it", path)
		}
	}
	// Right to left: an allow-list entry nothing offers is a setting that quietly stopped being
	// editable.
	for path := range setPaths {
		if _, offered := fields[path]; !offered {
			t.Errorf("the server can set %s but the page no longer offers it", path)
		}
	}
	for path := range unsetPaths {
		if unset, offered := fields[path]; !offered || !unset {
			t.Errorf("the server can clear %s but the page no longer offers to", path)
		}
	}
}

func TestThePageOffersNoInPlaceEditTheServerWouldRefuse(t *testing.T) {
	// The same drift, one layer down: `editRow(row, '<list>', i, entry, [{ key: '…' }])` in the
	// page must line up with arraySetFields, or "Save changes" 400s.
	calls := editRowRe.FindAllStringSubmatch(pageHTML, -1)
	if len(calls) == 0 {
		t.Fatal("no editRow call sites were found — the regex has stopped matching")
	}
	found := map[string]bool{}
	for _, call := range calls {
		list, block := call[1], call[2]
		found[list] = true
		keys := editKeyRe.FindAllStringSubmatch(block, -1)
		if len(keys) == 0 {
			t.Errorf("%s: the editRow call offers no fields at all", list)
		}
		for _, key := range keys {
			editable := false
			for _, allowed := range arraySetFields[list] {
				if key[1] == allowed {
					editable = true
					break
				}
			}
			if !editable {
				t.Errorf("%s.%s is offered by the page but not editable on the server", list, key[1])
			}
		}
	}
	// Every list the server can edit is one the page actually renders an editor for.
	var missing []string
	for list := range arraySetFields {
		if !found[list] {
			missing = append(missing, list)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("the server can edit %v in place, but the page offers no editor for them", missing)
	}
}

func TestTheAllowListCoversEverySettingTheSchemaAdded(t *testing.T) {
	// The acceptance bar each version set for itself: the wizard exposes every new key.
	for _, key := range []string{
		// 0.2.0
		"theme.heat.hot", "theme.heat.warm", "theme.heat.neutral", "theme.heat.cool",
		"ingest.maxCommitAgeDays", "ingest.reuse.enabled", "ingest.reuse.maxAgeMinutes",
		"site.base", "hosting.maxFileBytes", "markdown.mermaid",
		// 0.3.0
		"owner.avatar", "ingest.failOnDegraded", "ingest.reuse.skipUnchanged", "ingest.reuse.cooldownSeconds",
	} {
		if !setPaths[key] {
			t.Errorf("%s should be editable by the wizard", key)
		}
	}
}
