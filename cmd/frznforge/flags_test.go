package main

// `frznforge init`'s argument parsing — the port of cli.test.ts's `parseArgs` block.
//
// The parser is where a mistyped command line either becomes a clear message or becomes a run
// that quietly does the wrong thing, so every branch below is a failure a user could hit.

import (
	"strings"
	"testing"
)

func TestParseInitFlagsReadsBothValueForms(t *testing.T) {
	// `--key=value` and `--key value` have to mean the same thing: the help text uses the first
	// and shells complete the second, and a user who mixes them should not have to care.
	flags, errs := parseInitFlags([]string{
		"--provider=github", "--account", "Descent098", "--select=1,3", "--releases", "tags", "--print",
	})
	if len(errs) != 0 {
		t.Fatalf("clean command line reported errors: %v", errs)
	}
	if flags.Provider != "github" || flags.Account != "Descent098" || flags.Select != "1,3" {
		t.Errorf("provider/account/select = %q/%q/%q", flags.Provider, flags.Account, flags.Select)
	}
	if flags.Releases != "tags" || !flags.ReleasesSet {
		t.Errorf("releases = %q (set: %v)", flags.Releases, flags.ReleasesSet)
	}
	if !flags.Print || flags.Yes {
		t.Errorf("print/yes = %v/%v; --yes was never given", flags.Print, flags.Yes)
	}
}

func TestParseInitFlagsAcceptsBothHelpSpellings(t *testing.T) {
	// -h is the one single-dash form the parser takes. Without it, `frznforge init -h` falls into
	// the "unexpected argument" branch and answers a request for help with an error.
	for _, argv := range [][]string{{"-h"}, {"--help"}, {"--provider=github", "-h"}} {
		flags, errs := parseInitFlags(argv)
		if !flags.Help {
			t.Errorf("%v did not ask for help", argv)
		}
		if len(errs) != 0 {
			t.Errorf("%v reported %v", argv, errs)
		}
	}
}

func TestParseInitFlagsStripsATrailingSlashFromHost(t *testing.T) {
	// The host is written into the config and compared against the provider default. A stray
	// slash would make `https://codeberg.org/` a different host from `https://codeberg.org`, so
	// re-running init would add every repository a second time.
	flags, _ := parseInitFlags([]string{"--host=https://codeberg.org/"})
	if flags.Host != "https://codeberg.org" {
		t.Errorf("host = %q", flags.Host)
	}
}

func TestParseInitFlagsCollectsEveryMistakeAtOnce(t *testing.T) {
	// Reporting only the first mistake makes fixing a bad command line a guessing game: the user
	// corrects one flag, re-runs, and is told about the next one.
	_, errs := parseInitFlags([]string{"--nope", "--provider=bitbucket", "--releases=maybe", "extra"})
	if len(errs) != 4 {
		t.Fatalf("want all four mistakes, got %d: %v", len(errs), errs)
	}
	joined := strings.Join(errs, "\n")
	for _, want := range []string{
		"unknown option: --nope",
		"--provider must be one of github, gitlab, gitea, forgejo",
		"--releases must be 'provider' or 'tags'",
		"unexpected argument: extra",
	} {
		mustContain(t, joined, want, "a mistake was not reported")
	}
}

func TestParseInitFlagsNeedsAValueAndWillNotStealTheNextFlag(t *testing.T) {
	// `--account --yes` is a forgotten account, not an account called "--yes". Swallowing the
	// next flag would silently drop --yes as well, and the run would then stop at a confirmation
	// prompt the user thought they had answered.
	flags, errs := parseInitFlags([]string{"--account", "--yes"})
	if len(errs) != 1 || errs[0] != "--account needs a value" {
		t.Fatalf("errors = %v", errs)
	}
	if !flags.Yes {
		t.Error("--yes was eaten as the value of --account")
	}
	if flags.Account != "" {
		t.Errorf("account = %q", flags.Account)
	}

	_, trailing := parseInitFlags([]string{"--provider"})
	if len(trailing) != 1 || trailing[0] != "--provider needs a value" {
		t.Errorf("a value flag at the end of the line: %v", trailing)
	}
}

func TestParseInitFlagsRejectsAValueOnABooleanFlag(t *testing.T) {
	// `--print=please` is a user who thinks --print takes an argument. Accepting it as truthy
	// would hide the misunderstanding until the next flag they invent behaves differently.
	_, errs := parseInitFlags([]string{"--print=please"})
	if len(errs) != 1 || errs[0] != "--print does not take a value" {
		t.Fatalf("errors = %v", errs)
	}
	// `=false` is the one value that is honoured, so a wrapper script can turn a flag back off.
	flags, clean := parseInitFlags([]string{"--print=false", "--yes=true"})
	if len(clean) != 0 {
		t.Fatalf("errors = %v", clean)
	}
	if flags.Print || !flags.Yes {
		t.Errorf("print/yes = %v/%v", flags.Print, flags.Yes)
	}
}

func TestParseInitFlagsReadsTheBrowserUIFlags(t *testing.T) {
	flags, errs := parseInitFlags([]string{"--web", "--port=4173", "--no-open"})
	if len(errs) != 0 {
		t.Fatalf("errors = %v", errs)
	}
	if !flags.Web || flags.Port != 4173 || !flags.NoOpen {
		t.Errorf("web/port/no-open = %v/%d/%v", flags.Web, flags.Port, flags.NoOpen)
	}

	bare, _ := parseInitFlags([]string{})
	if bare.Web || bare.NoOpen || bare.Port != 0 {
		t.Errorf("a bare init should ask for none of the browser flags, got %+v", bare)
	}
	// 0 is meaningful — "pick a free ephemeral port" — which is how the e2e harness starts the
	// wizard without racing another process for a fixed number.
	zero, zeroErrs := parseInitFlags([]string{"--web", "--port", "0"})
	if len(zeroErrs) != 0 || zero.Port != 0 {
		t.Errorf("--port 0: port=%d errors=%v", zero.Port, zeroErrs)
	}
}

func TestParseInitFlagsRejectsAPortThatIsNotAPort(t *testing.T) {
	// Each of these would otherwise reach net/http as 0 and bind a random port, so the user
	// would be told the wizard is running somewhere they did not ask for.
	for _, bad := range []string{"--port=x", "--port=70000", "--port=80.5", "--port=-1", "--port="} {
		_, errs := parseInitFlags([]string{bad})
		if len(errs) != 1 || !strings.Contains(errs[0], "--port must be a whole number between 0 and 65535") {
			t.Errorf("%s: errors = %v", bad, errs)
		}
	}
}

func TestParseInitFlagsRefusesWebTogetherWithPrint(t *testing.T) {
	// --print promises to touch nothing; --web opens a UI whose whole purpose is to write the
	// config. Honouring both would mean silently picking one, and the user only finds out which
	// by looking at their config afterwards.
	_, errs := parseInitFlags([]string{"--web", "--print"})
	if len(errs) != 1 {
		t.Fatalf("errors = %v", errs)
	}
	mustContain(t, errs[0], "--web and --print cannot be combined", "the refusal does not name the conflict")

	for _, argv := range [][]string{{"--web"}, {"--print"}} {
		if _, alone := parseInitFlags(argv); len(alone) != 0 {
			t.Errorf("%v on its own reported %v", argv, alone)
		}
	}
}

func TestSelectSetTellsAnEmptySelectionFromNoSelectAtAll(t *testing.T) {
	// `--select=` means "none, and do not ask me" — a legitimate answer for a script probing an
	// account. No --select at all means "ask". Collapsing the two would either hang a script or
	// stop asking a person.
	empty, _ := parseInitFlags([]string{"--select="})
	if !empty.SelectSet || empty.Select != "" {
		t.Errorf("--select= : set=%v value=%q", empty.SelectSet, empty.Select)
	}
	absent, _ := parseInitFlags([]string{})
	if absent.SelectSet {
		t.Error("no --select at all was recorded as a selection")
	}
}

func TestPlanFromFlagsNamesExactlyWhatIsStillMissing(t *testing.T) {
	// The missing list is what a non-TTY run is refused over, and what the refusal tells the user
	// to add. A wrong entry here sends someone to fix a flag that was already correct.
	cases := []struct {
		name string
		argv []string
		want []string
	}{
		{"nothing given", nil, []string{"--provider", "--account", "--select"}},
		{"self-hosted needs a host", []string{"--provider=gitea", "--account=me", "--select=all"},
			[]string{"--host"}},
		{"github infers its host", []string{"--provider=github", "--account=me", "--select=all"}, nil},
		{"gitea with a host is complete", []string{"--provider=forgejo", "--host=https://codeberg.org", "--account=me", "--select="}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			flags, errs := parseInitFlags(c.argv)
			if len(errs) != 0 {
				t.Fatalf("parse: %v", errs)
			}
			plan, missing := planFromFlags(flags)
			if !equalStrings(missing, c.want) {
				t.Fatalf("missing = %v, want %v", missing, c.want)
			}
			if len(c.want) == 0 && plan.host == "" {
				t.Error("a complete plan has no host to query")
			}
		})
	}
}

func TestPlanFromFlagsDefaultsReleasesToProvider(t *testing.T) {
	// A fully-flagged run asks nothing, so the releases question never gets put. "provider" is
	// the documented default; leaving it empty would write `"releases": ""` into the config.
	flags, _ := parseInitFlags([]string{"--provider=github", "--account=me", "--select=all"})
	plan, _ := planFromFlags(flags)
	if plan.releases != "provider" {
		t.Errorf("releases = %q", plan.releases)
	}
}

func TestInitUsageDocumentsTheSelectionGrammarAndTheBrowserFlags(t *testing.T) {
	// The usage text is the only place the `all-…` grammar is written down for someone who did
	// not read the docs, so it has to carry the codes themselves rather than a vague mention.
	usage := initUsage()
	for _, want := range []string{excludeHelp(), "all-nf", "--web", "--port=<n>", "--no-open", "--select=<spec>"} {
		mustContain(t, usage, want, "init --help does not document a flag it accepts")
	}
	// Token variables are named so a user can set the right one; the values never appear here
	// because there are none to print.
	mustContain(t, usage, "FRZNFORGE_GITHUB_TOKEN", "init --help does not name the token variables")
}

func TestNonTTYMessageTellsAPipedRunHowToSucceed(t *testing.T) {
	// This is what CI sees instead of a hang. It is worth nothing unless it carries a command
	// line that actually works, so it names the three flags that make a run answer-free.
	msg := nonTTYMessage()
	for _, want := range []string{"--provider=github", "--account=", "--select=all", "docs/user/importing.md"} {
		mustContain(t, msg, want, "the non-TTY guidance is not actionable")
	}
}
