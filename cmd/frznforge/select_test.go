package main

// The `--select` grammar — the port of cli.test.ts's parseSelection, selectRepos, parseAllSpec,
// resolveSelection, reporting and "repositories whose name starts with all-" blocks.
//
// Everything here decides which repositories end up in someone's config file, so a wrong answer
// is not a cosmetic bug: it is either a repository silently missing from their site or one they
// deliberately excluded appearing on it.

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
)

/* ---- parseSelection ------------------------------------------------------- */

func TestParseSelectionHandlesAllNoneAndBlank(t *testing.T) {
	cases := []struct {
		spec  string
		count int
		want  []int
	}{
		{"all", 3, []int{0, 1, 2}},
		{"ALL", 2, []int{0, 1}}, // the prompt shows `all`; nobody should be punished for shift
		{"*", 2, []int{0, 1}},
		{"none", 3, nil},
		{"   ", 3, nil},
		{"", 3, nil},
	}
	for _, c := range cases {
		got, err := parseSelection(c.spec, c.count)
		if err != nil {
			t.Errorf("%q: %v", c.spec, err)
			continue
		}
		if !equalInts(got, c.want) {
			t.Errorf("%q → %v, want %v", c.spec, got, c.want)
		}
	}
}

func TestParseSelectionSortsAndDeduplicatesIndexes(t *testing.T) {
	// The order the entries are written in is the order this slice comes back in, so an
	// unsorted or duplicated result means a config that differs between two identical runs —
	// or one that lists the same repository twice.
	cases := []struct {
		spec  string
		count int
		want  []int
	}{
		{"1,3,5-8", 10, []int{0, 2, 4, 5, 6, 7}},
		{"3, 1 , 3", 4, []int{0, 2}}, // spaces around the commas, and a repeat
		{"2-2", 4, []int{1}},         // a range of one is still a range
	}
	for _, c := range cases {
		got, err := parseSelection(c.spec, c.count)
		if err != nil {
			t.Errorf("%q: %v", c.spec, err)
			continue
		}
		if !equalInts(got, c.want) {
			t.Errorf("%q → %v, want %v", c.spec, got, c.want)
		}
	}
}

func TestParseSelectionRejectsNonsenseWithTheRangeItHas(t *testing.T) {
	// "0 is out of range (1-3)" is the whole point: the listing is numbered from 1 on screen, and
	// a user who typed a 0 or a 4 needs the bounds repeated back, not a bare "invalid".
	cases := []struct {
		spec  string
		count int
		want  string
	}{
		{"abc", 3, "not a number: abc"},
		{"0", 3, "0 is out of range (1-3)"},
		{"4", 3, "4 is out of range (1-3)"},
		{"1-9", 3, "4 is out of range (1-3)"},
		{"5-2", 9, "range 5-2 runs backwards"},
	}
	for _, c := range cases {
		_, err := parseSelection(c.spec, c.count)
		if err == nil || err.Error() != c.want {
			t.Errorf("%q → %v, want %q", c.spec, err, c.want)
		}
	}
	if _, err := parseSelection("1;2", 3); err == nil {
		t.Error("a semicolon-separated list was accepted; only commas and whitespace separate")
	}
}

/* ---- names and indexes ---------------------------------------------------- */

// selectExplicit is the half of the grammar that is not an `all` form: resolveSelection claims
// `all`/`all-…` before this ever sees them, which is why "all" is not a case below.
func TestSelectExplicitTakesIndexesAndNamesInTheOrderAsked(t *testing.T) {
	repos := []remoteRepo{testRepo("me/alpha"), testRepo("me/beta"), testRepo("me/gamma")}
	cases := []struct {
		spec string
		want []string
	}{
		{"1,3", []string{"me/alpha", "me/gamma"}},
		// Named order is honoured, not re-sorted: the user wrote the order they want to read.
		{"gamma,alpha", []string{"me/gamma", "me/alpha"}},
		{"me/beta", []string{"me/beta"}},
		{"GAMMA", []string{"me/gamma"}},
		{"alpha alpha", []string{"me/alpha"}}, // named twice, added once
	}
	for _, c := range cases {
		got, err := selectExplicit(repos, c.spec)
		if err != nil {
			t.Errorf("%q: %v", c.spec, err)
			continue
		}
		if !equalStrings(names(got), c.want) {
			t.Errorf("%q → %v, want %v", c.spec, names(got), c.want)
		}
	}
}

func TestSelectExplicitReportsUnknownNamesAndOutOfRangeIndexes(t *testing.T) {
	repos := []remoteRepo{testRepo("me/alpha"), testRepo("me/beta"), testRepo("me/gamma")}
	if _, err := selectExplicit(repos, "delta"); err == nil || err.Error() != "no repository named delta" {
		t.Errorf("unknown name → %v", err)
	}
	if _, err := selectExplicit(repos, "9"); err == nil || err.Error() != "9 is out of range (1-3)" {
		t.Errorf("out-of-range index → %v", err)
	}
}

/* ---- all-<flags> ---------------------------------------------------------- */

func TestParseAllSpecReadsPlainAllAndNothingElse(t *testing.T) {
	for _, spec := range []string{"all", "  ALL  ", "*"} {
		codes, isAll, err := parseAllSpec(spec)
		if err != nil || !isAll || len(codes) != 0 {
			t.Errorf("%q → codes=%v isAll=%v err=%v", spec, codes, isAll, err)
		}
	}
	// Not an `all` form at all → isAll false with no error, which is the caller's cue to read the
	// spec as indexes or names. Reporting an error here would make `1,3` unselectable.
	for _, spec := range []string{"", "none", "1,3,5-8", "ezcv,sdu", "allsorts"} {
		_, isAll, err := parseAllSpec(spec)
		if isAll || err != nil {
			t.Errorf("%q was claimed by the filter grammar (isAll=%v err=%v)", spec, isAll, err)
		}
	}
}

func TestParseAllSpecReadsEveryCodeInTheTable(t *testing.T) {
	// Derived from the table, so adding a fourth filter without teaching the parser about it
	// fails here rather than at a user's prompt.
	for _, f := range excludeFilters {
		codes, isAll, err := parseAllSpec("all-" + f.code)
		if err != nil || !isAll || len(codes) != 1 || !codes[f.code] {
			t.Errorf("all-%s → codes=%v isAll=%v err=%v", f.code, codes, isAll, err)
		}
	}
}

func TestParseAllSpecReadsCodesConcatenatedDashedRepeatedAndInAnyCase(t *testing.T) {
	first, second := excludeFilters[0].code, excludeFilters[1].code
	both := []string{first, second}
	cases := []struct {
		spec string
		want []string
	}{
		{"all-" + first + second, both},
		{"all-" + first + "-" + second, both},
		{"all-" + second + "-" + first, both}, // order is irrelevant: they are a set
		{"ALL-" + strings.ToUpper(first) + second, both},
		{"all-" + first + "-" + first, []string{first}}, // repeated is not doubled
	}
	for _, c := range cases {
		codes, _, err := parseAllSpec(c.spec)
		if err != nil {
			t.Errorf("%q: %v", c.spec, err)
			continue
		}
		if !equalStrings(sortedKeys(codes), sortedStrings(c.want)) {
			t.Errorf("%q → %v, want %v", c.spec, sortedKeys(codes), sortedStrings(c.want))
		}
	}
	// Every code at once, still from the table.
	var all string
	for _, f := range excludeFilters {
		all += f.code
	}
	codes, _, err := parseAllSpec("all-" + all)
	if err != nil || len(codes) != len(excludeFilters) {
		t.Errorf("all-%s → %v (%v)", all, codes, err)
	}
}

func TestParseAllSpecNamesTheOffendingFlagAndTheKnownOnes(t *testing.T) {
	// The user's next move is to retype the flag, so the message has to say which two characters
	// were wrong and what the alternatives are.
	cases := []struct {
		spec string
		want string
	}{
		{"all-nx", "unknown filter 'nx' in 'all-nx'; known: " + excludeHelp()},
		// A good flag followed by a bad one points at the bad one, not at the whole spec.
		{"all-nfnx", "unknown filter 'nx' in 'all-nfnx'; known: " + excludeHelp()},
		{"all-", "'all-' names no filter; known: " + excludeHelp()},
	}
	for _, c := range cases {
		_, isAll, err := parseAllSpec(c.spec)
		if !isAll {
			t.Errorf("%q was not read as an all- form", c.spec)
			continue
		}
		if err == nil || err.Error() != c.want {
			t.Errorf("%q → %v\n want %q", c.spec, err, c.want)
		}
	}
}

func TestParseAllSpecRefusesAFilterMixedIntoAList(t *testing.T) {
	// `all-nf,plain` is someone combining two grammars. "unknown filter ',p'" would name two
	// characters of their own input back at them and explain nothing.
	_, isAll, err := parseAllSpec("all-nf,plain")
	if !isAll || err == nil {
		t.Fatalf("isAll=%v err=%v", isAll, err)
	}
	mustContain(t, err.Error(), "mixes an all-… filter with a list", "the message does not name the mistake")
}

func TestExcludeHelpIsSpelledOutOfTheTableOnce(t *testing.T) {
	// The help string appears in the usage text, the prompt hint and three error messages. If it
	// were written out by hand in any of them, a new filter would be documented in some and not
	// in others.
	var parts []string
	for _, f := range excludeFilters {
		parts = append(parts, f.code+" = "+f.label)
	}
	if got := excludeHelp(); got != strings.Join(parts, ", ") {
		t.Errorf("excludeHelp() = %q", got)
	}
	if excludeHelp() != "nf = forks, na = archived, np = private" {
		t.Errorf("the documented filter set changed: %q", excludeHelp())
	}
}

/* ---- resolveSelection ----------------------------------------------------- */

func TestResolveSelectionDropsExactlyWhatEachCodeNames(t *testing.T) {
	// Derived from each filter's own predicate rather than restating it, so a filter whose
	// matcher is rewired to the wrong listing field fails here.
	plain := testRepo("me/plain")
	for _, f := range excludeFilters {
		flagged, ok := repoMatching(f)
		if !ok {
			t.Errorf("no listing flag makes %s match anything", f.code)
			continue
		}
		outcome, err := resolveSelection([]remoteRepo{plain, flagged}, "all-"+f.code)
		if err != nil {
			t.Errorf("all-%s: %v", f.code, err)
			continue
		}
		if !equalStrings(names(outcome.repos), []string{"me/plain"}) {
			t.Errorf("all-%s kept %v", f.code, names(outcome.repos))
		}
		if outcome.total != 2 || !outcome.filtered {
			t.Errorf("all-%s: total=%d filtered=%v", f.code, outcome.total, outcome.filtered)
		}
		if len(outcome.excluded) != 1 || outcome.excluded[0].code != f.code || outcome.excluded[0].count != 1 {
			t.Errorf("all-%s: excluded = %+v", f.code, outcome.excluded)
		}
	}
}

func TestResolveSelectionCombinesCodes(t *testing.T) {
	forked := testRepo("me/forked", isFork)
	hidden := testRepo("me/hidden", isPrivate)
	old := testRepo("me/old", isArchived)
	plain := testRepo("me/plain")
	listing := []remoteRepo{forked, hidden, old, plain}

	cases := []struct {
		spec string
		want []string
	}{
		{"all-nfna", []string{"me/hidden", "me/plain"}},
		{"all-nf-na-np", []string{"me/plain"}},
		{"all", []string{"me/forked", "me/hidden", "me/old", "me/plain"}},
	}
	for _, c := range cases {
		outcome, err := resolveSelection(listing, c.spec)
		if err != nil {
			t.Errorf("%q: %v", c.spec, err)
			continue
		}
		if !equalStrings(names(outcome.repos), c.want) {
			t.Errorf("%q kept %v, want %v", c.spec, names(outcome.repos), c.want)
		}
	}
	// Plain `all` filtered nothing, so nothing may be reported as excluded — otherwise the run
	// would print an exclusion summary for a selection that excluded nothing.
	outcome, _ := resolveSelection(listing, "all")
	if outcome.filtered || len(outcome.excluded) != 0 {
		t.Errorf("plain all: filtered=%v excluded=%+v", outcome.filtered, outcome.excluded)
	}
}

func TestResolveSelectionIsANoOpWhenNothingMatches(t *testing.T) {
	// filtered stays true (an `all-…` form really did run) but there is nothing to report, so
	// the summary must be empty rather than "excluded 0 forks".
	clean := []remoteRepo{testRepo("me/plain"), testRepo("me/other")}
	outcome, err := resolveSelection(clean, "all-nf")
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(names(outcome.repos), []string{"me/plain", "me/other"}) {
		t.Errorf("kept %v", names(outcome.repos))
	}
	if !outcome.filtered || len(outcome.excluded) != 0 {
		t.Errorf("filtered=%v excluded=%+v", outcome.filtered, outcome.excluded)
	}
	if summary := selectionSummary(outcome); summary != "" {
		t.Errorf("summary = %q, want nothing said", summary)
	}
}

func TestResolveSelectionCountsARepoOnceUnderTheFirstReason(t *testing.T) {
	// A repo that is both a fork and archived must be counted once, or the reasons in
	// "excluded 2 forks, 2 archived" add up to more repositories than the listing held.
	plain := testRepo("me/plain")
	both := testRepo("me/both", isFork, isArchived)
	old := testRepo("me/old", isArchived)
	outcome, err := resolveSelection([]remoteRepo{plain, both, old}, "all-nfna")
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(names(outcome.repos), []string{"me/plain"}) {
		t.Errorf("kept %v", names(outcome.repos))
	}
	want := []exclusionCount{
		{code: "nf", label: "forks", one: "fork", count: 1},
		{code: "na", label: "archived", one: "archived", count: 1},
	}
	if fmt.Sprint(outcome.excluded) != fmt.Sprint(want) {
		t.Errorf("excluded = %+v, want %+v", outcome.excluded, want)
	}
}

func TestResolveSelectionLeavesExplicitPicksAlone(t *testing.T) {
	// `1,3` and `ezcv,sdu` name repositories outright. Being a fork or archived is irrelevant —
	// the user looked at the listing and pointed.
	named := []remoteRepo{
		testRepo("me/ezcv", isFork),
		testRepo("me/plain"),
		testRepo("me/sdu", isArchived, isPrivate),
	}
	for _, spec := range []string{"1,3", "ezcv,sdu"} {
		outcome, err := resolveSelection(named, spec)
		if err != nil {
			t.Errorf("%q: %v", spec, err)
			continue
		}
		if !equalStrings(names(outcome.repos), []string{"me/ezcv", "me/sdu"}) {
			t.Errorf("%q kept %v", spec, names(outcome.repos))
		}
		if outcome.filtered {
			t.Errorf("%q was reported as filtered", spec)
		}
	}
}

func TestResolveSelectionPassesTheParseErrorThroughWithBothHalves(t *testing.T) {
	// "unknown filter 'nx'" alone reads like a typo in a filter when it is just as often a
	// repository that is not in the listing, so the message has to say both.
	listing := []remoteRepo{testRepo("me/plain")}
	_, err := resolveSelection(listing, "all-nx")
	if err == nil {
		t.Fatal("all-nx was accepted")
	}
	want := fmt.Sprintf("unknown filter 'nx' in 'all-nx'; known: %s, and no listed repository is named 'all-nx'", excludeHelp())
	if err.Error() != want {
		t.Errorf("error = %q\n want %q", err.Error(), want)
	}
}

func TestResolveSelectionRefusesAFilterTheListingCannotAnswer(t *testing.T) {
	// A GitLab user listing reports neither fork nor archived. Treating "not reported" as false
	// would make `all-nf` look like it ran and keep every fork — the failure nobody notices,
	// because there is nothing on screen to notice.
	unknownFlags := []remoteRepo{
		{Name: "plain", FullName: "me/plain", Owner: "me", Project: "me/plain"},
		{Name: "mirror", FullName: "me/mirror", Owner: "me", Project: "me/mirror"},
	}
	_, err := resolveSelection(unknownFlags, "all-nf")
	if err == nil {
		t.Fatal("all-nf ran against a listing that cannot say what a fork is")
	}
	for _, want := range []string{"'nf' cannot be applied here", "which repositories are forks", "name,name"} {
		mustContain(t, err.Error(), want, "the refusal does not explain itself or offer a way round")
	}
	// The flag the same listing *does* answer still works: refusing everything would be as wrong
	// as refusing nothing.
	outcome, err := resolveSelection(unknownFlags, "all-np")
	if err != nil {
		t.Fatalf("all-np: %v", err)
	}
	if len(outcome.repos) != 2 {
		t.Errorf("all-np kept %v", names(outcome.repos))
	}
}

/* ---- reporting ------------------------------------------------------------ */

func TestSelectionSummarySaysHowManySurvivedAndWhyTheRestDidNot(t *testing.T) {
	outcome := selectionOutcome{
		repos: make([]remoteRepo, 12), total: 20, filtered: true,
		excluded: []exclusionCount{
			{code: "nf", label: "forks", one: "fork", count: 5},
			{code: "na", label: "archived", one: "archived", count: 3},
		},
	}
	if got := selectionSummary(outcome); got != "selected 12 of 20 (excluded 5 forks, 3 archived)" {
		t.Errorf("summary = %q", got)
	}
}

func TestSelectionSummaryUsesTheSingularForACountOfOne(t *testing.T) {
	// "excluded 1 forks" is the kind of wrongness that makes a tool feel unmaintained.
	one := selectionOutcome{
		repos: make([]remoteRepo, 2), total: 3, filtered: true,
		excluded: []exclusionCount{{code: "nf", label: "forks", one: "fork", count: 1}},
	}
	if got := selectionSummary(one); got != "selected 2 of 3 (excluded 1 fork)" {
		t.Errorf("summary = %q", got)
	}
	two := selectionOutcome{
		repos: make([]remoteRepo, 1), total: 3, filtered: true,
		excluded: []exclusionCount{
			{code: "nf", label: "forks", one: "fork", count: 1},
			{code: "np", label: "private", one: "private", count: 1},
		},
	}
	if got := selectionSummary(two); got != "selected 1 of 3 (excluded 1 fork, 1 private)" {
		t.Errorf("summary = %q", got)
	}
}

func TestExcludedEverythingMessageSpellsOutAFilterThatLeftNothing(t *testing.T) {
	// Without this line, `all-np` against an account of only private repos looks like a
	// successful run that happened to add nothing.
	four := selectionOutcome{
		total: 4, filtered: true,
		excluded: []exclusionCount{{code: "nf", label: "forks", one: "fork", count: 4}},
	}
	if got := excludedEverythingMessage("all-nf", four); got != "all-nf excluded all 4 repositories (4 forks) — nothing to add." {
		t.Errorf("message = %q", got)
	}
	single := selectionOutcome{
		total: 1, filtered: true,
		excluded: []exclusionCount{{code: "np", label: "private", one: "private", count: 1}},
	}
	// The spec is trimmed and the noun agrees with the count.
	if got := excludedEverythingMessage(" all-np ", single); got != "all-np excluded all 1 repository (1 private) — nothing to add." {
		t.Errorf("message = %q", got)
	}
}

/* ---- repositories whose name starts with all- ----------------------------- */

func TestARealRepositoryBeatsTheFilterGrammar(t *testing.T) {
	// Without the name pre-check, someone typing the name of a repository sitting right there in
	// the listing is told `unknown filter 'co'`.
	contributors := testRepo("me/all-contributors")
	filterish := testRepo("me/all-nf")
	plain := testRepo("me/plain")
	forked := testRepo("me/forked", isFork)
	listing := []remoteRepo{contributors, filterish, forked, plain}

	for _, spec := range []string{"all-contributors", "ALL-Contributors", "me/all-contributors"} {
		outcome, err := resolveSelection(listing, spec)
		if err != nil {
			t.Errorf("%q: %v", spec, err)
			continue
		}
		if !equalStrings(names(outcome.repos), []string{"me/all-contributors"}) {
			t.Errorf("%q → %v", spec, names(outcome.repos))
		}
	}

	// A repository literally named all-nf wins over the filter of the same name: it is the more
	// specific answer, and the user can still filter by index or by the other codes.
	outcome, err := resolveSelection(listing, "all-nf")
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(names(outcome.repos), []string{"me/all-nf"}) || outcome.filtered {
		t.Errorf("all-nf → %v (filtered=%v)", names(outcome.repos), outcome.filtered)
	}

	// …and with no such repository in the listing it is a filter again.
	outcome, err = resolveSelection([]remoteRepo{plain, forked}, "all-nf")
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(names(outcome.repos), []string{"me/plain"}) || !outcome.filtered {
		t.Errorf("all-nf → %v (filtered=%v)", names(outcome.repos), outcome.filtered)
	}
}

func TestARepositoryNamedPlainAllNeverShadowsTheAllKeyword(t *testing.T) {
	// The pre-check is scoped to `all-…`/`*-…` for exactly this reason: `all` has to keep meaning
	// "everything", or an account with a repo called `all` loses the only way to select it all.
	everything := testRepo("me/all")
	plain := testRepo("me/plain")
	outcome, err := resolveSelection([]remoteRepo{everything, plain}, "all")
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(names(outcome.repos), []string{"me/all", "me/plain"}) {
		t.Errorf("all → %v", names(outcome.repos))
	}
}

func TestASpacedAllPointsAtTheFilterGrammar(t *testing.T) {
	// `all -nf` reaches the name branch, where "no repository named all" points at the wrong
	// thing entirely. The hint is the difference between a fixed command line and a puzzled user.
	_, err := resolveSelection([]remoteRepo{testRepo("me/plain"), testRepo("me/forked", isFork)}, "all -nf")
	if err == nil {
		t.Fatal("all -nf was accepted")
	}
	mustContain(t, err.Error(), "did you mean all-nf", "the hint does not name the right spelling")
	mustContain(t, err.Error(), "no spaces", "the hint does not say why the spacing matters")
}

/* ---- the interactive loop ------------------------------------------------- */

// scriptedAsk answers the picker from a fixed list, recording what it printed. An answer of ""
// takes the prompt's own fallback, exactly as pressing Return does.
type scriptedAsk struct {
	answers []string
	asked   int
	lines   []string
	t       *testing.T
}

func (s *scriptedAsk) ask(_, fallback string) (string, error) {
	if s.asked >= len(s.answers) {
		// A loop that asks more times than the script answers is the hang this seam exists to
		// make impossible; failing loudly beats blocking or returning "".
		s.t.Fatalf("the picker asked %d times; the script has %d answers", s.asked+1, len(s.answers))
	}
	answer := s.answers[s.asked]
	s.asked++
	if answer == "" {
		return fallback, nil
	}
	return answer, nil
}

func (s *scriptedAsk) write(line string) { s.lines = append(s.lines, line) }

func TestAskForSelectionAcceptsNoneAndStopsAsking(t *testing.T) {
	// `none` is advertised in the prompt's own syntax line. Re-asking made it unanswerable, and
	// Ctrl-C was the only way out of the picker.
	s := &scriptedAsk{answers: []string{"none"}, t: t}
	got, err := askForSelection([]remoteRepo{testRepo("me/plain"), testRepo("me/forked", isFork)}, s.ask, s.write)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("none selected %v", names(got))
	}
	if s.asked != 1 {
		t.Errorf("asked %d times", s.asked)
	}
}

func TestAskForSelectionTakesTheShownAllDefaultForAnEmptyAnswer(t *testing.T) {
	// The prompt shows `[all]`. Pressing Return must do what the bracket says.
	s := &scriptedAsk{answers: []string{""}, t: t}
	got, err := askForSelection([]remoteRepo{testRepo("me/plain"), testRepo("me/forked", isFork)}, s.ask, s.write)
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(names(got), []string{"me/plain", "me/forked"}) {
		t.Errorf("Return selected %v", names(got))
	}
	if s.asked != 1 {
		t.Errorf("asked %d times", s.asked)
	}
}

func TestAskForSelectionReAsksAfterASpecItCouldNotHonour(t *testing.T) {
	// Two failures that must both re-ask rather than abort the run: a spec that did not parse,
	// and a filter that legitimately parsed but left nothing to add.
	unparseable := &scriptedAsk{answers: []string{"all-nx", "plain"}, t: t}
	got, err := askForSelection([]remoteRepo{testRepo("me/plain"), testRepo("me/forked", isFork)},
		unparseable.ask, unparseable.write)
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(names(got), []string{"me/plain"}) || unparseable.asked != 2 {
		t.Errorf("selected %v after %d questions", names(got), unparseable.asked)
	}
	mustContain(t, strings.Join(unparseable.lines, "\n"), "unknown filter 'nx'",
		"the picker re-asked without saying what was wrong")

	emptied := &scriptedAsk{answers: []string{"all-nf", "none"}, t: t}
	got, err = askForSelection([]remoteRepo{testRepo("me/forked", isFork)}, emptied.ask, emptied.write)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 || emptied.asked != 2 {
		t.Errorf("selected %v after %d questions", names(got), emptied.asked)
	}
	mustContain(t, strings.Join(emptied.lines, "\n"), "nothing to add",
		"the picker re-asked without saying the filter emptied the listing")
}

func TestAskForSelectionReportsWhatAFilterDropped(t *testing.T) {
	// A run that quietly loses three of five repositories is a run the user does not audit.
	s := &scriptedAsk{answers: []string{"all-nf"}, t: t}
	got, err := askForSelection([]remoteRepo{
		testRepo("me/plain"), testRepo("me/a", isFork), testRepo("me/b", isFork),
	}, s.ask, s.write)
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(names(got), []string{"me/plain"}) {
		t.Errorf("selected %v", names(got))
	}
	mustContain(t, strings.Join(s.lines, "\n"), "selected 1 of 3 (excluded 2 forks)",
		"the picker did not report the exclusions")
}

func TestAskForSelectionStopsWhenTheInputEnds(t *testing.T) {
	// A closed stdin makes every re-ask unanswerable. Without a returned error the loop would
	// spin forever on "" and the command would look like a hang.
	failing := func(string, string) (string, error) { return "", errInputClosed }
	_, err := askForSelection([]remoteRepo{testRepo("me/plain")}, failing, func(string) {})
	if !errors.Is(err, errInputClosed) {
		t.Errorf("err = %v, want errInputClosed", err)
	}
}

/* ---- small helpers -------------------------------------------------------- */

// repoMatching builds a repository that trips exactly one filter, derived from the filter's own
// predicate rather than from a hand-written table.
func repoMatching(f excludeFilter) (remoteRepo, bool) {
	for _, mutate := range []func(*remoteRepo){isFork, isArchived, isPrivate} {
		candidate := testRepo("me/"+f.code, mutate)
		if f.matches(candidate) {
			return candidate, true
		}
	}
	return remoteRepo{}, false
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
