package ingest

// Organizations (schema v4): turn config.organizations plus the `org` field on each repo
// source into model.Organization values — the port of src/lib/ingest/orgs.ts.
//
// Membership is the UNION of the two directions, so a repo can be claimed by the org
// (organizations[].repos) or claim the org itself (repos[].org), and doing both is a no-op
// rather than a duplicate. Dangling references on either side are warnings, never failures: a
// config edit that renames a repo must not take the site down.

import (
	"fmt"
	"sort"

	"frznforge/internal/config"
	"frznforge/internal/model"
)

// OrgRepoInput is one scanned repo as organization resolution sees it.
//
// Deliberately not a model.Repo: the artifact's Repo carries no org (it is a site-config
// concern, not a property of the repository) and the slug here must be the FINAL one, after
// the collision renaming.
type OrgRepoInput struct {
	// Slug is the final artifact slug — what lands in Organization.Repos and in URLs.
	Slug string
	// Org is `org` from this repo's source config, empty when it declared none.
	Org string
}

// ResolveOrganizationsResult is what ResolveOrganizations hands back to the assembly.
type ResolveOrganizationsResult struct {
	// Organizations are every configured org, sorted by slug, each with its member repo slugs
	// sorted and de-duplicated. An org with no members is still emitted — it has a page either
	// way.
	Organizations []model.Organization
	// Warnings are org-unknown-repo (an organizations[].repos entry matches no ingested repo,
	// Repo nil) and repo-unknown-org (a source's org matches no configured organization, Repo
	// set to that repo's slug), emitted orgs-in-config-order then repos-in-slug-order.
	Warnings []model.Warning
}

// orgDraft accumulates one organization while both membership directions are collected.
type orgDraft struct {
	slug        string
	name        string
	description *string
	// avatar is a public/-relative image path (schema v8), or nil.
	avatar *string
	// members is a set so the two directions cannot produce a duplicate.
	members map[string]bool
	// reported holds entries already raised as org-unknown-repo, so a slug listed twice warns
	// once.
	reported map[string]bool
}

// ResolveOrganizations resolves organization membership. Pure — it only reads config and slugs.
//
// Two shapes the config schema allows but does not describe:
//
//   - **The same org slug twice in `organizations`.** The definitions are merged: the first
//     one's name/description/avatar win and the repos lists are unioned. Dropping the later one
//     would silently lose members, and failing would be a build error over a duplicated key.
//   - **One repo listed by several orgs.** It joins all of them. A repo source's own `org` field
//     can only name one org, but organizations[].repos is a claim, not an exclusive one.
//
// repos must already be in final-slug order, which is what the assembly hands over.
func ResolveOrganizations(cfg *config.Resolved, repos []OrgRepoInput) ResolveOrganizationsResult {
	res := ResolveOrganizationsResult{Organizations: []model.Organization{}, Warnings: []model.Warning{}}
	known := make(map[string]bool, len(repos))
	for _, r := range repos {
		known[r.Slug] = true
	}

	// Config order, so org-unknown-repo warnings come out in the order the user wrote them.
	// The slice keeps that order; the map is only an index into it.
	var order []*orgDraft
	drafts := map[string]*orgDraft{}
	for _, org := range cfg.Organizations {
		draft, existing := drafts[org.Slug]
		if !existing {
			draft = &orgDraft{
				slug:     org.Slug,
				name:     org.Name,
				members:  map[string]bool{},
				reported: map[string]bool{},
			}
			// An absent description is null in the artifact. The Go config cannot tell an absent
			// key from `description: ""` — both decode to the empty string — so an explicitly
			// empty one also becomes null here, where the TypeScript would emit "".
			if org.Description != "" {
				d := org.Description
				draft.description = &d
			}
			if org.Avatar != "" {
				a := config.PublicPath(org.Avatar)
				draft.avatar = &a
			}
			drafts[org.Slug] = draft
			order = append(order, draft)
		}
		for _, slug := range org.Repos {
			switch {
			case known[slug]:
				draft.members[slug] = true
			case !draft.reported[slug]:
				draft.reported[slug] = true
				res.Warnings = append(res.Warnings, model.Warning{
					Code:    "org-unknown-repo",
					Repo:    nil,
					Message: fmt.Sprintf("organization '%s' lists repo '%s', which was not ingested; ignored", org.Slug, slug),
				})
			}
		}
	}

	// Then the repo → org direction, in slug order regardless of how repos was handed over, so
	// the warning list does not depend on config or scan-completion order.
	bySlug := make([]OrgRepoInput, len(repos))
	copy(bySlug, repos)
	sort.SliceStable(bySlug, func(i, j int) bool { return bySlug[i].Slug < bySlug[j].Slug })
	for _, repo := range bySlug {
		if repo.Org == "" {
			continue
		}
		if draft, ok := drafts[repo.Org]; ok {
			draft.members[repo.Slug] = true
			continue
		}
		slug := repo.Slug
		res.Warnings = append(res.Warnings, model.Warning{
			Code:    "repo-unknown-org",
			Repo:    &slug,
			Message: fmt.Sprintf("repo '%s' declares org '%s', which is not configured; ignored", repo.Slug, repo.Org),
		})
	}

	sort.SliceStable(order, func(i, j int) bool { return order[i].slug < order[j].slug })
	for _, d := range order {
		members := make([]string, 0, len(d.members))
		for slug := range d.members {
			members = append(members, slug)
		}
		sort.Strings(members)
		res.Organizations = append(res.Organizations, model.Organization{
			Slug:        d.slug,
			Name:        d.name,
			Description: d.description,
			Repos:       members,
			Avatar:      d.avatar,
		})
	}
	return res
}
