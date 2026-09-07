package ingest

import (
	"math"
	"regexp"
	"sort"
	"strings"

	"frznforge/internal/model"
)

// languageDef is one entry of the language map — deliberately small (no linguist); the colours
// follow GitHub's linguist palette.
type languageDef struct {
	Name  string
	Color *string
	// Prose marks documentation, data and config languages: listed per file, excluded from
	// the per-repo statistics.
	Prose bool
}

func color(hex string) *string { return &hex }

// langs is keyed the way languages.ts keys it, so the two files can be diffed side by side.
var langs = map[string]languageDef{
	"TypeScript":       {"TypeScript", color("#3178c6"), false},
	"JavaScript":       {"JavaScript", color("#f1e05a"), false},
	"Python":           {"Python", color("#3572a5"), false},
	"Go":               {"Go", color("#00add8"), false},
	"Rust":             {"Rust", color("#dea584"), false},
	"Svelte":           {"Svelte", color("#ff3e00"), false},
	"Astro":            {"Astro", color("#ff5a03"), false},
	"Vue":              {"Vue", color("#41b883"), false},
	"HTML":             {"HTML", color("#e34c26"), false},
	"CSS":              {"CSS", color("#663399"), false},
	"SCSS":             {"SCSS", color("#c6538c"), false},
	"Sass":             {"Sass", color("#a53b70"), false},
	"Less":             {"Less", color("#1d365d"), false},
	"Shell":            {"Shell", color("#89e051"), false},
	"PowerShell":       {"PowerShell", color("#012456"), false},
	"Batchfile":        {"Batchfile", color("#c1f12e"), false},
	"Dockerfile":       {"Dockerfile", color("#384d54"), false},
	"Makefile":         {"Makefile", color("#427819"), false},
	"CMake":            {"CMake", color("#da3434"), false},
	"C":                {"C", color("#555555"), false},
	"C++":              {"C++", color("#f34b7d"), false},
	"C#":               {"C#", color("#178600"), false},
	"Objective-C":      {"Objective-C", color("#438eff"), false},
	"Java":             {"Java", color("#b07219"), false},
	"Kotlin":           {"Kotlin", color("#a97bff"), false},
	"Scala":            {"Scala", color("#c22d40"), false},
	"Groovy":           {"Groovy", color("#4298b8"), false},
	"Swift":            {"Swift", color("#f05138"), false},
	"Ruby":             {"Ruby", color("#701516"), false},
	"PHP":              {"PHP", color("#4f5d95"), false},
	"Perl":             {"Perl", color("#0298c3"), false},
	"Lua":              {"Lua", color("#000080"), false},
	"R":                {"R", color("#198ce7"), false},
	"Julia":            {"Julia", color("#a270ba"), false},
	"Dart":             {"Dart", color("#00b4ab"), false},
	"Elixir":           {"Elixir", color("#6e4a7e"), false},
	"Erlang":           {"Erlang", color("#b83998"), false},
	"Haskell":          {"Haskell", color("#5e5086"), false},
	"OCaml":            {"OCaml", color("#ef7a08"), false},
	"F#":               {"F#", color("#b845fc"), false},
	"Clojure":          {"Clojure", color("#db5855"), false},
	"Elm":              {"Elm", color("#60b5cc"), false},
	"Zig":              {"Zig", color("#ec915c"), false},
	"Nim":              {"Nim", color("#ffc200"), false},
	"Crystal":          {"Crystal", color("#000100"), false},
	"SQL":              {"SQL", color("#e38c00"), false},
	"Nix":              {"Nix", color("#7e7eff"), false},
	"Terraform":        {"HCL", color("#844fba"), false},
	"Protobuf":         {"Protocol Buffer", nil, false},
	"GraphQL":          {"GraphQL", color("#e10098"), false},
	"Jupyter Notebook": {"Jupyter Notebook", color("#da5b0b"), false},
	"TeX":              {"TeX", color("#3d6117"), false},
	"Assembly":         {"Assembly", color("#6e4c13"), false},
	"WebAssembly":      {"WebAssembly", color("#04133b"), false},
	"Solidity":         {"Solidity", color("#aa6746"), false},
	"Vim":              {"Vim Script", color("#199f4b"), false},
	"Emacs Lisp":       {"Emacs Lisp", color("#c065db"), false},
	"Common Lisp":      {"Common Lisp", color("#3fb68b"), false},
	"Scheme":           {"Scheme", color("#1e4aec"), false},
	"Fortran":          {"Fortran", color("#4d41b1"), false},
	"Pascal":           {"Pascal", color("#e3f171"), false},
	"Ada":              {"Ada", color("#02f88c"), false},
	"D":                {"D", color("#ba595e"), false},
	"V":                {"V", color("#4f87c4"), false},
	"Gleam":            {"Gleam", color("#ffaff3"), false},
	"CoffeeScript":     {"CoffeeScript", color("#244776"), false},
	"Handlebars":       {"Handlebars", color("#f7931e"), false},
	"Pug":              {"Pug", color("#a86454"), false},
	"MDX":              {"MDX", color("#fcb32c"), false},

	// prose / data / config — listed per file, excluded from stats
	"Markdown":         {"Markdown", color("#083fa1"), true},
	"JSON":             {"JSON", color("#292929"), true},
	"YAML":             {"YAML", color("#cb171e"), true},
	"TOML":             {"TOML", color("#9c4221"), true},
	"XML":              {"XML", color("#0060ac"), true},
	"INI":              {"INI", color("#d1dbe0"), true},
	"CSV":              {"CSV", nil, true},
	"Text":             {"Text", nil, true},
	"reStructuredText": {"reStructuredText", color("#141414"), true},
	"AsciiDoc":         {"AsciiDoc", color("#73a0c5"), true},
	"SVG":              {"SVG", color("#ff9900"), true},
	"Ignore":           {"Ignore List", color("#000000"), true},
	"Lockfile":         {"Lockfile", nil, true},
	"EditorConfig":     {"EditorConfig", color("#fff1f2"), true},
	"Git Attributes":   {"Git Attributes", color("#f44d27"), true},
	"Git Config":       {"Git Config", color("#f44d27"), true},
}

var langByExt = map[string]string{
	"ts": "TypeScript", "tsx": "TypeScript", "mts": "TypeScript", "cts": "TypeScript",
	"js": "JavaScript", "jsx": "JavaScript", "mjs": "JavaScript", "cjs": "JavaScript",
	"py": "Python", "pyi": "Python", "pyw": "Python",
	"go":     "Go",
	"rs":     "Rust",
	"svelte": "Svelte",
	"astro":  "Astro",
	"vue":    "Vue",
	"html":   "HTML", "htm": "HTML", "xhtml": "HTML",
	"css":  "CSS",
	"scss": "SCSS",
	"sass": "Sass",
	"less": "Less",
	"sh":   "Shell", "bash": "Shell", "zsh": "Shell", "fish": "Shell", "ksh": "Shell",
	"ps1": "PowerShell", "psm1": "PowerShell", "psd1": "PowerShell",
	"bat": "Batchfile", "cmd": "Batchfile",
	"dockerfile": "Dockerfile",
	"mk":         "Makefile", "mak": "Makefile",
	"cmake": "CMake",
	"c":     "C", "h": "C",
	"cpp": "C++", "cc": "C++", "cxx": "C++", "c++": "C++", "hpp": "C++", "hh": "C++", "hxx": "C++", "inl": "C++",
	"cs": "C#", "csx": "C#",
	"m": "Objective-C", "mm": "Objective-C",
	"java": "Java",
	"kt":   "Kotlin", "kts": "Kotlin",
	"scala": "Scala", "sc": "Scala",
	"groovy": "Groovy", "gradle": "Groovy",
	"swift": "Swift",
	"rb":    "Ruby", "rake": "Ruby", "gemspec": "Ruby",
	"php": "PHP", "phtml": "PHP",
	"pl": "Perl", "pm": "Perl",
	"lua": "Lua",
	"r":   "R", "rmd": "R",
	"jl":   "Julia",
	"dart": "Dart",
	"ex":   "Elixir", "exs": "Elixir",
	"erl": "Erlang", "hrl": "Erlang",
	"hs": "Haskell", "lhs": "Haskell",
	"ml": "OCaml", "mli": "OCaml",
	"fs": "F#", "fsi": "F#", "fsx": "F#",
	"clj": "Clojure", "cljs": "Clojure", "cljc": "Clojure", "edn": "Clojure",
	"elm": "Elm",
	"zig": "Zig",
	"nim": "Nim",
	"cr":  "Crystal",
	"sql": "SQL",
	"nix": "Nix",
	"tf":  "Terraform", "tfvars": "Terraform", "hcl": "Terraform",
	"proto":   "Protobuf",
	"graphql": "GraphQL", "gql": "GraphQL",
	"ipynb": "Jupyter Notebook",
	"tex":   "TeX", "sty": "TeX", "cls": "TeX", "bib": "TeX",
	"asm": "Assembly", "s": "Assembly",
	"wat": "WebAssembly", "wast": "WebAssembly",
	"sol":  "Solidity",
	"vim":  "Vim",
	"el":   "Emacs Lisp",
	"lisp": "Common Lisp", "lsp": "Common Lisp",
	"scm": "Scheme", "ss": "Scheme", "rkt": "Scheme",
	"f": "Fortran", "f90": "Fortran", "f95": "Fortran", "f03": "Fortran", "for": "Fortran",
	"pas": "Pascal", "pp": "Pascal",
	"adb": "Ada", "ads": "Ada",
	"d":      "D",
	"v":      "V",
	"gleam":  "Gleam",
	"coffee": "CoffeeScript",
	"hbs":    "Handlebars", "handlebars": "Handlebars",
	"pug": "Pug", "jade": "Pug",
	"mdx": "MDX",
	"md":  "Markdown", "markdown": "Markdown", "mdown": "Markdown", "mkd": "Markdown",
	"json": "JSON", "jsonc": "JSON", "json5": "JSON", "webmanifest": "JSON", "geojson": "JSON",
	"yml": "YAML", "yaml": "YAML",
	"toml": "TOML",
	"xml":  "XML", "xsl": "XML", "xsd": "XML", "plist": "XML", "csproj": "XML", "props": "XML", "targets": "XML",
	"ini": "INI", "cfg": "INI", "conf": "INI",
	"csv": "CSV", "tsv": "CSV",
	"txt": "Text", "text": "Text",
	"rst":  "reStructuredText",
	"adoc": "AsciiDoc", "asciidoc": "AsciiDoc",
	"svg":  "SVG",
	"lock": "Lockfile",
}

var langByFilename = map[string]string{
	"dockerfile":        "Dockerfile",
	"containerfile":     "Dockerfile",
	"makefile":          "Makefile",
	"gnumakefile":       "Makefile",
	"cmakelists.txt":    "CMake",
	"rakefile":          "Ruby",
	"gemfile":           "Ruby",
	"podfile":           "Ruby",
	"brewfile":          "Ruby",
	"vagrantfile":       "Ruby",
	"justfile":          "Makefile",
	"package-lock.json": "Lockfile",
	"yarn.lock":         "Lockfile",
	"pnpm-lock.yaml":    "Lockfile",
	"bun.lockb":         "Lockfile",
	"cargo.lock":        "Lockfile",
	"go.sum":            "Lockfile",
	"poetry.lock":       "Lockfile",
	"composer.lock":     "Lockfile",
	"gemfile.lock":      "Lockfile",
	"flake.lock":        "Lockfile",
	".gitignore":        "Ignore",
	".dockerignore":     "Ignore",
	".npmignore":        "Ignore",
	".prettierignore":   "Ignore",
	".eslintignore":     "Ignore",
	".gitattributes":    "Git Attributes",
	".gitmodules":       "Git Config",
	".gitconfig":        "Git Config",
	".editorconfig":     "EditorConfig",
	".bashrc":           "Shell",
	".bash_profile":     "Shell",
	".zshrc":            "Shell",
	".profile":          "Shell",
	"go.mod":            "Text",
	"license":           "Text",
	"licence":           "Text",
	"copying":           "Text",
	"readme":            "Text",
	"authors":           "Text",
	"changelog":         "Text",
	"contributors":      "Text",
	"version":           "Text",
}

// vendoredDirs are path segments that mark vendored or generated content, excluded from stats.
var vendoredDirs = map[string]bool{
	"node_modules": true, "vendor": true, "dist": true, "build": true, ".git": true,
	"bower_components": true, "third_party": true, "__pycache__": true, ".venv": true, "venv": true,
}

// minifiedRe is `/\.min\.[a-z0-9]+$/i` written out longhand.
//
// The `i` flag is spelled as explicit classes rather than Go's `(?i)`, because the two do not
// agree: Go folds Unicode, so `(?i)[a-z]` also matches U+212A KELVIN SIGN and U+017F LATIN
// SMALL LETTER LONG S, while JavaScript's non-unicode `i` deliberately refuses to canonicalise
// a non-ASCII character onto an ASCII one. `foo.min.ſ` is a silly filename, but it would be
// vendored under one implementation and counted under the other.
var minifiedRe = regexp.MustCompile(`\.[mM][iI][nN]\.[a-zA-Z0-9]+$`)

func pathBase(p string) string {
	if i := strings.LastIndexByte(p, '/'); i != -1 {
		return p[i+1:]
	}
	return p
}

// detectLanguageDef resolves a path to a language definition, or nil when unknown.
func detectLanguageDef(path string) *languageDef {
	base := pathBase(path)
	lower := strings.ToLower(base)
	if key, ok := langByFilename[lower]; ok {
		def := langs[key]
		return &def
	}
	// "Dockerfile.dev", "Makefile.am"
	if strings.HasPrefix(lower, "dockerfile.") {
		def := langs["Dockerfile"]
		return &def
	}
	if strings.HasPrefix(lower, "makefile.") {
		def := langs["Makefile"]
		return &def
	}
	dot := strings.LastIndexByte(lower, '.')
	if dot <= 0 {
		return nil
	}
	if key, ok := langByExt[lower[dot+1:]]; ok {
		def := langs[key]
		return &def
	}
	return nil
}

// DetectLanguage names the language of a path, or nil when unknown.
func DetectLanguage(path string) *string {
	def := detectLanguageDef(path)
	if def == nil {
		return nil
	}
	name := def.Name
	return &name
}

// langColors indexes langs by DISPLAY name, which is not always the key: "Terraform" is shown
// as "HCL", "Protobuf" as "Protocol Buffer", "Vim" as "Vim Script", "Ignore" as "Ignore List".
//
// The TypeScript scans Object.values(LANGS) and takes the first match, i.e. declaration order.
// A Go map has no declaration order, so on the (currently impossible) collision this keeps the
// entry with the smallest key rather than whichever the runtime happened to visit first —
// languageColor must not be able to return different bytes on two runs of the same build.
// TestLanguageNamesAreUnique holds the "currently impossible" part.
var langColors = func() map[string]*string {
	out := make(map[string]*string, len(langs))
	winner := make(map[string]string, len(langs))
	for key, def := range langs {
		if held, taken := winner[def.Name]; taken && held <= key {
			continue
		}
		winner[def.Name] = key
		out[def.Name] = def.Color
	}
	return out
}()

// languageColor is the bar colour for a language name (nil when the map has none, and nil for
// the handful of languages that are deliberately colourless — the UI falls back to a neutral).
func languageColor(name string) *string {
	return langColors[name]
}

// IsVendoredPath reports whether a path sits in a vendored or generated location, or is a
// minified asset. Both language stats and the insights code-size series exclude these.
func IsVendoredPath(path string) bool {
	segs := strings.Split(path, "/")
	for _, s := range segs[:len(segs)-1] {
		if vendoredDirs[strings.ToLower(s)] {
			return true
		}
	}
	return minifiedRe.MatchString(segs[len(segs)-1])
}

// countsTowardStats reports whether a file contributes to the language breakdown.
func countsTowardStats(f model.FileInfo) bool {
	if f.Binary || f.Language == nil {
		return false
	}
	if IsVendoredPath(f.Path) {
		return false
	}
	def := detectLanguageDef(f.Path)
	return def != nil && !def.Prose
}

// LanguageStats computes bytes per language over the given files (docs, config, vendored and
// binary excluded), sorted by bytes descending then name, with percents summing to 100 —
// rounding drift is folded into the largest entry so the bars always fill the row.
func LanguageStats(files map[string]model.FileInfo) []model.LanguageStat {
	bytesPer := map[string]int64{}
	// Iterated in path order rather than map order: the sums are integers so the order cannot
	// change them, but a deterministic walk keeps this debuggable.
	for _, path := range sortedKeys(files) {
		f := files[path]
		if !countsTowardStats(f) {
			continue
		}
		bytesPer[*f.Language] += f.Size
	}
	// Never `return nil` on the empty path: the artifact says `[]`, and a nil slice marshals
	// as `null`.
	stats := []model.LanguageStat{}
	names := sortedKeys(bytesPer)
	var total int64
	for _, name := range names {
		total += bytesPer[name]
	}
	if total == 0 {
		return stats
	}
	for _, name := range names {
		b := bytesPer[name]
		stats = append(stats, model.LanguageStat{
			Name:  name,
			Bytes: b,
			// Math.round(x) and math.Round(x) part company on exact .5 for NEGATIVE x only
			// (JavaScript rounds toward +∞, Go away from zero). b and total are both
			// non-negative, so this — and the drift arithmetic below — agree everywhere.
			Percent: math.Round(float64(b)/float64(total)*1000) / 10,
			Color:   languageColor(name),
		})
	}
	sort.SliceStable(stats, func(i, j int) bool {
		if stats[i].Bytes != stats[j].Bytes {
			return stats[i].Bytes > stats[j].Bytes
		}
		return stats[i].Name < stats[j].Name
	})
	// Summed AFTER the sort, in the sorted order, because float addition is not associative:
	// summing in name order instead could land a different last bit and flip the drift.
	var sum float64
	for _, s := range stats {
		sum += s.Percent
	}
	drift := math.Round((100-sum)*10) / 10
	if drift != 0 && len(stats) > 0 {
		stats[0].Percent = math.Round((stats[0].Percent+drift)*10) / 10
	}
	return stats
}

// sortedKeys lists a map's keys in code-point order. Every ordered walk over a map in this
// package goes through it — Go randomises map iteration, and the artifact must not.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// LanguageNames is every language name ingest can put on a file, sorted.
//
// Exported for internal/highlight's sync test, which has to check that the highlighter's map
// covers everything ingest can label — a name ingest emits that the highlighter does not know is
// a file rendered with no colour, which is how `.cfg` and `.conf` shipped uncoloured for a
// version. That test used to derive the list by parsing src/lib/ingest/languages.ts; Phase 9
// deleted it, and the test skipped from then on rather than failing, so it stopped covering
// anything at all. Reading the real map is both simpler and stronger than parsing a copy of it.
func LanguageNames() []string {
	out := make([]string, 0, len(langs))
	for _, def := range langs {
		out = append(out, def.Name)
	}
	sort.Strings(out)
	return out
}
