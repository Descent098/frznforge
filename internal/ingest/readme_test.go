package ingest

import "testing"

func TestReadmeRank(t *testing.T) {
	for name, want := range map[string]int{
		"README.md":       0,
		"readme.md":       0,
		"README.markdown": 1,
		"readme.mdown":    1,
		"README":          2,
		"readme":          2,
		"README.txt":      3,
		"README.rst":      4,
		"readme.adoc":     4,
		"README.org":      4,
		// Anything else that starts "readme." is still a readme, just the least preferred one.
		"README.html":   5,
		"readme.md.bak": 5,
		"readme.":       5,
		// Not readmes at all.
		"read.me":    -1,
		"readme2.md": -1,
		"readmes":    -1,
		"LICENSE":    -1,
		"":           -1,
	} {
		if got := readmeRank(name); got != want {
			t.Errorf("readmeRank(%q) = %d, want %d", name, got, want)
		}
	}
}

func TestFindReadmeEntry(t *testing.T) {
	blob := func(name string) RawTreeEntry {
		return RawTreeEntry{Type: "blob", Name: name, Path: name, Sha: "sha-" + name}
	}
	cases := []struct {
		name    string
		entries []RawTreeEntry
		want    string // "" means nil
	}{
		{"none", []RawTreeEntry{blob("LICENSE"), blob("main.go")}, ""},
		// Markdown wins over every other spelling, whatever order git listed them in.
		{"prefers md", []RawTreeEntry{blob("README.rst"), blob("README"), blob("README.md")}, "README.md"},
		{"markdown long form", []RawTreeEntry{blob("README.txt"), blob("README.markdown")}, "README.markdown"},
		{"bare README beats txt", []RawTreeEntry{blob("README.txt"), blob("README")}, "README"},
		// Same rank: the name breaks the tie, in code-point order.
		{"rank tie", []RawTreeEntry{blob("README.rst"), blob("README.adoc"), blob("README.org")}, "README.adoc"},
		{"unranked fallback", []RawTreeEntry{blob("README.html")}, "README.html"},
		// A directory called README is not the readme.
		{"blobs only", []RawTreeEntry{{Type: "tree", Name: "README.md", Path: "README.md"}}, ""},
	}
	for _, c := range cases {
		got := findReadmeEntry(c.entries)
		switch {
		case c.want == "" && got != nil:
			t.Errorf("%s: got %q, want none", c.name, got.Name)
		case c.want != "" && got == nil:
			t.Errorf("%s: got none, want %q", c.name, c.want)
		case c.want != "" && got.Name != c.want:
			t.Errorf("%s: got %q, want %q", c.name, got.Name, c.want)
		}
	}
}
