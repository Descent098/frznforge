package render

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestListingMatchesBrowser pins the second of the version's two-language pairs:
// internal/render/listing.go (build time, Go) against web/js/listing.js (view time, browser).
//
// The build renders page one; the browser re-renders the grid the moment a filter is touched.
// If the two disagree the listing visibly changes under the visitor's hands, which reads as a
// glitch rather than a bug. tests/fixtures/listing-cases.json is generated FROM the JavaScript
// (`node tests/fixtures/gen-listing-cases.mjs`), so the JavaScript is the reference.
func TestListingMatchesBrowser(t *testing.T) {
	var golden struct {
		Repos  []RepoSummary `json:"repos"`
		Facets struct {
			Languages []struct {
				Name  string `json:"name"`
				Count int    `json:"count"`
			} `json:"languages"`
			Tags []struct {
				Name  string `json:"name"`
				Count int    `json:"count"`
			} `json:"tags"`
			Templates int `json:"templates"`
		} `json:"facets"`
		Sorts    map[string][]string `json:"sorts"`
		Listings []struct {
			Name  string `json:"name"`
			Query struct {
				Q         string   `json:"q"`
				Sort      string   `json:"sort"`
				Languages []string `json:"languages"`
				Tags      []string `json:"tags"`
				Kind      string   `json:"kind"`
				Page      int      `json:"page"`
				PageSize  int      `json:"pageSize"`
			} `json:"query"`
			Items     []string `json:"items"`
			Total     int      `json:"total"`
			Page      int      `json:"page"`
			PageCount int      `json:"pageCount"`
		} `json:"listings"`
	}

	raw, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "listing-cases.json"))
	if err != nil {
		t.Fatalf("read shared golden: %v (regenerate with `node tests/fixtures/gen-listing-cases.mjs`)", err)
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("parse golden: %v", err)
	}
	if len(golden.Listings) == 0 {
		t.Fatal("golden is empty — a vacuous pass is worse than no test")
	}

	t.Run("facets", func(t *testing.T) {
		langs, tags, templates := Facets(golden.Repos)
		if templates != golden.Facets.Templates {
			t.Errorf("templates = %d, browser says %d", templates, golden.Facets.Templates)
		}
		for i, want := range golden.Facets.Languages {
			if i >= len(langs) || langs[i].Name != want.Name || langs[i].Count != want.Count {
				t.Errorf("language facet %d: got %+v, browser says %+v", i, langs, golden.Facets.Languages)
				break
			}
		}
		for i, want := range golden.Facets.Tags {
			if i >= len(tags) || tags[i].Name != want.Name || tags[i].Count != want.Count {
				t.Errorf("tag facet %d: got %+v, browser says %+v", i, tags, golden.Facets.Tags)
				break
			}
		}
	})

	for key, want := range golden.Sorts {
		t.Run("sort/"+key, func(t *testing.T) {
			got := slugsOf(SortRepos(golden.Repos, key))
			if !reflect.DeepEqual(got, want) {
				t.Errorf("got  %v\nwant %v", got, want)
			}
		})
	}

	for _, c := range golden.Listings {
		t.Run("listing/"+c.Name, func(t *testing.T) {
			q := ListingQuery{
				Q: c.Query.Q, Sort: c.Query.Sort, Languages: c.Query.Languages,
				Tags: c.Query.Tags, Kind: c.Query.Kind, Page: c.Query.Page, PageSize: c.Query.PageSize,
			}
			got := ApplyListing(golden.Repos, q)
			if items := slugsOf(got.Items); !reflect.DeepEqual(items, c.Items) {
				t.Errorf("items:\n got  %v\n want %v", items, c.Items)
			}
			if got.Total != c.Total || got.Page != c.Page || got.PageCount != c.PageCount {
				t.Errorf("total/page/pageCount = %d/%d/%d, browser says %d/%d/%d",
					got.Total, got.Page, got.PageCount, c.Total, c.Page, c.PageCount)
			}
		})
	}
}

func slugsOf(repos []RepoSummary) []string {
	out := []string{}
	for _, r := range repos {
		out = append(out, r.Slug)
	}
	return out
}
