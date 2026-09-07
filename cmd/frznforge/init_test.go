package main

// `frznforge init` end to end — the port of cli.test.ts's `main` block, plus the interactive
// picker the TypeScript could only exercise through its two callbacks.
//
// Every run below goes through initCmd or run(), so what is asserted is what a person typing the
// command would get: the message, the exit (an error or nil), and the file on disk afterwards.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"frznforge/internal/ingest"
)

/* ---- refusing to hang ----------------------------------------------------- */

func TestInitRefusesToPromptWhenStdinIsNotATerminal(t *testing.T) {
	// This is what CI sees instead of a hang. A tool that blocks on an unanswerable question in a
	// pipeline looks like an infrastructure problem, and the log says nothing at all.
	tio := newTestIo(t.TempDir())
	err := initCmd(nil, tio.Io)
	if err == nil {
		t.Fatal("a piped init with no flags went ahead")
	}
	if err.Error() != nonTTYMessage() {
		t.Errorf("error =\n%s", err)
	}
}

func TestInitStillBailsOutWhenOnlySomeFlagsAreGiven(t *testing.T) {
	// gitea is self-hosted, so --host is not optional. Guessing a host would query somebody
	// else's server with the user's account name.
	tio := newTestIo(t.TempDir())
	err := initCmd([]string{"--provider=gitea", "--account=me", "--select=all"}, tio.Io)
	if err == nil || err.Error() != nonTTYMessage() {
		t.Errorf("error = %v", err)
	}
}

func TestInitReportsBadFlagsWithItsOwnHelp(t *testing.T) {
	tio := newTestIo(t.TempDir())
	err := initCmd([]string{"--provider=bitbucket"}, tio.Io)
	if err == nil {
		t.Fatal("an unknown provider was accepted")
	}
	mustContain(t, err.Error(), "--provider must be one of", "the mistake is not named")
	// The help follows the mistake, so the user does not have to run a second command to find
	// out what the valid values were.
	mustContain(t, err.Error(), "frznforge init — add repositories", "the refusal does not carry the usage")
}

func TestInitPrintsItsOwnHelpWithoutTouchingAnything(t *testing.T) {
	tio := newTestIo(t.TempDir())
	if err := initCmd([]string{"--help"}, tio.Io); err != nil {
		t.Fatal(err)
	}
	mustContain(t, tio.stdout(), "--select=<spec>", "init --help printed something else")
	if tio.stderr() != "" {
		t.Errorf("help went to stderr: %s", tio.stderr())
	}
}

func TestInitRejectsWebWithPrintBeforeOpeningAnything(t *testing.T) {
	// Caught in the parser, so no port is bound and no browser is launched on the way to the
	// refusal — which is the whole reason the check lives there rather than in the wizard.
	tio := newTestIo(t.TempDir())
	err := initCmd([]string{"--web", "--print"}, tio.Io)
	if err == nil {
		t.Fatal("--web --print was accepted")
	}
	mustContain(t, err.Error(), "--web and --print cannot be combined", "the conflict is not explained")
}

/* ---- a whole run, no terminal and no network ------------------------------ */

func TestInitRunsEndToEndFromFlagsAlone(t *testing.T) {
	// The scriptable form: every question answered on the command line, so the run needs no
	// terminal and behaves the same in CI as on a desk.
	dir, file := writeFixtureConfig(t)
	srv := fakeProvider(t, map[string]any{
		"/api/v1/users/me/repos": []any{
			listingItem("me", "beta", map[string]any{"description": "B", "archived": true}),
			listingItem("me", "alpha", map[string]any{"description": "A"}),
		},
	})
	tio := newTestIo(dir)
	tio.Client = srv.Client()

	err := initCmd([]string{
		"--provider=forgejo", "--host=" + srv.URL, "--account=me",
		"--select=alpha", "--releases=tags", "--yes",
	}, tio.Io)
	if err != nil {
		t.Fatalf("init: %v", err)
	}

	written := readFile(t, file)
	mustContain(t, written, `"type": "forgejo", "host": "`+srv.URL+`", "owner": "me", "repo": "alpha", "releases": "tags"`,
		"the selected repository did not reach the config")
	mustNotContain(t, written, `"repo": "beta"`, "a repository the user did not select was added")
	mustContain(t, written, "// Self-host demo: the frznforge repo itself.", "the user's comment was lost")
	// The backup is announced, because it is the only way back if the splice went somewhere
	// unexpected and the user did not have the file in git.
	mustContain(t, tio.stdout(), "Backup: ", "the run did not say where the backup went")
	mustContain(t, tio.stdout(), "Next: frznforge build", "the run does not say what to do next")
}

func TestInitIsIdempotent(t *testing.T) {
	// Re-running init after adding one repository is the normal workflow. The second run must
	// say "nothing to do" rather than writing a second copy or another backup.
	dir, file := writeFixtureConfig(t)
	srv := fakeProvider(t, map[string]any{
		"/api/v1/users/me/repos": []any{listingItem("me", "alpha", nil)},
	})
	argv := []string{"--provider=forgejo", "--host=" + srv.URL, "--account=me", "--select=all", "--yes"}

	first := newTestIo(dir)
	first.Client = srv.Client()
	if err := initCmd(argv, first.Io); err != nil {
		t.Fatal(err)
	}
	afterFirst := readFile(t, file)

	second := newTestIo(dir)
	second.Client = srv.Client()
	if err := initCmd(argv, second.Io); err != nil {
		t.Fatal(err)
	}
	if readFile(t, file) != afterFirst {
		t.Error("the second run changed the config")
	}
	mustContain(t, second.stdout(), "already in", "the second run did not say why it did nothing")
	mustNotContain(t, second.stdout(), "Backup: ", "a no-op run still took a backup")
}

func TestInitNeverWritesWithPrint(t *testing.T) {
	// --print promises to touch nothing, and it has to keep that promise even alongside --yes:
	// somebody scripting a dry run should not be one stray flag away from editing their config.
	dir, file := writeFixtureConfig(t)
	srv := fakeProvider(t, map[string]any{"/users/me/repos": []any{listingItem("me", "alpha", nil)}})
	tio := newTestIo(dir)
	tio.Client = srv.Client()

	if err := initCmd([]string{"--provider=github", "--host=" + srv.URL, "--account=me", "--select=all", "--print", "--yes"}, tio.Io); err != nil {
		t.Fatal(err)
	}
	if readFile(t, file) != fixtureConfig {
		t.Error("--print wrote to the config")
	}
	mustContain(t, tio.stdout(), `"repo": "alpha"`, "--print did not print the snippet it exists to print")
}

func TestInitDoesNotWriteWhenConfirmationIsImpossibleAndYesIsAbsent(t *testing.T) {
	// No terminal to confirm at and no --yes: the only safe answer is to print what would happen
	// and say which flag makes it happen.
	dir, file := writeFixtureConfig(t)
	srv := fakeProvider(t, map[string]any{"/users/me/repos": []any{listingItem("me", "alpha", nil)}})
	tio := newTestIo(dir)
	tio.Client = srv.Client()

	if err := initCmd([]string{"--provider=github", "--host=" + srv.URL, "--account=me", "--select=all"}, tio.Io); err != nil {
		t.Fatal(err)
	}
	if readFile(t, file) != fixtureConfig {
		t.Error("the config was written without a confirmation")
	}
	mustContain(t, tio.stdout(), "--yes", "the run does not say how to go ahead")
}

func TestInitNeverPromptsOnATTYWhenTheFlagsAlreadyDescribeTheRun(t *testing.T) {
	// stdin is empty here, so any prompt reached would end the run with errInputClosed. --print
	// and --yes are the scriptable forms; blocking on "Where should releases come from?" would
	// make a terminal behave differently from CI over the same command line.
	dir, file := writeFixtureConfig(t)
	srv := fakeProvider(t, map[string]any{"/users/me/repos": []any{listingItem("me", "alpha", nil)}})

	printing := newTestIo(dir)
	printing.Client, printing.IsTTY = srv.Client(), true
	if err := initCmd([]string{"--provider=github", "--host=" + srv.URL, "--account=me", "--select=all", "--print"}, printing.Io); err != nil {
		t.Fatalf("a fully-flagged --print run stopped at a prompt: %v", err)
	}
	if readFile(t, file) != fixtureConfig {
		t.Error("--print wrote to the config")
	}

	writing := newTestIo(dir)
	writing.Client, writing.IsTTY = srv.Client(), true
	if err := initCmd([]string{"--provider=github", "--host=" + srv.URL, "--account=me", "--select=all", "--yes"}, writing.Io); err != nil {
		t.Fatalf("a fully-flagged --yes run stopped at a prompt: %v", err)
	}
	mustContain(t, readFile(t, file), `"repo": "alpha"`, "--yes did not write")
}

func TestInitTargetsTheConfigNamedByTheConfigFlag(t *testing.T) {
	// --config is how someone edits a site that is not the directory they are standing in. If it
	// were ignored, the run would silently walk up and edit whatever config it found instead.
	elsewhere, file := writeFixtureConfig(t)
	srv := fakeProvider(t, map[string]any{"/users/me/repos": []any{listingItem("me", "alpha", nil)}})
	tio := newTestIo(t.TempDir()) // a working directory with no config in it or above it
	tio.Client = srv.Client()

	if err := initCmd([]string{
		"--provider=github", "--host=" + srv.URL, "--account=me", "--select=all", "--yes", "--config=" + file,
	}, tio.Io); err != nil {
		t.Fatal(err)
	}
	mustContain(t, readFile(t, file), `"repo": "alpha"`, "--config did not reach the named file")
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 2 {
		t.Errorf("the target directory holds %d files; want the config and one .bak", len(entries))
	}
}

func TestInitEditsTheConfigUnderTheRootFlag(t *testing.T) {
	// --root is how the wizard, a script or a run from elsewhere points init at a site that is
	// not the current directory. Ignoring it would send the search up from the wrong place and
	// edit whichever config happened to be above the caller instead.
	site, file := writeFixtureConfig(t)
	srv := fakeProvider(t, map[string]any{"/users/me/repos": []any{listingItem("me", "alpha", nil)}})
	tio := newTestIo(t.TempDir()) // a working directory with no config at or above it
	tio.Client = srv.Client()

	if err := initCmd([]string{
		"--provider=github", "--host=" + srv.URL, "--account=me", "--select=all", "--yes", "--root=" + site,
	}, tio.Io); err != nil {
		t.Fatal(err)
	}
	mustContain(t, readFile(t, file), `"repo": "alpha"`, "--root did not reach the config it named")
}

func TestInitFallsBackToPastingWhenThereIsNoConfigToEdit(t *testing.T) {
	// A user who has not scaffolded a site yet still gets something usable out of the run rather
	// than an error about a file they were never told to create.
	srv := fakeProvider(t, map[string]any{"/users/me/repos": []any{listingItem("me", "alpha", nil)}})
	tio := newTestIo(t.TempDir())
	tio.Client = srv.Client()

	if err := initCmd([]string{"--provider=github", "--host=" + srv.URL, "--account=me", "--select=all", "--yes"}, tio.Io); err != nil {
		t.Fatal(err)
	}
	mustContain(t, tio.stdout(), "paste the snippet above", "the run does not say what to do with the snippet")
	mustContain(t, tio.stdout(), `"repos": [`, "no snippet was printed to paste")
}

/* ---- what the run says about the selection -------------------------------- */

func TestInitReportsWhatAFilterDropped(t *testing.T) {
	// Adding an account minus its forks and archives is the flag's whole purpose, and a run that
	// silently loses three of four repositories is a run nobody audits.
	srv := fakeProvider(t, map[string]any{
		"/users/me/repos": []any{
			listingItem("me", "alpha", nil),
			listingItem("me", "beta", map[string]any{"fork": true}),
			listingItem("me", "delta", map[string]any{"archived": true}),
			listingItem("me", "gamma", map[string]any{"fork": true}),
		},
	})
	tio := newTestIo(t.TempDir())
	tio.Client = srv.Client()

	if err := initCmd([]string{"--provider=github", "--host=" + srv.URL, "--account=me", "--select=all-nfna", "--print"}, tio.Io); err != nil {
		t.Fatal(err)
	}
	printed := tio.stdout()
	mustContain(t, printed, "selected 1 of 4 (excluded 2 forks, 1 archived)", "the exclusions were not reported")
	mustContain(t, printed, `"repo": "alpha"`, "the surviving repository is missing")
	for _, dropped := range []string{`"repo": "beta"`, `"repo": "delta"`, `"repo": "gamma"`} {
		mustNotContain(t, printed, dropped, "an excluded repository was added anyway")
	}
}

func TestInitLeavesAnUnfilteredRunSilentAboutExclusions(t *testing.T) {
	// Plain `all` filtered nothing. Printing "excluded 0 forks" on every ordinary run trains the
	// user to skip the line that matters when it is not zero.
	srv := fakeProvider(t, map[string]any{
		"/users/me/repos": []any{listingItem("me", "alpha", map[string]any{"fork": true})},
	})
	tio := newTestIo(t.TempDir())
	tio.Client = srv.Client()

	if err := initCmd([]string{"--provider=github", "--host=" + srv.URL, "--account=me", "--select=all", "--print"}, tio.Io); err != nil {
		t.Fatal(err)
	}
	mustNotContain(t, tio.stdout(), "excluded", "an unfiltered run talked about exclusions")
	mustContain(t, tio.stdout(), `"repo": "alpha"`, "a fork was dropped by a selection that filters nothing")
}

func TestInitRejectsAnUnknownFilterWithTheKnownOnes(t *testing.T) {
	srv := fakeProvider(t, map[string]any{"/users/me/repos": []any{listingItem("me", "alpha", nil)}})
	tio := newTestIo(t.TempDir())
	tio.Client = srv.Client()

	err := initCmd([]string{"--provider=github", "--host=" + srv.URL, "--account=me", "--select=all-nx", "--print"}, tio.Io)
	if err == nil {
		t.Fatal("all-nx was accepted")
	}
	// The `--select:` prefix says which flag was wrong; the rest says what would have been right.
	want := "--select: unknown filter 'nx' in 'all-nx'; known: " + excludeHelp() + ", and no listed repository is named 'all-nx'"
	if err.Error() != want {
		t.Errorf("error = %q\n want %q", err.Error(), want)
	}
}

func TestInitReportsABadSelectWithoutTouchingTheConfig(t *testing.T) {
	dir, file := writeFixtureConfig(t)
	srv := fakeProvider(t, map[string]any{"/users/me/repos": []any{listingItem("me", "alpha", nil)}})
	tio := newTestIo(dir)
	tio.Client = srv.Client()

	err := initCmd([]string{"--provider=github", "--host=" + srv.URL, "--account=me", "--select=42", "--yes"}, tio.Io)
	if err == nil {
		t.Fatal("index 42 of a one-repository listing was accepted")
	}
	mustContain(t, err.Error(), "--select: 42 is out of range (1-1)", "the range was not reported")
	if readFile(t, file) != fixtureConfig {
		t.Error("a rejected selection still wrote to the config")
	}
}

func TestInitRefusesAnEmptySelectionWhenAFilterExcludedEverything(t *testing.T) {
	// Writing nothing after `--select=all-nf --yes` would look like success. The user asked for
	// an account's repositories and got none, and they need to know why before they move on.
	dir, file := writeFixtureConfig(t)
	srv := fakeProvider(t, map[string]any{
		"/users/me/repos": []any{
			listingItem("me", "alpha", map[string]any{"fork": true}),
			listingItem("me", "beta", map[string]any{"fork": true}),
		},
	})
	tio := newTestIo(dir)
	tio.Client = srv.Client()

	err := initCmd([]string{"--provider=github", "--host=" + srv.URL, "--account=me", "--select=all-nf", "--yes"}, tio.Io)
	if err == nil {
		t.Fatal("a selection that excluded everything succeeded")
	}
	mustContain(t, err.Error(), "all-nf excluded all 2 repositories (2 forks) — nothing to add.",
		"the refusal does not say what was excluded")
	if readFile(t, file) != fixtureConfig {
		t.Error("the config was touched")
	}
}

func TestInitAcceptsAnExplicitlyEmptySelectionAsAnAnswer(t *testing.T) {
	// `--select=none` is a deliberate "list them, add nothing" — a probe, not a mistake. Turning
	// it into an error would make it useless.
	dir, file := writeFixtureConfig(t)
	srv := fakeProvider(t, map[string]any{"/users/me/repos": []any{listingItem("me", "alpha", nil)}})
	tio := newTestIo(dir)
	tio.Client = srv.Client()

	if err := initCmd([]string{"--provider=github", "--host=" + srv.URL, "--account=me", "--select=none", "--yes"}, tio.Io); err != nil {
		t.Fatalf("--select=none: %v", err)
	}
	mustContain(t, tio.stdout(), "Nothing selected — no changes made.", "the run did not say it did nothing")
	if readFile(t, file) != fixtureConfig {
		t.Error("--select=none wrote to the config")
	}
}

func TestInitSaysSoWhenAnAccountHasNothingVisible(t *testing.T) {
	// An account with no public repositories and no token set looks identical to a typo, so the
	// message has to name the account AND mention the thing that would change the answer.
	srv := fakeProvider(t, map[string]any{"/users/me/repos": []any{}})
	tio := newTestIo(t.TempDir())
	tio.Client = srv.Client()

	err := initCmd([]string{"--provider=github", "--host=" + srv.URL, "--account=me", "--select=all", "--print"}, tio.Io)
	if err == nil {
		t.Fatal("an empty listing succeeded")
	}
	mustContain(t, err.Error(), "no repositories visible for me", "the account is not named")
	mustContain(t, err.Error(), "A token may reveal private ones", "the tokenless case is not explained")
}

func TestInitPutsTheTruncationWarningWhereAScriptWillSeeIt(t *testing.T) {
	// listRepos reports a capped page walk through a callback, and init wires that callback to
	// stderr. If it were wired to stdout — or nowhere — a run whose listing was cut short would
	// look complete, and `--select=1,3,5-8` against it would silently pick other repositories.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		page := make([]any, 50) // gitea's page size: a full page is what makes the walk continue
		for i := range page {
			page[i] = listingItem("me", fmt.Sprintf("r%03d", i), nil)
		}
		_ = json.NewEncoder(w).Encode(page)
	}))
	defer srv.Close()

	tio := newTestIo(t.TempDir())
	tio.Client = srv.Client()
	// `none` ends the run right after the listing, so this test costs ten requests and no
	// rendering of five hundred entries.
	if err := initCmd([]string{"--provider=gitea", "--host=" + srv.URL, "--account=me", "--select=none"}, tio.Io); err != nil {
		t.Fatal(err)
	}
	mustContain(t, tio.stderr(), "listing stopped after 500 repositories", "the truncation warning did not reach stderr")
	mustNotContain(t, tio.stdout(), "listing stopped", "the warning went to stdout, where a piped snippet would swallow it")
}

/* ---- secrets and hostile input -------------------------------------------- */

func TestInitKeepsTheTokenValueOutOfEverythingItPrints(t *testing.T) {
	// The token is read, used in a header and reported by variable name. Any path that echoed it
	// — a URL in an error, a debug line, the token status — would put it in a terminal
	// transcript the user may well paste somewhere.
	srv := fakeProvider(t, map[string]any{"/users/me/repos": []any{listingItem("me", "alpha", nil)}})
	tio := newTestIo(t.TempDir())
	tio.Client = srv.Client()
	tio.Env = ingest.Env{"GITHUB_TOKEN": secret}

	if err := initCmd([]string{"--provider=github", "--host=" + srv.URL, "--account=me", "--select=all", "--print"}, tio.Io); err != nil {
		t.Fatal(err)
	}
	mustContain(t, tio.everything(), "$GITHUB_TOKEN", "the run does not say which variable it used")
	mustNotContain(t, tio.everything(), secret, "the token was printed")
	mustContain(t, tio.stdout(), `"repo": "alpha"`, "the run produced no snippet")
}

func TestInitStopsAHostileListingBeforeItTouchesTheFile(t *testing.T) {
	// A repository name arrives over the network and would end up inside a string literal in a
	// file the user reads for years. The check runs while the entries are built — before the
	// backup, before the write — so a rejected listing leaves the config exactly as it was.
	dir, file := writeFixtureConfig(t)
	srv := fakeProvider(t, map[string]any{
		"/users/me/repos": []any{listingItem("me", "ok\n    \"path\": \"/etc/passwd", nil)},
	})
	tio := newTestIo(dir)
	tio.Client = srv.Client()

	err := initCmd([]string{"--provider=github", "--host=" + srv.URL, "--account=me", "--select=all", "--yes"}, tio.Io)
	if err == nil {
		t.Fatal("a hostile repository name was written")
	}
	mustContain(t, err.Error(), "cannot go in a config file", "the refusal does not say what was wrong")
	if readFile(t, file) != fixtureConfig {
		t.Error("the config was modified")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("the directory holds %d files; a refused run should not even take a backup", len(entries))
	}
}

/* ---- the interactive picker ----------------------------------------------- */

func TestInitAsksItsWayThroughAWholeRun(t *testing.T) {
	// The picker driven through the Io seam: a terminal is simulated by IsTTY plus a script on
	// stdin, so the whole flow — provider menu, host, account, listing, selection, releases,
	// confirmation — is exercised without a process or a terminal.
	dir, file := writeFixtureConfig(t)
	srv := fakeProvider(t, map[string]any{
		"/api/v1/users/me/repos": []any{
			listingItem("me", "alpha", map[string]any{"description": "the first one"}),
			listingItem("me", "beta", map[string]any{"fork": true}),
		},
	})
	tio := newTestIo(dir)
	tio.Client, tio.IsTTY = srv.Client(), true
	// 3 = Gitea (the third entry of the provider menu), then the instance URL, the account, the
	// repository to add, "annotated git tags" for releases, and finally the confirmation.
	tio.In = strings.NewReader("3\n" + srv.URL + "\nme\n1\n2\ny\n")

	if err := initCmd(nil, tio.Io); err != nil {
		t.Fatalf("interactive init: %v", err)
	}

	out := tio.stdout()
	for _, want := range []string{
		"Provider",                        // the menu was put
		"Gitea instance URL",              // a self-hosted provider was asked for its host
		"Account (user or organisation)",  // …then for the account
		"1. me/alpha — the first one",     // the numbered listing carries the description
		"2. me/beta (fork)",               // …and the badges
		"Select repositories",             // the picker
		"Where should releases come from", // the follow-up question
		"Add 1 entry to the config? [y/N]",
	} {
		mustContain(t, out, want, "the interactive run skipped a step")
	}

	written := readFile(t, file)
	mustContain(t, written, `"type": "gitea", "host": "`+srv.URL+`", "owner": "me", "repo": "alpha", "releases": "tags"`,
		"the answers did not reach the config")
	mustNotContain(t, written, `"repo": "beta"`, "a repository the user did not pick was added")
}

func TestInitWritesNothingWhenTheConfirmationIsDeclined(t *testing.T) {
	// The confirmation is the last gate before someone's config is edited. Answering "n" has to
	// leave the file alone and still tell them how to go ahead if they change their mind.
	dir, file := writeFixtureConfig(t)
	srv := fakeProvider(t, map[string]any{"/api/v1/users/me/repos": []any{listingItem("me", "alpha", nil)}})
	tio := newTestIo(dir)
	tio.Client, tio.IsTTY = srv.Client(), true
	tio.In = strings.NewReader("n\n")

	if err := initCmd([]string{"--provider=forgejo", "--host=" + srv.URL, "--account=me", "--select=all"}, tio.Io); err != nil {
		t.Fatal(err)
	}
	if readFile(t, file) != fixtureConfig {
		t.Error("a declined confirmation still wrote")
	}
	mustContain(t, tio.stdout(), "Not written.", "the run does not say it stopped")
	mustContain(t, tio.stdout(), "--yes", "the run does not say how to skip the question next time")
}

func TestInitShowsExactlyWhatItIsAboutToDoBeforeAsking(t *testing.T) {
	// A confirmation is worth nothing if the answer is "yes" to an unnamed change, so the preview
	// lists every entry that would be added and every one already present.
	dir, file := writeFixtureConfig(t)
	srv := fakeProvider(t, map[string]any{
		"/api/v1/users/me/repos": []any{listingItem("me", "alpha", nil), listingItem("me", "beta", nil)},
	})
	argv := []string{"--provider=forgejo", "--host=" + srv.URL, "--account=me", "--select=alpha", "--yes"}

	first := newTestIo(dir)
	first.Client = srv.Client()
	if err := initCmd(argv, first.Io); err != nil {
		t.Fatal(err)
	}
	mustContain(t, first.stdout(), file, "the preview does not name the file being edited")
	mustContain(t, first.stdout(), `  + { "type": "forgejo"`, "the preview does not show what is being added")
	mustContain(t, first.stdout(), "a timestamped .bak copy is written first", "the preview does not mention the backup")

	// A second run that adds one repository and re-lists one already present marks each.
	second := newTestIo(dir)
	second.Client = srv.Client()
	if err := initCmd([]string{"--provider=forgejo", "--host=" + srv.URL, "--account=me", "--select=all", "--yes"}, second.Io); err != nil {
		t.Fatal(err)
	}
	mustContain(t, second.stdout(), `  + { "type": "forgejo", "host": "`+srv.URL+`", "owner": "me", "repo": "beta"`,
		"the second run does not show the addition")
	mustContain(t, second.stdout(), "(already present, skipped)", "the second run does not mark what it skipped")
}

func TestInitTurnsAClosedStdinIntoTheNonTTYGuidance(t *testing.T) {
	// IsTTY is Stat's character-device bit, and `frznforge init < NUL` on Windows hands over
	// exactly that. When the answers then run out, the user gets the same guidance a piped run
	// would have got up front rather than a bare "input ended".
	tio := newTestIo(t.TempDir())
	tio.IsTTY = true
	tio.In = strings.NewReader("") // a terminal by the check, empty by the read

	err := initCmd(nil, tio.Io)
	if err == nil {
		t.Fatal("a run with no answers succeeded")
	}
	if !errors.Is(err, errInputClosed) {
		t.Errorf("error is not errInputClosed: %v", err)
	}
	mustContain(t, err.Error(), "frznforge init is interactive", "the guidance was not attached")
	mustContain(t, err.Error(), "--select=all", "the guidance does not carry a working command line")
}

/* ---- the listing as the user sees it -------------------------------------- */

func TestDescribeRepoCarriesTheBadgesAndTrimsTheDescription(t *testing.T) {
	// This line is what someone picks a number against, so the badges have to be there: choosing
	// an archived fork by accident is a repository on their site they did not want.
	if got := describeRepo(testRepo("me/plain")); got != "me/plain" {
		t.Errorf("plain = %q", got)
	}
	flagged := testRepo("me/thing", isPrivate, isArchived, isFork)
	flagged.Description = "does things"
	if got := describeRepo(flagged); got != "me/thing (private, archived, fork) — does things" {
		t.Errorf("flagged = %q", got)
	}
	// Long descriptions are cut by RUNES, not bytes: half a UTF-8 sequence would put a
	// replacement character in the middle of the menu.
	wide := testRepo("me/wide")
	wide.Description = strings.Repeat("é", 100)
	got := describeRepo(wide)
	if runes := []rune(strings.TrimPrefix(got, "me/wide — ")); len(runes) != 70 {
		t.Errorf("description kept %d runes, want 70", len(runes))
	}
	if strings.ContainsRune(got, '�') {
		t.Error("the truncation split a character in half")
	}
}
