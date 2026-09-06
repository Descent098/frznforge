package ingest

// Hosted static sites (schema v7): resolve the config's `hosting.sites` entries against the
// scanned repos into the artifact's ForgeData.Hosting records — the port of
// src/lib/ingest/hosting.ts.
//
// Two halves share the branch-resolution rule. ScanRepo calls ResolveHostedBranch at scan time
// to learn which branches must get cap-exempt, big-file-capable trees; ResolveHosting calls it
// again here against the exact same inputs. A branch list is a pure function of the repo, so
// both resolve identically. Dangling references follow the organizations precedent: warnings,
// never failures.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"frznforge/internal/config"
	"frznforge/internal/model"
	"frznforge/internal/routes"
)

// HostedBranchFallbacks is the lookup order when a `hosting.sites` entry names no branch.
var HostedBranchFallbacks = []string{"gh-pages", "main", "master"}

// ResolveHostedBranch picks the branch a hosted entry serves: the configured one when it
// exists, else the first existing fallback, else "" (nothing to serve).
//
// Shared by two halves on purpose. The scanner calls it to learn which branches must get
// cap-exempt, big-file-capable trees; the assembly calls it again against the same branch list
// when it writes ForgeData.hosting. A branch list is a pure function of the repo, so both
// resolve identically — which is the point.
func ResolveHostedBranch(branchNames []string, requested *string) string {
	if requested != nil {
		if containsString(branchNames, *requested) {
			return *requested
		}
		return ""
	}
	for _, candidate := range HostedBranchFallbacks {
		if containsString(branchNames, candidate) {
			return candidate
		}
	}
	return ""
}

// ResolveHostingResult is what ResolveHosting hands back to the assembly.
type ResolveHostingResult struct {
	// Hosting is sorted by slug — artifact order.
	Hosting []model.HostedSite
	// Warnings are site-level (Repo nil). Repo-scoped problems go onto the matched repo's own
	// warning list instead; see ResolveHosting.
	Warnings []model.Warning
}

// ResolveHosting resolves every hosting entry against the FINAL (post-collision-rename) repos,
// so hosting.sites[].repo always means the slug that actually reached the artifact — the same
// rule organization membership follows.
//
// Repo-scoped problems (a missing branch, unservable paths) are APPENDED TO THE MATCHED REPO's
// warning list, which is why repos is a slice of pointers and why the assembly calls this
// before it mirrors repo warnings into the site-level list.
//
// A collision with the user's own public/ directory is a hard error rather than a warning: a
// hosted site quietly fighting public/x over who owns /x/… would be a wrong site that reports
// success. The reserved-slug set is static and already checked at config parse; public/ is
// not, so it is checked here.
func ResolveHosting(cfg *config.Resolved, repos []*model.Repo) (ResolveHostingResult, error) {
	res := ResolveHostingResult{Hosting: []model.HostedSite{}, Warnings: []model.Warning{}}
	bySlug := make(map[string]*model.Repo, len(repos))
	for _, r := range repos {
		bySlug[r.Slug] = r
	}

	publicEntries := map[string]bool{}
	if entries, err := os.ReadDir(filepath.Join(cfg.Root, "public")); err == nil {
		for _, e := range entries {
			publicEntries[e.Name()] = true
		}
	} // no public/ — nothing to collide with

	for _, site := range cfg.Hosting.Sites {
		slug := site.Slug
		if slug == "" {
			slug = site.Repo
		}
		if publicEntries[slug] {
			return ResolveHostingResult{}, fmt.Errorf(
				"hosted site '/%s/' collides with public/%s — rename one of them "+
					"(public/ files are copied to the site root verbatim)", slug, slug)
		}
		repo, ok := bySlug[site.Repo]
		if !ok {
			res.Warnings = append(res.Warnings, model.Warning{
				Code: "hosting-unknown-repo",
				Repo: nil,
				Message: fmt.Sprintf(
					"hosting entry '%s' names repo '%s', which is not in the artifact; the site is not served",
					slug, site.Repo),
			})
			continue
		}

		branchNames := make([]string, len(repo.Branches))
		for i, b := range repo.Branches {
			branchNames[i] = b.Name
		}
		// An empty Branch means the key was absent, which is what selects the fallback chain;
		// an explicitly configured branch that does not exist resolves to nothing instead.
		var requested *string
		if site.Branch != "" {
			requested = &site.Branch
		}
		ref := ResolveHostedBranch(branchNames, requested)
		if ref == "" {
			message := fmt.Sprintf("no branch to host (none of %s exist); '/%s/' is not served",
				strings.Join(HostedBranchFallbacks, ", "), slug)
			if requested != nil {
				message = fmt.Sprintf("hosted branch '%s' does not exist; '/%s/' is not served", site.Branch, slug)
			}
			repoSlug := repo.Slug
			repo.Warnings = append(repo.Warnings, model.Warning{
				Code: "hosting-branch-missing", Repo: &repoSlug, Message: message,
			})
			continue
		}

		files := repo.Files
		if repo.DefaultBranch == nil || ref != *repo.DefaultBranch {
			files = nil
			if tree, ok := repo.RefTrees.Get(ref); ok {
				files = tree.Files
			}
		}
		unservable := 0
		for path := range files {
			if !routes.IsRawServable(path) {
				unservable++
			}
		}
		if unservable > 0 {
			repoSlug := repo.Slug
			repo.Warnings = append(repo.Warnings, model.Warning{
				Code: "hosting-file-unservable",
				Repo: &repoSlug,
				Message: fmt.Sprintf(
					"%d file(s) on hosted branch '%s' contain '#' or '%%', which no static URL can round-trip; "+
						"they are missing from '/%s/'", unservable, ref, slug),
			})
		}
		res.Hosting = append(res.Hosting, model.HostedSite{Slug: slug, Repo: repo.Slug, Ref: ref})
	}

	sort.SliceStable(res.Hosting, func(i, j int) bool { return res.Hosting[i].Slug < res.Hosting[j].Slug })
	return res, nil
}
