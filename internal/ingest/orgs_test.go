package ingest

import (
	"reflect"
	"testing"

	"frznforge/internal/model"
)

func orgSlugs(orgs []model.Organization) []string {
	out := make([]string, len(orgs))
	for i, o := range orgs {
		out[i] = o.Slug
	}
	return out
}

func warningMessages(warnings []model.Warning) []string {
	out := make([]string, len(warnings))
	for i, w := range warnings {
		out[i] = w.Message
	}
	return out
}

// Membership is the union of both directions, and doing both is a no-op rather than a
// duplicate.
func TestResolveOrganizationsUnionsBothDirections(t *testing.T) {
	cfg := testResolvedConfig(t, t.TempDir(), `{
      "owner": {"name": "Tester", "handle": "tester"},
      "organizations": [
        {"slug": "tools", "name": "Tools", "description": "Small things", "repos": ["alpha", "beta"], "avatar": "logos/tools.png"},
        {"slug": "apps", "name": "Apps"}
      ]
    }`)
	res := ResolveOrganizations(cfg, []OrgRepoInput{
		{Slug: "alpha", Org: "tools"}, // claimed from both sides
		{Slug: "beta", Org: ""},
		{Slug: "gamma", Org: "apps"},
	})
	if got := orgSlugs(res.Organizations); !reflect.DeepEqual(got, []string{"apps", "tools"}) {
		t.Fatalf("orgs = %v, want them sorted by slug", got)
	}
	apps, tools := res.Organizations[0], res.Organizations[1]
	if !reflect.DeepEqual(apps.Repos, []string{"gamma"}) {
		t.Errorf("apps.repos = %v", apps.Repos)
	}
	if !reflect.DeepEqual(tools.Repos, []string{"alpha", "beta"}) {
		t.Errorf("tools.repos = %v, want sorted and de-duplicated", tools.Repos)
	}
	if tools.Description == nil || *tools.Description != "Small things" {
		t.Errorf("description = %v", tools.Description)
	}
	// The avatar is a public/-relative path, normalised to a leading slash exactly as the
	// TypeScript's PublicPath transform does.
	if tools.Avatar == nil || *tools.Avatar != "/logos/tools.png" {
		t.Errorf("avatar = %v", tools.Avatar)
	}
	if apps.Description != nil || apps.Avatar != nil {
		t.Errorf("an org that declared neither must carry nulls: %+v", apps)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("warnings = %v", warningCodes(res.Warnings))
	}
}

// An org with no members still gets emitted: it has a page either way.
func TestResolveOrganizationsKeepsEmptyOrgs(t *testing.T) {
	cfg := testResolvedConfig(t, t.TempDir(), `{
      "owner": {"name": "Tester", "handle": "tester"},
      "organizations": [{"slug": "lonely", "name": "Lonely"}]
    }`)
	res := ResolveOrganizations(cfg, nil)
	if len(res.Organizations) != 1 || len(res.Organizations[0].Repos) != 0 {
		t.Fatalf("orgs = %+v", res.Organizations)
	}
	if res.Organizations[0].Repos == nil {
		t.Error("Repos must be an empty slice, not nil — nil marshals as null")
	}
}

// Dangling references on either side are warnings, never failures: a config edit that renames
// a repo must not take the site down.
func TestResolveOrganizationsWarnsAboutDanglingReferences(t *testing.T) {
	cfg := testResolvedConfig(t, t.TempDir(), `{
      "owner": {"name": "Tester", "handle": "tester"},
      "organizations": [
        {"slug": "zed", "name": "Zed", "repos": ["ghost", "ghost", "alpha"]},
        {"slug": "acme", "name": "Acme", "repos": ["phantom"]}
      ]
    }`)
	res := ResolveOrganizations(cfg, []OrgRepoInput{
		{Slug: "zulu", Org: "nowhere"},
		{Slug: "alpha", Org: "nowhere"},
	})
	// Org-side warnings come in CONFIG order (zed before acme, as written), repo-side ones in
	// SLUG order — so neither depends on how the scans finished.
	if got := warningCodes(res.Warnings); !reflect.DeepEqual(got,
		[]string{"org-unknown-repo", "org-unknown-repo", "repo-unknown-org", "repo-unknown-org"}) {
		t.Fatalf("warnings = %v", got)
	}
	messages := warningMessages(res.Warnings)
	if messages[0] != "organization 'zed' lists repo 'ghost', which was not ingested; ignored" {
		t.Errorf("first warning = %q", messages[0])
	}
	if messages[1] != "organization 'acme' lists repo 'phantom', which was not ingested; ignored" {
		t.Errorf("a slug listed twice must warn once; warnings = %v", messages)
	}
	if messages[2] != "repo 'alpha' declares org 'nowhere', which is not configured; ignored" {
		t.Errorf("repo-side warnings are not in slug order: %v", messages[2:])
	}
	// org-unknown-repo is site-level; repo-unknown-org names the repo that declared it.
	if res.Warnings[0].Repo != nil {
		t.Error("org-unknown-repo must be site-level")
	}
	if res.Warnings[2].Repo == nil || *res.Warnings[2].Repo != "alpha" {
		t.Errorf("repo-unknown-org repo = %v", res.Warnings[2].Repo)
	}
}

// The same slug twice is merged rather than dropped or fatal: the first definition wins and
// the repos lists are unioned.
func TestResolveOrganizationsMergesDuplicateSlugs(t *testing.T) {
	cfg := testResolvedConfig(t, t.TempDir(), `{
      "owner": {"name": "Tester", "handle": "tester"},
      "organizations": [
        {"slug": "dup", "name": "First", "description": "kept", "repos": ["alpha"]},
        {"slug": "dup", "name": "Second", "description": "dropped", "repos": ["beta"]}
      ]
    }`)
	res := ResolveOrganizations(cfg, []OrgRepoInput{{Slug: "alpha"}, {Slug: "beta"}})
	if len(res.Organizations) != 1 {
		t.Fatalf("orgs = %+v, want one merged entry", res.Organizations)
	}
	org := res.Organizations[0]
	if org.Name != "First" || org.Description == nil || *org.Description != "kept" {
		t.Errorf("the later definition won: %+v", org)
	}
	if !reflect.DeepEqual(org.Repos, []string{"alpha", "beta"}) {
		t.Errorf("repos = %v, want both lists unioned", org.Repos)
	}
}

// A repo's own `org` names one org; `organizations[].repos` is a claim, not an exclusive one.
func TestResolveOrganizationsLetsSeveralOrgsClaimOneRepo(t *testing.T) {
	cfg := testResolvedConfig(t, t.TempDir(), `{
      "owner": {"name": "Tester", "handle": "tester"},
      "organizations": [
        {"slug": "a", "name": "A", "repos": ["shared"]},
        {"slug": "b", "name": "B", "repos": ["shared"]}
      ]
    }`)
	res := ResolveOrganizations(cfg, []OrgRepoInput{{Slug: "shared"}})
	for _, org := range res.Organizations {
		if !reflect.DeepEqual(org.Repos, []string{"shared"}) {
			t.Errorf("%s.repos = %v", org.Slug, org.Repos)
		}
	}
}

// The result must not depend on the order the caller happened to hand repos over.
func TestResolveOrganizationsIgnoresRepoInputOrder(t *testing.T) {
	cfg := testResolvedConfig(t, t.TempDir(), `{
      "owner": {"name": "Tester", "handle": "tester"},
      "organizations": [{"slug": "tools", "name": "Tools"}]
    }`)
	forwards := ResolveOrganizations(cfg, []OrgRepoInput{
		{Slug: "alpha", Org: "tools"}, {Slug: "beta", Org: "gone"}, {Slug: "aardvark", Org: "gone"},
	})
	backwards := ResolveOrganizations(cfg, []OrgRepoInput{
		{Slug: "aardvark", Org: "gone"}, {Slug: "beta", Org: "gone"}, {Slug: "alpha", Org: "tools"},
	})
	if !reflect.DeepEqual(forwards, backwards) {
		t.Fatalf("input order changed the result:\n%+v\n%+v", forwards, backwards)
	}
	if got := warningMessages(forwards.Warnings); got[0] != "repo 'aardvark' declares org 'gone', which is not configured; ignored" {
		t.Errorf("warnings = %v, want slug order", got)
	}
}
