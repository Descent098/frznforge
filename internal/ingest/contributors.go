package ingest

import (
	"sort"
	"strings"

	"frznforge/internal/config"
	"frznforge/internal/model"
)

// ContributorIndex maps a lower-cased git author email to the config entry that claims it.
type ContributorIndex map[string]config.ContributorConfig

// BuildContributorIndex indexes the site config's contributors by email.
//
// Built once per run rather than per repo. When two entries claim the same address the FIRST
// one wins, so the result never depends on map iteration order.
func BuildContributorIndex(entries []config.ContributorConfig) ContributorIndex {
	index := ContributorIndex{}
	for _, entry := range entries {
		for _, email := range entry.Emails {
			key := strings.ToLower(jsTrim(email))
			if key == "" {
				continue
			}
			if _, taken := index[key]; !taken {
				index[key] = entry
			}
		}
	}
	return index
}

// contributorGroup accumulates one person's commits while ContributorsFromCommits walks.
type contributorGroup struct {
	name     string
	nameDate string
	nameSha  string
	email    string
	commits  int64
	first    string
	last     string
	config   *config.ContributorConfig
}

// ContributorsFromCommits groups commits by author email (case-insensitive), sorted by commit
// count descending, then name, then email.
//
// With a configured index, every address one entry claims collapses into ONE contributor —
// commits summed, first/last widened — carrying the configured name, avatar, blurb and link.
// That merge happens here rather than as a post-pass because the merged count and name are
// what the sort has to order by.
func ContributorsFromCommits(commits map[string]model.Commit, configured ContributorIndex) []model.Contributor {
	groups := map[string]*contributorGroup{}
	// Ascending sha order, and that is the TypeScript's order too, not a coincidence: loadCommits
	// inserts its record's keys sorted (commits.ts:89) and scan.ts hands `Object.values(commits)`
	// straight to this function, so JS insertion order IS sha order.
	//
	// It matters for exactly one field. commits/first/last/name are order-independent by
	// construction (a sum, a min, a max, and a max keyed on date+sha). `config` is not: it is
	// fixed by whichever commit creates the group, so two config entries that resolve to the
	// same canonical address — entry A claiming ["x@h"], entry B claiming ["x@h", "y@h"], where
	// B's canonical is the x@h that A already owns — would be decided by walk order. Matching
	// the TypeScript's order is what keeps even that pathological config byte-identical.
	for _, sha := range sortedKeys(commits) {
		c := commits[sha]
		email := strings.ToLower(jsTrim(c.Author.Email))
		var cfg *config.ContributorConfig
		if entry, ok := configured[email]; ok {
			cfg = &entry
		}
		// A configured person is keyed by their FIRST listed address, so every address they
		// committed under lands in the same group.
		key := email
		canonical := email
		if cfg != nil {
			canonical = strings.ToLower(jsTrim(cfg.Emails[0]))
			key = "cfg:" + canonical
		}
		g, ok := groups[key]
		if !ok {
			groups[key] = &contributorGroup{
				name: c.Author.Name, nameDate: c.AuthorDate, nameSha: c.Sha,
				email: canonical, commits: 1, first: c.AuthorDate, last: c.AuthorDate, config: cfg,
			}
			continue
		}
		g.commits++
		if c.AuthorDate < g.first {
			g.first = c.AuthorDate
		}
		if c.AuthorDate > g.last {
			g.last = c.AuthorDate
		}
		// most recent name wins; ties broken by sha so the result is deterministic
		if c.AuthorDate > g.nameDate || (c.AuthorDate == g.nameDate && c.Sha > g.nameSha) {
			g.name, g.nameDate, g.nameSha = c.Author.Name, c.AuthorDate, c.Sha
		}
	}

	out := make([]model.Contributor, 0, len(groups))
	for _, key := range sortedKeys(groups) {
		g := groups[key]
		entry := model.Contributor{
			Name:        g.name,
			Email:       g.email,
			Commits:     g.commits,
			FirstCommit: g.first,
			LastCommit:  g.last,
		}
		if g.config != nil {
			entry.Name = g.config.Name
			// The config stores the avatar as written; the artifact stores the public/-relative
			// path the site serves, which is what the TypeScript's PublicPath transform produced
			// at parse time.
			if g.config.Avatar != "" {
				avatar := config.PublicPath(g.config.Avatar)
				entry.Avatar = &avatar
			}
			if g.config.Description != "" {
				description := g.config.Description
				entry.Description = &description
			}
			if g.config.URL != "" {
				url := g.config.URL
				entry.URL = &url
			}
		}
		out = append(out, entry)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Commits != b.Commits {
			return a.Commits > b.Commits
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Email < b.Email
	})
	return out
}

// UnmatchedContributors returns the configured contributors that decorated nobody in this
// build, in CONFIG order — the port of unmatchedContributors in contributors.ts.
//
// Not an error: the repo that person contributed to may simply not be part of this build,
// exactly like org-unknown-repo. The caller turns each one into a contributor-unknown-email
// warning, and config order is what makes the resulting warning list reproducible.
//
// seenEmails holds every author email ingest actually met, already lower-cased and trimmed —
// the same normalisation BuildContributorIndex applies, so an address that differs only in
// case or surrounding space still counts as matched.
func UnmatchedContributors(entries []config.ContributorConfig, seenEmails map[string]bool) []config.ContributorConfig {
	out := []config.ContributorConfig{}
	for _, entry := range entries {
		matched := false
		for _, email := range entry.Emails {
			if seenEmails[strings.ToLower(jsTrim(email))] {
				matched = true
				break
			}
		}
		if !matched {
			out = append(out, entry)
		}
	}
	return out
}
