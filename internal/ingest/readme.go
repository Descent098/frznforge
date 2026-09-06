package ingest

import (
	"sort"
	"strings"
)

// readmeRank ranks a README candidate found in the root of the default-branch tree; lower is
// better, and -1 means "not a readme at all".
//
// The port of readmeRank in src/lib/ingest/readme.ts, where the function returns `number |
// null`. -1 stands in for null because every caller only ever asks "is this a readme" and
// "which of these two is better", and a sentinel keeps both answers in one comparison.
func readmeRank(name string) int {
	l := strings.ToLower(name)
	switch {
	case l == "readme.md":
		return 0
	case l == "readme.markdown" || l == "readme.mdown":
		return 1
	case l == "readme":
		return 2
	case l == "readme.txt":
		return 3
	case l == "readme.rst" || l == "readme.adoc" || l == "readme.org":
		return 4
	case strings.HasPrefix(l, "readme."):
		return 5
	}
	return -1
}

// findReadmeEntry picks the README among root entries, preferring README.md.
//
// Ties in rank fall back to the name so the pick never depends on the order git listed the
// tree in — ls-tree is already path-sorted, but a rank tie between README.rst and README.adoc
// would otherwise be settled by whatever came first.
func findReadmeEntry(rootEntries []RawTreeEntry) *RawTreeEntry {
	candidates := []RawTreeEntry{}
	for _, e := range rootEntries {
		if e.Type == "blob" && readmeRank(e.Name) >= 0 {
			candidates = append(candidates, e)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		ri, rj := readmeRank(candidates[i].Name), readmeRank(candidates[j].Name)
		if ri != rj {
			return ri < rj
		}
		return candidates[i].Name < candidates[j].Name
	})
	best := candidates[0]
	return &best
}
