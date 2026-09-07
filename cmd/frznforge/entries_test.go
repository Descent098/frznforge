package main

// The entries `init` writes, and the splice that puts them into a config file — the port of
// cli.test.ts's `entries`, `insertRepos`, `updateConfigFile` and "what can and cannot reach a
// config file" blocks, retargeted from TypeScript source to JSONC.
//
// The file being edited is the user's own, hand-written and full of comments, and this command
// is the only thing in frznforge that writes to it. Everything below is either "the right entry
// went in" or "nothing else moved".

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"frznforge/internal/config"
)

var ezcv = repoEntry{Type: "github", Owner: "Descent098", Repo: "ezcv", Releases: "provider"}

/* ---- rendering ------------------------------------------------------------ */

func TestRenderEntryIsOneLinePerSourceOmittingTheDefaultHost(t *testing.T) {
	// One line per source is what makes the array readable and the diff after an init reviewable.
	if got := renderEntry(ezcv); got != `{ "type": "github", "owner": "Descent098", "repo": "ezcv", "releases": "provider" }` {
		t.Errorf("github entry = %s", got)
	}
	gitea := repoEntry{Type: "gitea", Host: "https://git.example.com", Owner: "a", Repo: "b"}
	if got := renderEntry(gitea); got != `{ "type": "gitea", "host": "https://git.example.com", "owner": "a", "repo": "b" }` {
		t.Errorf("gitea entry = %s", got)
	}
	// Key order comes from entryKeyOrder, not from a map: two runs of init must write the same
	// bytes, and ranging a map here would give a different config every time.
	if got := renderEntry(repoEntry{Repo: "b", Owner: "a", Type: "github"}); got != `{ "type": "github", "owner": "a", "repo": "b" }` {
		t.Errorf("field order is not fixed: %s", got)
	}
}

func TestRenderSnippetIsPasteable(t *testing.T) {
	// This is what a user without a config file is told to paste, so it has to be a valid
	// fragment on its own — key, brackets, trailing comma and all.
	got := renderSnippet([]repoEntry{{Type: "github", Owner: "a", Repo: "b"}})
	want := "  \"repos\": [\n    { \"type\": \"github\", \"owner\": \"a\", \"repo\": \"b\" },\n  ],"
	if got != want {
		t.Errorf("snippet =\n%s\nwant\n%s", got, want)
	}
}

func TestEntriesForBuildsEachProvidersShape(t *testing.T) {
	github, err := entriesFor("github", "https://api.github.com", []remoteRepo{testRepo("me/alpha")}, "provider")
	if err != nil {
		t.Fatal(err)
	}
	// The default host is omitted: writing it would be noise in every GitHub config, and
	// entryKey has to treat "absent" and "the default" as the same source anyway.
	if len(github) != 1 || github[0] != (repoEntry{Type: "github", Owner: "me", Repo: "alpha", Releases: "provider"}) {
		t.Errorf("github entry = %+v", github)
	}

	gl := remoteRepo{Name: "proj", FullName: "g/s/proj", Owner: "g/s", Project: "g/s/proj"}
	gitlab, err := entriesFor("gitlab", "https://gitlab.com", []remoteRepo{gl}, "tags")
	if err != nil {
		t.Fatal(err)
	}
	// GitLab is addressed by its full namespaced path, never by owner+repo: `g/s/proj` has two
	// levels of namespace and splitting it would address the wrong project.
	if len(gitlab) != 1 || gitlab[0] != (repoEntry{Type: "gitlab", Project: "g/s/proj", Releases: "tags"}) {
		t.Errorf("gitlab entry = %+v", gitlab)
	}

	forgejo, err := entriesFor("forgejo", "https://codeberg.org", []remoteRepo{testRepo("me/alpha")}, "provider")
	if err != nil {
		t.Fatal(err)
	}
	// Self-hosted providers have no default host, so theirs is always written out.
	if len(forgejo) != 1 || forgejo[0].Host != "https://codeberg.org" {
		t.Errorf("forgejo entry = %+v", forgejo)
	}
}

func TestEntriesForDisambiguatesReposThatWouldShareASlug(t *testing.T) {
	// Two repositories called `tool` would render to the same URL, and the second would silently
	// overwrite the first's pages.
	entries, err := entriesFor("github", "https://api.github.com",
		[]remoteRepo{testRepo("one/tool"), testRepo("two/tool")}, "provider")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Slug != "one-tool" || entries[1].Slug != "two-tool" {
		t.Errorf("slugs = %q, %q", entries[0].Slug, entries[1].Slug)
	}
	// A selection with no collision gets no slug at all: the default (the repository name) is
	// the nicer URL, and writing it explicitly would freeze it against a later rename.
	single, err := entriesFor("github", "https://api.github.com", []remoteRepo{testRepo("one/tool")}, "provider")
	if err != nil {
		t.Fatal(err)
	}
	if single[0].Slug != "" {
		t.Errorf("an uncontested repo was given an explicit slug %q", single[0].Slug)
	}
}

func TestEntryKeyIdentifiesASourceIgnoringCosmetics(t *testing.T) {
	// This key is what makes a second `init` add nothing. Case, a trailing slash and the implicit
	// default host all describe the same remote repository.
	if a, b := entryKey(repoEntry{Type: "github", Owner: "A", Repo: "B"}),
		entryKey(repoEntry{Type: "github", Host: "https://api.github.com/", Owner: "a", Repo: "b"}); a != b {
		t.Errorf("%q and %q are the same source", a, b)
	}
	// Two forges at the same address are not the same source: their APIs differ, and so does
	// everything ingest does with them.
	if a, b := entryKey(repoEntry{Type: "gitea", Host: "https://x.dev", Owner: "a", Repo: "b"}),
		entryKey(repoEntry{Type: "forgejo", Host: "https://x.dev", Owner: "a", Repo: "b"}); a == b {
		t.Errorf("gitea and forgejo collapsed to the same key %q", a)
	}
}

/* ---- the splice ----------------------------------------------------------- */

// newEntryLine matches the line insertRepos added, so a test can subtract it and compare the
// rest of the file byte for byte.
var newEntryLine = regexp.MustCompile(`(?m)^ +\{ "type": "github".*\n`)

func TestInsertReposAddsEntriesWithoutDisturbingAnythingElse(t *testing.T) {
	result, ok := insertRepos(fixtureConfig, []repoEntry{ezcv})
	if !ok || !result.changed || len(result.added) != 1 {
		t.Fatalf("ok=%v changed=%v added=%v", ok, result.changed, result.added)
	}
	mustContain(t, result.text, `    { "type": "github", "owner": "Descent098", "repo": "ezcv", "releases": "provider" },`,
		"the new entry is not in the file")
	// Comments are the config's documentation and most of its bytes. Losing one is losing the
	// only explanation the user wrote for themselves.
	for _, comment := range []string{
		"// Self-host demo: the frznforge repo itself.",
		"* Repositories to ingest.",
		`"outDir": "./data" // keep this comment`,
		`{ "type": "local", "path": ".", "slug": "frznforge" },`,
	} {
		mustContain(t, result.text, comment, "the splice destroyed something it did not write")
	}
	// The strongest property available: subtract the one line that was added and the file is
	// byte-identical to what it was. Nothing reindented, no comment moved, no key reordered.
	if rest := newEntryLine.ReplaceAllString(result.text, ""); rest != fixtureConfig {
		t.Errorf("bytes outside the new entry changed:\n%s", rest)
	}
}

func TestInsertReposIsIdempotent(t *testing.T) {
	// Running init twice against the same account is the normal way to add one repository, so
	// the second run must add nothing rather than a second copy of everything.
	first, _ := insertRepos(fixtureConfig, []repoEntry{ezcv})
	second, ok := insertRepos(first.text, []repoEntry{ezcv})
	if !ok {
		t.Fatal("the spliced file no longer has a repos array")
	}
	if second.changed || len(second.added) != 0 {
		t.Errorf("a re-run changed the file: added %v", second.added)
	}
	if len(second.skipped) != 1 || second.skipped[0] != ezcv {
		t.Errorf("skipped = %v", second.skipped)
	}
	if second.text != first.text {
		t.Error("the second run rewrote the file")
	}
}

func TestInsertReposMatchesExistingEntriesWhateverTheirKeyOrder(t *testing.T) {
	// A user who typed their own entry, or the wizard writing keys in another order, still
	// describes the same source. Comparing rendered text instead of identity would duplicate it.
	existing := "{ \"repos\": [\n  { \"repo\": \"ezcv\", \"owner\": \"Descent098\", \"type\": \"github\", \"host\": \"https://api.github.com\" },\n] }\n"
	result, ok := insertRepos(existing, []repoEntry{ezcv})
	if !ok {
		t.Fatal("no repos array found")
	}
	if result.changed || len(result.skipped) != 1 {
		t.Errorf("changed=%v skipped=%v", result.changed, result.skipped)
	}
}

func TestInsertReposFillsAnEmptyArray(t *testing.T) {
	result, ok := insertRepos("{ \"repos\": [],\n}\n", []repoEntry{ezcv})
	if !ok {
		t.Fatal("no repos array found")
	}
	// The `{ ` before the key is not indentation, so the splice falls back to two spaces rather
	// than indenting the entry by whatever happened to precede the key on that line.
	want := "{ \"repos\": [\n    { \"type\": \"github\", \"owner\": \"Descent098\", \"repo\": \"ezcv\", \"releases\": \"provider\" },\n  ],\n}\n"
	if result.text != want {
		t.Errorf("text =\n%q\nwant\n%q", result.text, want)
	}
}

func TestInsertReposKeepsCommentedOutExamplesInAnOtherwiseEmptyArray(t *testing.T) {
	// This is the scaffolded config exactly: `repos: [ … ]` holding five commented-out examples
	// and no value. Treating a value-less array as an empty one replaces the body — deleting the
	// only documentation a new site has for the block it is about to grow.
	source := "{\n  \"repos\": [\n    // { \"type\": \"github\", \"owner\": \"YOU\", \"repo\": \"REPO\" },\n    // { \"type\": \"local\", \"path\": \"../thing\" },\n  ],\n}\n"
	result, ok := insertRepos(source, []repoEntry{ezcv})
	if !ok || !result.changed {
		t.Fatalf("ok=%v changed=%v", ok, result.changed)
	}
	for _, example := range []string{`// { "type": "github", "owner": "YOU", "repo": "REPO" },`, `// { "type": "local", "path": "../thing" },`} {
		mustContain(t, result.text, example, "a commented-out example was deleted")
	}
	mustContain(t, result.text, `"repo": "ezcv"`, "the new entry was not added")
}

func TestInsertReposAddsTheMissingCommaAfterAnEntryWithoutOne(t *testing.T) {
	// Appending after a value with no trailing comma would produce `} {`, which no parser reads
	// — the user's config would stop loading, and the only intact copy would be the .bak.
	source := "{ \"repos\": [\n  { \"type\": \"local\", \"path\": \".\" }\n] }\n"
	result, ok := insertRepos(source, []repoEntry{ezcv})
	if !ok {
		t.Fatal("no repos array found")
	}
	mustContain(t, result.text, "{ \"type\": \"local\", \"path\": \".\" },\n", "the separating comma was not added")
	mustContain(t, result.text, `"repo": "ezcv"`, "the new entry was not added")
	if _, err := config.ParseBytes([]byte(strings.Replace(result.text, "{ \"repos\"", "{ \"owner\": { \"name\": \"x\", \"handle\": \"x\" }, \"repos\"", 1))); err != nil {
		t.Errorf("the spliced file does not parse: %v", err)
	}
}

func TestInsertReposRefusesAFileWithNoReposArrayToSpliceInto(t *testing.T) {
	// ok=false is what makes init fall back to "paste this in yourself" rather than guessing
	// where the array should go and writing a broken file.
	for _, source := range []string{
		"{ \"note\": \"repos: [\" }\n",      // the text appears only inside a string
		"{}\n",                              // no repos key at all
		"{ \"repos\": { \"a\": 1 } }\n",     // repos is there but is not an array
		"// \"repos\": [ ]\n{ \"a\": 1 }\n", // the array is commented out
		"[ { \"repos\": [] } ]\n",           // the file's root is not an object
	} {
		if _, ok := insertRepos(source, []repoEntry{ezcv}); ok {
			t.Errorf("spliced into %q", source)
		}
	}
}

func TestInsertReposDoesNotReadEntryFieldsOutOfAComment(t *testing.T) {
	// A commented-out example above a real entry used to be read *instead of* the real entry,
	// because the field reader took the first match in the element's text.
	commentedExample := "{ \"repos\": [\n" +
		"  // { \"type\": \"github\", \"owner\": \"Descent098\", \"repo\": \"ezcv\" },   <- example, not enabled\n" +
		"  { \"type\": \"local\", \"path\": \".\" },\n] }\n"
	sdu := ezcv
	sdu.Repo = "sdu"
	result, ok := insertRepos(commentedExample, []repoEntry{ezcv, sdu})
	if !ok {
		t.Fatal("no repos array found")
	}
	// ezcv is only mentioned in a comment, so it must be added — not reported as already present.
	if len(result.added) != 2 || len(result.skipped) != 0 {
		t.Errorf("added=%v skipped=%v", result.added, result.skipped)
	}

	shadowedReal := "{ \"repos\": [\n" +
		"  // was: { \"type\": \"github\", \"owner\": \"Descent098\", \"repo\": \"beta\" } — renamed\n" +
		"  { \"type\": \"github\", \"owner\": \"Descent098\", \"repo\": \"ezcv\", \"releases\": \"provider\" },\n] }\n"
	duplicate, ok := insertRepos(shadowedReal, []repoEntry{ezcv})
	if !ok {
		t.Fatal("no repos array found")
	}
	// …and the real entry underneath the comment must still be recognised, not duplicated.
	if duplicate.changed || len(duplicate.skipped) != 1 {
		t.Errorf("changed=%v skipped=%v", duplicate.changed, duplicate.skipped)
	}
}

func TestInsertReposSeesThroughABlockCommentToo(t *testing.T) {
	source := "{ \"repos\": [\n  /* { \"type\": \"github\", \"owner\": \"Descent098\", \"repo\": \"ezcv\" } */\n  { \"type\": \"local\", \"path\": \".\" },\n] }\n"
	result, ok := insertRepos(source, []repoEntry{ezcv})
	if !ok {
		t.Fatal("no repos array found")
	}
	if len(result.added) != 1 || result.added[0] != ezcv {
		t.Errorf("added = %v", result.added)
	}
}

/* ---- the file on disk ----------------------------------------------------- */

func TestUpdateConfigFileBacksUpWritesOnceAndDoesNothingOnARerun(t *testing.T) {
	_, file := writeFixtureConfig(t)
	entry := repoEntry{Type: "forgejo", Host: "https://codeberg.org", Owner: "me", Repo: "thing"}

	first, ok, err := updateConfigFile(file, []repoEntry{entry}, fixedNow)
	if err != nil || !ok || !first.changed {
		t.Fatalf("ok=%v changed=%v err=%v", ok, first.changed, err)
	}
	// The backup is named after the run's clock, not the wall clock: a test that asserted a
	// timestamp it did not control would be flaky, and a user comparing two runs wants the name
	// to say when.
	if want := file + ".20260823T195100Z.bak"; first.backup != want {
		t.Errorf("backup = %q, want %q", first.backup, want)
	}
	// The backup holds the file as it was — that is the whole point of taking one.
	if got := readFile(t, first.backup); got != fixtureConfig {
		t.Error("the backup is not the pre-write file")
	}

	written := readFile(t, file)
	mustContain(t, written, `{ "type": "forgejo", "host": "https://codeberg.org", "owner": "me", "repo": "thing" },`,
		"the entry did not reach the file")
	mustContain(t, written, "// Self-host demo: the frznforge repo itself.", "a comment was lost on the way to disk")

	second, ok, err := updateConfigFile(file, []repoEntry{entry}, fixedNow)
	if err != nil || !ok {
		t.Fatalf("re-run: ok=%v err=%v", ok, err)
	}
	if second.changed || second.backup != "" {
		t.Errorf("a no-op run wrote something: changed=%v backup=%q", second.changed, second.backup)
	}
	if readFile(t, file) != written {
		t.Error("the second run rewrote the file")
	}
}

func TestUpdateConfigFileWritesAFileTheRealLoaderAccepts(t *testing.T) {
	// The strongest thing that can be said about a written config: the loader that reads it from
	// now on takes it. A splice that produced `} {` or ate a bracket passes every string
	// assertion above and fails here.
	dir, file := writeFixtureConfig(t)
	entries := []repoEntry{
		{Type: "github", Owner: "Descent098", Repo: "ezcv", Releases: "provider"},
		{Type: "forgejo", Host: "https://codeberg.org", Owner: "me", Repo: "thing", Releases: "tags"},
	}
	if _, _, err := updateConfigFile(file, entries, fixedNow); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatalf("the config init just wrote does not load: %v", err)
	}
	// The original local source plus the two that were added, in file order.
	var types []string
	for _, s := range cfg.Repos {
		types = append(types, s.Type)
	}
	if !equalStrings(types, []string{"local", "github", "forgejo"}) {
		t.Errorf("sources = %v", types)
	}
}

func TestUpdateConfigFileReportsAMissingReposArrayRatherThanWriting(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, config.Filename)
	before := "{ \"owner\": { \"name\": \"x\", \"handle\": \"x\" } }\n"
	if err := os.WriteFile(file, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	_, ok, err := updateConfigFile(file, []repoEntry{ezcv}, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("a config with no repos array was reported as spliceable")
	}
	if readFile(t, file) != before {
		t.Error("the file was modified anyway")
	}
}

func TestBackupPathIsNamedWithAUTCTimestamp(t *testing.T) {
	// UTC, always: a .bak named in local time sorts wrongly next to one written from another
	// machine, and the name is the only record of when the copy was taken.
	got := backupPathFor("/x/frznforge.config.jsonc", fixedNow.Add(-16*3600*1e9))
	if got != "/x/frznforge.config.jsonc.20260823T035100Z.bak" {
		t.Errorf("backup path = %q", got)
	}
}

func TestTwoBackupsInTheSameSecondNeverOverwriteEachOther(t *testing.T) {
	// The timestamp has one-second resolution, so two quick runs would otherwise name the same
	// file and the second would clobber the first — losing the only copy of the original.
	_, file := writeFixtureConfig(t)
	first, _, err := updateConfigFile(file, []repoEntry{{Type: "github", Owner: "me", Repo: "one"}}, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := updateConfigFile(file, []repoEntry{{Type: "github", Owner: "me", Repo: "two"}}, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if first.backup != backupPathFor(file, fixedNow) {
		t.Errorf("first backup = %q", first.backup)
	}
	if second.backup == first.backup {
		t.Fatalf("both runs wrote %q", first.backup)
	}
	if got := readFile(t, first.backup); got != fixtureConfig {
		t.Error("the first backup no longer holds the original")
	}
	mustContain(t, readFile(t, second.backup), `"repo": "one"`, "the second backup is not the state before the second write")
}

func TestABOMSurvivesAWrite(t *testing.T) {
	// An editor on Windows adds one, encoding/json refuses a file that starts with one, and the
	// promise of this command is that everything it did not edit is unchanged. Dropping the BOM
	// would show up as a whole-file diff in the user's next commit.
	dir := t.TempDir()
	file := filepath.Join(dir, config.Filename)
	if err := os.WriteFile(file, append([]byte{0xEF, 0xBB, 0xBF}, fixtureConfig...), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := updateConfigFile(file, []repoEntry{ezcv}, fixedNow); err != nil {
		t.Fatal(err)
	}
	written := readFile(t, file)
	if !strings.HasPrefix(written, "\xEF\xBB\xBF") {
		t.Error("the byte order mark was dropped")
	}
	if _, err := config.Load(dir); err != nil {
		t.Errorf("the file with its BOM restored no longer loads: %v", err)
	}
}

/* ---- what cannot reach a config file -------------------------------------- */

func TestRenderEntryEscapesWhatWouldBreakOutOfAStringLiteral(t *testing.T) {
	// Nothing below can arrive from a listing (assertSafeField refuses it first), but this is the
	// last line before bytes hit the user's file, and an unescaped newline or quote leaves that
	// file unparseable with only the .bak intact.
	for _, c := range []struct{ in, want string }{
		{"a\nb", `a\nb`},
		{"a\rb", `a\rb`},
		{"a\tb", `a\tb`},
		{`a"b`, `a\"b`},
		{`a\b`, `a\\b`},
		{"ab", `a\u0001b`},
	} {
		got := renderEntry(repoEntry{Type: "github", Owner: "o", Repo: c.in})
		mustContain(t, got, `"repo": "`+c.want+`"`, "a control character reached the file unescaped")
		if strings.ContainsAny(got, "\n\r") {
			t.Errorf("%q rendered to a multi-line entry: %q", c.in, got)
		}
	}
	// < > & are NOT escaped: encoding/json would turn every URL in the config into < soup,
	// and this file is meant to be read and edited by hand afterwards.
	if got := renderEntry(repoEntry{Type: "github", Owner: "o", Repo: "a<b>&c"}); !strings.Contains(got, "a<b>&c") {
		t.Errorf("angle brackets were escaped into unreadability: %s", got)
	}
}

func TestEntriesForRefusesAListingSuppliedNameThatHasNoBusinessInAConfig(t *testing.T) {
	// The listing is remote input and entriesFor is the one place it becomes config text.
	hostile := remoteRepo{Name: "evil\n    \"path\": \"/etc/passwd\", \"slug\": \"pwned", FullName: "me/evil", Owner: "me"}
	_, err := entriesFor("github", "https://api.github.com", []remoteRepo{hostile}, "provider")
	if err == nil || !strings.Contains(err.Error(), "repository name that cannot go in a config file") {
		t.Errorf("hostile repository name → %v", err)
	}

	gl := remoteRepo{Name: "p", FullName: "g/p", Owner: "g", Project: "g/p\",\n  \"evil\": \""}
	_, err = entriesFor("gitlab", "https://gitlab.com", []remoteRepo{gl}, "provider")
	if err == nil || !strings.Contains(err.Error(), "project path that cannot go in a config file") {
		t.Errorf("hostile project path → %v", err)
	}

	// An owner is checked as well as a name: the two are written into the same line.
	bad := remoteRepo{Name: "ok", FullName: "x/ok", Owner: "a\"b"}
	if _, err := entriesFor("github", "https://api.github.com", []remoteRepo{bad}, "provider"); err == nil {
		t.Error("a hostile owner was accepted")
	}
}

func TestEntriesForRefusesAHostThatCouldBreakOutOfAStringLiteral(t *testing.T) {
	plain := testRepo("me/alpha")
	cases := []struct{ host, want string }{
		{"https://x.dev/a\nb", "not a usable host"},
		{"https://x.dev/'", "not a usable host"}, // a quote of any kind, checked as text
		{"ftp://x.dev", "host must be http or https"},
		{"https://user:pw@x.dev", "host must not carry credentials"},
		{"x.dev", "not a URL"}, // no scheme at all
	}
	for _, c := range cases {
		_, err := entriesFor("gitea", c.host, []remoteRepo{plain}, "provider")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("host %q → %v, want %q", c.host, err, c.want)
		}
	}
}
