package main

// The `--select` grammar — the port of the selection half of scripts/cli.ts.
//
// Three forms, tried in this order: `all` / `all-<flags>`, then a list of 1-based indexes and
// `a-b` ranges, then a list of names. Exclusions are confined to the `all` form on purpose:
// `1,3` and `ezcv,sdu` name repositories outright and must be honoured whatever they are
// flagged as.
//
// This half has no counterpart in internal/wizard: the browser picker filters with checkboxes,
// so the grammar exists only here.

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// excludeFilter is one `all-…` exclusion. The help text, the interactive hint and the error
// messages are all generated from the table below, so adding a fourth exclusion is one row and
// nothing else.
type excludeFilter struct {
	// code is the `n<initial>` written after `all-`, e.g. `nf`.
	code string
	// label is the plural noun for messages: "excluded 5 forks".
	label string
	// one is the singular: "excluded 1 fork".
	one string
	// matches reports whether this repo should be dropped from an `all-…` selection.
	matches func(remoteRepo) bool
	// known is false when the listing did not report the flag this filter keys off. A filter
	// that cannot answer must refuse rather than under-filter — see resolveSelection.
	known func(remoteRepo) bool
	// question is how the flag is described when a listing cannot answer for it.
	question string
}

var excludeFilters = []excludeFilter{
	{
		code:     "nf",
		label:    "forks",
		one:      "fork",
		matches:  func(r remoteRepo) bool { return r.Fork != nil && *r.Fork },
		known:    func(r remoteRepo) bool { return r.Fork != nil },
		question: "which repositories are forks",
	},
	{
		code:     "na",
		label:    "archived",
		one:      "archived",
		matches:  func(r remoteRepo) bool { return r.Archived != nil && *r.Archived },
		known:    func(r remoteRepo) bool { return r.Archived != nil },
		question: "which repositories are archived",
	},
	{
		code:  "np",
		label: "private",
		one:   "private",
		// Every listing answers this one: GitLab derives it from `visibility`, the others carry
		// a `private` boolean on every item. Hence known() is unconditionally true.
		matches:  func(r remoteRepo) bool { return r.Private },
		known:    func(remoteRepo) bool { return true },
		question: "which repositories are private",
	},
}

// excludeCodes is `nf/na/np` — the codes alone, for a one-line hint.
func excludeCodes() string {
	codes := make([]string, len(excludeFilters))
	for i, f := range excludeFilters {
		codes[i] = f.code
	}
	return strings.Join(codes, "/")
}

// excludeHelp is `nf = forks, na = archived, np = private` — the canonical explanation.
func excludeHelp() string {
	parts := make([]string, len(excludeFilters))
	for i, f := range excludeFilters {
		parts[i] = f.code + " = " + f.label
	}
	return strings.Join(parts, ", ")
}

// excludeCombinedExample is a concatenation example built from the table, e.g. `all-nfna`.
func excludeCombinedExample() string {
	return "all-" + excludeFilters[0].code + excludeFilters[1].code
}

// selectSyntax is the whole grammar on one line, for the help text and the prompt.
func selectSyntax() string {
	return "all | none | 1,3,5-8 | name,name | all-" + excludeCodes()
}

// allForm matches an `all-…` / `*-…` spec, the only shape the filter grammar claims.
var allForm = regexp.MustCompile(`(?i)^(?:all|\*)-`)

// listSeparators splits a spec on commas and whitespace.
var listSeparators = regexp.MustCompile(`[,\s]+`)

// splitTokens splits a spec into non-empty tokens.
func splitTokens(text string) []string {
	var out []string
	for _, t := range listSeparators.Split(text, -1) {
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// parseAllSpec reads an `all` selection and its exclusion flags.
//
// `all`, `all-nf`, `all-nfna`, `all-nf-na` (order irrelevant, case-insensitive) → the codes to
// exclude. isAll is false — with no error — when the spec is not an `all` form at all, which is
// the caller's cue to treat it as indexes or names.
func parseAllSpec(spec string) (codes map[string]bool, isAll bool, err error) {
	text := strings.TrimSpace(spec)
	lower := strings.ToLower(text)
	if lower == "all" || lower == "*" {
		return map[string]bool{}, true, nil
	}
	if !allForm.MatchString(lower) {
		return nil, false, nil
	}
	rest := lower[strings.IndexByte(lower, '-')+1:]

	// A comma or a space after `all-` is someone trying to mix a filter with a list. Saying
	// "unknown filter ',a'" for `all-nf,alpha` names two characters of their own input back at
	// them and explains nothing.
	if strings.ContainsAny(rest, ", \t\r\n") {
		return nil, true, fmt.Errorf(
			"'%s' mixes an all-… filter with a list; select either all-<flags> (%s) or names/indexes, not both",
			text, excludeCombinedExample())
	}

	codes = map[string]bool{}
	for _, group := range strings.Split(rest, "-") {
		for len(group) > 0 {
			hit := longestFilterPrefix(group)
			if hit == "" {
				offender := group
				if len(offender) > maxCodeLength() {
					offender = offender[:maxCodeLength()]
				}
				return nil, true, fmt.Errorf("unknown filter '%s' in '%s'; known: %s", offender, text, excludeHelp())
			}
			codes[hit] = true
			group = group[len(hit):]
		}
	}
	if len(codes) == 0 {
		return nil, true, fmt.Errorf("'%s' names no filter; known: %s", text, excludeHelp())
	}
	return codes, true, nil
}

// longestFilterPrefix returns the longest filter code that starts group, or "".
//
// Longest first so a future three-letter code that begins with an existing two-letter one still
// parses; with today's table the order is irrelevant.
func longestFilterPrefix(group string) string {
	best := ""
	for _, f := range excludeFilters {
		if strings.HasPrefix(group, f.code) && len(f.code) > len(best) {
			best = f.code
		}
	}
	return best
}

func maxCodeLength() int {
	n := 0
	for _, f := range excludeFilters {
		if len(f.code) > n {
			n = len(f.code)
		}
	}
	return n
}

// exclusionCount is one reason repos were dropped, and how many it accounted for.
type exclusionCount struct {
	code  string
	label string
	one   string
	count int
}

// selectionOutcome is what a spec resolved to, plus enough to explain it.
type selectionOutcome struct {
	// repos are the ones to add, in the order the spec asked for them.
	repos []remoteRepo
	// total is how many repos the listing offered.
	total int
	// filtered is true when an `all-…` form ran, even if it happened to drop nothing.
	filtered bool
	// excluded lists the reasons that actually dropped something, in table order.
	excluded []exclusionCount
}

// resolveSelection resolves a selection against a listing, reporting what a filter removed.
func resolveSelection(repos []remoteRepo, spec string) (selectionOutcome, error) {
	// A repository really named `all-contributors` beats the filter grammar. Without this the
	// `all-` prefix claims the string first and the user is told `unknown filter 'co'` about a
	// repository sitting right there in the listing. Scoped to `all-…`/`*-…` so a repo called
	// plain `all` cannot shadow the `all` keyword.
	trimmed := strings.TrimSpace(spec)
	if allForm.MatchString(trimmed) {
		if named, ok := findByName(repos, trimmed); ok {
			return selectionOutcome{repos: []remoteRepo{named}, total: len(repos)}, nil
		}
	}

	codes, isAll, err := parseAllSpec(spec)
	if isAll {
		if err != nil {
			// Only an `all-…` form can fail to parse, and the name pre-check above has already
			// run, so this spec is neither a filter nor a repository. Say both halves —
			// "unknown filter 'co'" alone reads like a typo in a filter when it is usually a
			// repository that is not in the listing.
			return selectionOutcome{}, fmt.Errorf("%w, and no listed repository is named '%s'", err, trimmed)
		}
		var active []excludeFilter
		for _, f := range excludeFilters {
			if codes[f.code] {
				active = append(active, f)
			}
		}
		// Refuse rather than under-filter. A listing that does not report a flag would otherwise
		// make `all-nf` look like it ran and keep every fork — the failure nobody notices.
		for _, f := range active {
			for _, r := range repos {
				if f.known(r) {
					continue
				}
				return selectionOutcome{}, fmt.Errorf(
					"'%s' cannot be applied here: this provider's repository listing does not say %s. "+
						"Select by name or index instead (e.g. name,name or 1,3,5-8)", f.code, f.question)
			}
		}
		counts := map[string]int{}
		var kept []remoteRepo
		for _, repo := range repos {
			// First matching reason only: a repo that is both a fork and archived is counted
			// once, so the reasons in the summary add up to the number actually dropped.
			dropped := false
			for _, f := range active {
				if f.matches(repo) {
					counts[f.code]++
					dropped = true
					break
				}
			}
			if !dropped {
				kept = append(kept, repo)
			}
		}
		outcome := selectionOutcome{repos: kept, total: len(repos), filtered: len(active) > 0}
		for _, f := range active {
			if counts[f.code] > 0 {
				outcome.excluded = append(outcome.excluded,
					exclusionCount{code: f.code, label: f.label, one: f.one, count: counts[f.code]})
			}
		}
		return outcome, nil
	}

	picked, err := selectExplicit(repos, spec)
	if err != nil {
		return selectionOutcome{}, err
	}
	return selectionOutcome{repos: picked, total: len(repos)}, nil
}

// findByName matches a token against a repo's name or owner/name, case-insensitively.
func findByName(repos []remoteRepo, token string) (remoteRepo, bool) {
	needle := strings.ToLower(token)
	for _, r := range repos {
		if strings.ToLower(r.Name) == needle || strings.ToLower(r.FullName) == needle {
			return r, true
		}
	}
	return remoteRepo{}, false
}

// selectionSummary is `selected 12 of 20 (excluded 5 forks, 3 archived)`, or "" when nothing
// was dropped.
func selectionSummary(outcome selectionOutcome) string {
	if len(outcome.excluded) == 0 {
		return ""
	}
	return fmt.Sprintf("selected %d of %d (excluded %s)",
		len(outcome.repos), outcome.total, reasonList(outcome.excluded))
}

// excludedEverythingMessage is the line printed when a filter leaves nothing behind. Without it
// an `all-np` against an account of only private repos would look like a successful run that
// happened to add no entries.
func excludedEverythingMessage(spec string, outcome selectionOutcome) string {
	noun := "repositories"
	if outcome.total == 1 {
		noun = "repository"
	}
	return fmt.Sprintf("%s excluded all %d %s (%s) — nothing to add.",
		strings.TrimSpace(spec), outcome.total, noun, reasonList(outcome.excluded))
}

// reasonList is `5 forks, 1 archived`, singular where the count is one.
func reasonList(excluded []exclusionCount) string {
	parts := make([]string, len(excluded))
	for i, e := range excluded {
		noun := e.label
		if e.count == 1 {
			noun = e.one
		}
		parts[i] = fmt.Sprintf("%d %s", e.count, noun)
	}
	return strings.Join(parts, ", ")
}

// numericList matches a spec made only of digits, commas, dashes and spaces.
var numericList = regexp.MustCompile(`^[\d,\s-]+$`)

// rangeToken matches `3-7`.
var rangeToken = regexp.MustCompile(`^(\d+)\s*-\s*(\d+)$`)

// parseSelection parses a numbered multi-select: `all`, `none`/empty, or a comma/space
// separated list of 1-based indexes and `a-b` ranges. Returns sorted, de-duplicated 0-based
// indexes.
//
// Index-based and exclusion-free on purpose: `all-nf` and friends are resolved against the
// listing itself by resolveSelection, which knows what each repo is.
func parseSelection(spec string, count int) ([]int, error) {
	text := strings.ToLower(strings.TrimSpace(spec))
	if text == "all" || text == "*" {
		out := make([]int, count)
		for i := range out {
			out[i] = i
		}
		return out, nil
	}
	if text == "" || text == "none" {
		return nil, nil
	}

	picked := map[int]bool{}
	for _, token := range splitTokens(text) {
		if m := rangeToken.FindStringSubmatch(token); m != nil {
			from, _ := strconv.Atoi(m[1])
			to, _ := strconv.Atoi(m[2])
			if from > to {
				return nil, fmt.Errorf("range %s runs backwards", token)
			}
			for n := from; n <= to; n++ {
				if n < 1 || n > count {
					return nil, fmt.Errorf("%d is out of range (1-%d)", n, count)
				}
				picked[n-1] = true
			}
			continue
		}
		n, err := strconv.Atoi(token)
		if err != nil {
			return nil, fmt.Errorf("not a number: %s", token)
		}
		if n < 1 || n > count {
			return nil, fmt.Errorf("%d is out of range (1-%d)", n, count)
		}
		picked[n-1] = true
	}
	out := make([]int, 0, len(picked))
	for i := range picked {
		out = append(out, i)
	}
	// picked is a map, so this sort is what makes the result deterministic. Ranging a map and
	// printing the entries would give a different config on every run.
	sort.Ints(out)
	return out, nil
}

// selectExplicit handles indexes and names — everything that is not an `all` form.
func selectExplicit(repos []remoteRepo, spec string) ([]remoteRepo, error) {
	text := strings.TrimSpace(spec)
	if text == "" || strings.EqualFold(text, "none") || numericList.MatchString(text) {
		indexes, err := parseSelection(text, len(repos))
		if err != nil {
			return nil, err
		}
		out := make([]remoteRepo, 0, len(indexes))
		for _, i := range indexes {
			out = append(out, repos[i])
		}
		return out, nil
	}

	var picked []remoteRepo
	seen := map[string]bool{}
	for _, token := range splitTokens(text) {
		if n, err := strconv.Atoi(token); err == nil {
			if n < 1 || n > len(repos) {
				return nil, fmt.Errorf("%d is out of range (1-%d)", n, len(repos))
			}
			repo := repos[n-1]
			if !seen[repo.FullName] {
				seen[repo.FullName] = true
				picked = append(picked, repo)
			}
			continue
		}
		match, ok := findByName(repos, token)
		if !ok {
			// `all -nf` / `allnf` fall through to here, and "no repository named all" points at
			// the wrong thing entirely: the filters attach to `all` with a dash and no spaces.
			hint := ""
			if strings.HasPrefix(strings.ToLower(text), "all") {
				hint = fmt.Sprintf(" — did you mean all-%s? Filters attach with a dash and no spaces: %s",
					excludeFilters[0].code, excludeCombinedExample())
			}
			return nil, fmt.Errorf("no repository named %s%s", token, hint)
		}
		if !seen[match.FullName] {
			seen[match.FullName] = true
			picked = append(picked, match)
		}
	}
	return picked, nil
}
