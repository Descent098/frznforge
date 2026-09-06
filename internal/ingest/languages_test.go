package ingest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"frznforge/internal/model"
)

func str(s string) *string { return &s }

func lang(path string, size int64, binary bool) model.FileInfo {
	return model.FileInfo{Path: path, Sha: "x", Size: size, Binary: binary, Language: DetectLanguage(path)}
}

// languageColor indexes langs by display NAME, not by key, and a Go map has no declaration
// order to break a tie with. Unique names are what makes that lookup deterministic, so the
// invariant is asserted rather than assumed.
func TestLanguageNamesAreUnique(t *testing.T) {
	seen := map[string]string{}
	for key, def := range langs {
		if other, dup := seen[def.Name]; dup {
			t.Errorf("languages %q and %q both display as %q; languageColor would pick one at random",
				other, key, def.Name)
		}
		seen[def.Name] = key
	}
}

// Every extension and filename maps to a KEY of langs. A typo would silently resolve to the
// zero languageDef — an empty name and no colour — which reaches the artifact as `"language":
// ""` rather than an honest null.
func TestLanguageMapsResolve(t *testing.T) {
	for ext, key := range langByExt {
		if _, ok := langs[key]; !ok {
			t.Errorf("langByExt[%q] = %q, which is not a language", ext, key)
		}
	}
	for name, key := range langByFilename {
		if _, ok := langs[key]; !ok {
			t.Errorf("langByFilename[%q] = %q, which is not a language", name, key)
		}
	}
	for key, def := range langs {
		if def.Name == "" {
			t.Errorf("language %q has no display name", key)
		}
	}
}

func TestDetectLanguage(t *testing.T) {
	cases := []struct {
		path string
		want string // "" means nil
	}{
		{"main.go", "Go"},
		{"src/lib/routes.ts", "TypeScript"},
		{"Component.svelte", "Svelte"},
		// The display name is not always the map key.
		{"infra/main.tf", "HCL"},
		{"api.proto", "Protocol Buffer"},
		{"plugin.vim", "Vim Script"},
		{".gitignore", "Ignore List"},
		// Filename wins over extension: cmakelists.txt is CMake, not Text.
		{"CMakeLists.txt", "CMake"},
		{"go.mod", "Text"},
		{"go.sum", "Lockfile"},
		// Case-insensitive on the basename only.
		{"README.MD", "Markdown"},
		{"SRC/Main.PY", "Python"},
		// Prefix rules for the two "Foo.variant" families.
		{"Dockerfile.dev", "Dockerfile"},
		{"Makefile.am", "Makefile"},
		// A bare dotfile has its dot at index 0, so it is not an extension.
		{".hidden", ""},
		{"nested/.hidden", ""},
		// …but a known dotfile still resolves by name.
		{"deep/.editorconfig", "EditorConfig"},
		{"LICENSE", "Text"},
		{"noextension", ""},
		{"unknown.qqq", ""},
		{"", ""},
		{"trailing.", ""},
	}
	for _, c := range cases {
		got := DetectLanguage(c.path)
		switch {
		case c.want == "" && got != nil:
			t.Errorf("DetectLanguage(%q) = %q, want nil", c.path, *got)
		case c.want != "" && got == nil:
			t.Errorf("DetectLanguage(%q) = nil, want %q", c.path, c.want)
		case c.want != "" && *got != c.want:
			t.Errorf("DetectLanguage(%q) = %q, want %q", c.path, *got, c.want)
		}
	}
}

func TestLanguageColor(t *testing.T) {
	if got := languageColor("Go"); got == nil || *got != "#00add8" {
		t.Errorf("languageColor(Go) = %v", got)
	}
	// Colourless by design; the UI substitutes a neutral.
	if got := languageColor("Protocol Buffer"); got != nil {
		t.Errorf("languageColor(Protocol Buffer) = %q, want nil", *got)
	}
	// "Terraform" is the KEY, not the name — looking a key up must miss.
	if got := languageColor("Terraform"); got != nil {
		t.Errorf("languageColor(Terraform) = %q, want nil", *got)
	}
	if got := languageColor("Nonesuch"); got != nil {
		t.Errorf("languageColor(Nonesuch) = %q, want nil", *got)
	}
}

func TestIsVendoredPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"node_modules/left-pad/index.js", true},
		{"web/node_modules/x/y.js", true},
		{"vendor/github.com/x/y.go", true},
		{"dist/bundle.js", true},
		{"build/out.o", true},
		{"NODE_MODULES/x.js", true}, // segments are compared case-insensitively
		// Only DIRECTORY segments count: a file that happens to be named "vendor" is real code.
		{"src/vendor", false},
		{"node_modules", false},
		{"src/main.go", false},
		{"assets/app.min.js", true},
		{"assets/app.MIN.JS", true},
		{"assets/app.min.css", true},
		// ".min." has to be followed by an extension.
		{"assets/app.min.", false},
		{"assets/minified.js", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsVendoredPath(c.path); got != c.want {
			t.Errorf("IsVendoredPath(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

// Go's (?i) folds Unicode onto ASCII and JavaScript's non-unicode /i deliberately does not, so
// `(?i)[a-z0-9]` would match U+017F and U+212A where the TypeScript matches neither. The
// pattern spells its classes out for exactly this reason.
func TestMinifiedPatternDoesNotFoldUnicode(t *testing.T) {
	for _, path := range []string{"app.min.ſ", "app.min.K", "app.min.é"} {
		if IsVendoredPath(path) {
			t.Errorf("IsVendoredPath(%q) = true; JavaScript's /i does not fold non-ASCII onto [a-z]", path)
		}
	}
}

func TestLanguageStatsBasics(t *testing.T) {
	files := map[string]model.FileInfo{
		"a.go":                lang("a.go", 700, false),
		"b.ts":                lang("b.ts", 300, false),
		"README.md":           lang("README.md", 5000, false),   // prose: excluded
		"config.json":         lang("config.json", 5000, false), // prose: excluded
		"logo.png":            {Path: "logo.png", Size: 9000, Binary: true, Language: nil},
		"vendor/dep.go":       lang("vendor/dep.go", 9000, false), // vendored: excluded
		"assets/app.min.js":   lang("assets/app.min.js", 9000, false),
		"unknown.qqq":         lang("unknown.qqq", 9000, false), // no language: excluded
		"bin/blob.go":         {Path: "bin/blob.go", Size: 9000, Binary: true, Language: str("Go")},
		"nested/deep/main.go": lang("nested/deep/main.go", 0, false),
	}
	got := LanguageStats(files)
	want := []model.LanguageStat{
		{Name: "Go", Bytes: 700, Percent: 70, Color: str("#00add8")},
		{Name: "TypeScript", Bytes: 300, Percent: 30, Color: str("#3178c6")},
	}
	assertStats(t, got, want)
}

// Percents are rounded to one decimal and then made to sum to exactly 100 by folding the drift
// into the largest entry, so a stacked bar always fills its row.
func TestLanguageStatsFoldsRoundingDrift(t *testing.T) {
	files := map[string]model.FileInfo{
		"a.go":  lang("a.go", 1, false),
		"b.rs":  lang("b.rs", 1, false),
		"c.zig": lang("c.zig", 1, false),
	}
	got := LanguageStats(files)
	if len(got) != 3 {
		t.Fatalf("got %d stats, want 3", len(got))
	}
	// 1/3 rounds to 33.3 three times over, leaving 0.1 unaccounted for.
	if got[0].Percent != 33.4 || got[1].Percent != 33.3 || got[2].Percent != 33.3 {
		t.Errorf("percents = %v/%v/%v, want 33.4/33.3/33.3", got[0].Percent, got[1].Percent, got[2].Percent)
	}
	var sum float64
	for _, s := range got {
		sum += s.Percent
	}
	if sum < 99.999 || sum > 100.001 {
		t.Errorf("percents sum to %v, want 100", sum)
	}
	// Equal byte counts: the tie is broken by name, in code-point order.
	if got[0].Name != "Go" || got[1].Name != "Rust" || got[2].Name != "Zig" {
		t.Errorf("tie order = %q/%q/%q, want Go/Rust/Zig", got[0].Name, got[1].Name, got[2].Name)
	}
}

// A nil slice marshals as `null`; the artifact says `[]`.
func TestLanguageStatsEmptyIsNotNil(t *testing.T) {
	for name, files := range map[string]map[string]model.FileInfo{
		"no files":     {},
		"prose only":   {"README.md": lang("README.md", 400, false)},
		"zero-byte Go": {"a.go": lang("a.go", 0, false)},
	} {
		got := LanguageStats(files)
		if got == nil {
			t.Errorf("%s: LanguageStats returned nil, want an empty slice", name)
		}
		if len(got) != 0 {
			t.Errorf("%s: LanguageStats = %+v, want empty", name, got)
		}
	}
}

// Map iteration is randomised; the output must not be.
func TestLanguageStatsIsDeterministic(t *testing.T) {
	files := map[string]model.FileInfo{}
	for _, p := range []string{"a.go", "b.rs", "c.ts", "d.py", "e.rb", "f.zig", "g.ex", "h.hs"} {
		files[p] = lang(p, 100, false)
	}
	first := LanguageStats(files)
	for i := 0; i < 20; i++ {
		assertStats(t, LanguageStats(files), first)
	}
}

/* ---- the tables themselves, against languages.ts -------------------------- */

// The language table is data, not logic, and a transcription slip in it is invisible: one
// wrong colour or one missing extension changes artifact bytes for exactly the repositories
// that happen to contain that file type, and every test written by hand would still pass. So
// the source of truth is read back out of the TypeScript and compared entry by entry.
//
// It also guards a second consumer: internal/highlight maps these display NAMES, so a rename
// here silently stops highlighting a language.
func TestLanguageTablesMatchTypeScript(t *testing.T) {
	src := filepath.Join(projectRoot(t), "src", "lib", "ingest", "languages.ts")
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Skipf("the TypeScript original is not present (%v); nothing to compare against", err)
	}
	text := string(raw)

	t.Run("definitions", func(t *testing.T) {
		// key: L('Name', '#colour' | null[, prose])
		re := regexp.MustCompile(`(?m)^\s*(?:'([^']+)'|([A-Za-z][\w ]*)):\s*L\('([^']+)',\s*(?:'([^']+)'|null)(?:,\s*(true|false))?\)`)
		matches := re.FindAllStringSubmatch(sliceBetween(t, text, "const LANGS = {", "} satisfies"), -1)
		if len(matches) == 0 {
			t.Fatal("parsed no language definitions; the regex and languages.ts have drifted apart")
		}
		for _, m := range matches {
			key := m[1] + m[2] // exactly one of the two alternatives matched
			def, ok := langs[key]
			if !ok {
				t.Errorf("language %q is in languages.ts and not in langs", key)
				continue
			}
			if def.Name != m[3] {
				t.Errorf("%s: name = %q, TypeScript says %q", key, def.Name, m[3])
			}
			switch {
			case m[4] == "" && def.Color != nil:
				t.Errorf("%s: colour = %q, TypeScript says null", key, *def.Color)
			case m[4] != "" && def.Color == nil:
				t.Errorf("%s: colour = nil, TypeScript says %q", key, m[4])
			case m[4] != "" && def.Color != nil && *def.Color != m[4]:
				t.Errorf("%s: colour = %q, TypeScript says %q", key, *def.Color, m[4])
			}
			if want := m[5] == "true"; def.Prose != want {
				t.Errorf("%s: prose = %v, TypeScript says %v", key, def.Prose, want)
			}
		}
		if len(matches) != len(langs) {
			t.Errorf("langs has %d entries, languages.ts has %d", len(langs), len(matches))
		}
	})

	t.Run("by extension", func(t *testing.T) {
		got := parseTSStringMap(t, text, "const BY_EXT: Record<string, LangKey> = {", "};")
		assertSameStringMap(t, "BY_EXT", got, langByExt)
	})

	t.Run("by filename", func(t *testing.T) {
		got := parseTSStringMap(t, text, "const BY_FILENAME: Record<string, LangKey> = {", "};")
		assertSameStringMap(t, "BY_FILENAME", got, langByFilename)
	})

	t.Run("vendored dirs", func(t *testing.T) {
		block := sliceBetween(t, text, "const VENDORED_DIRS = new Set([", "]);")
		want := map[string]bool{}
		for _, m := range regexp.MustCompile(`'([^']+)'`).FindAllStringSubmatch(block, -1) {
			want[m[1]] = true
		}
		if len(want) == 0 {
			t.Fatal("parsed no vendored directories")
		}
		for dir := range want {
			if !vendoredDirs[dir] {
				t.Errorf("%q is vendored in languages.ts and not in vendoredDirs", dir)
			}
		}
		for dir := range vendoredDirs {
			if !want[dir] {
				t.Errorf("%q is vendored in Go and not in languages.ts", dir)
			}
		}
	})
}

// sliceBetween returns the text between the first `open` and the next `closer` after it.
func sliceBetween(t *testing.T, text, open, closer string) string {
	t.Helper()
	start := strings.Index(text, open)
	if start < 0 {
		t.Fatalf("languages.ts no longer contains %q", open)
	}
	rest := text[start+len(open):]
	end := strings.Index(rest, closer)
	if end < 0 {
		t.Fatalf("languages.ts has no %q after %q", closer, open)
	}
	return rest[:end]
}

// parseTSStringMap reads `key: 'value'` pairs, quoted or bare, out of one object literal.
func parseTSStringMap(t *testing.T, text, open, closer string) map[string]string {
	t.Helper()
	re := regexp.MustCompile(`(?:'([^']+)'|([\w.+-]+))\s*:\s*'([^']+)'`)
	out := map[string]string{}
	for _, m := range re.FindAllStringSubmatch(sliceBetween(t, text, open, closer), -1) {
		out[m[1]+m[2]] = m[3]
	}
	if len(out) == 0 {
		t.Fatalf("parsed no entries out of %q", open)
	}
	return out
}

func assertSameStringMap(t *testing.T, name string, want, got map[string]string) {
	t.Helper()
	for k, v := range want {
		switch g, ok := got[k]; {
		case !ok:
			t.Errorf("%s[%q] = %q in languages.ts, missing in Go", name, k, v)
		case g != v:
			t.Errorf("%s[%q] = %q in Go, %q in languages.ts", name, k, g, v)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("%s[%q] exists in Go and not in languages.ts", name, k)
		}
	}
}

func assertStats(t *testing.T, got, want []model.LanguageStat) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d stats %+v, want %d %+v", len(got), got, len(want), want)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Name != w.Name || g.Bytes != w.Bytes || g.Percent != w.Percent {
			t.Errorf("stat %d = %+v, want %+v", i, g, w)
		}
		switch {
		case g.Color == nil && w.Color != nil:
			t.Errorf("stat %d colour = nil, want %q", i, *w.Color)
		case g.Color != nil && w.Color == nil:
			t.Errorf("stat %d colour = %q, want nil", i, *g.Color)
		case g.Color != nil && w.Color != nil && *g.Color != *w.Color:
			t.Errorf("stat %d colour = %q, want %q", i, *g.Color, *w.Color)
		}
	}
}
