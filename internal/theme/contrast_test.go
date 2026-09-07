package theme

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// The WCAG AA floor for the palettes, read straight out of the stylesheets — the port of
// tests/unit/contrast.test.ts.
//
// Each entry below is "this ink, on these backgrounds". The background list is not decorative:
// it is where that colour is actually painted, and `why` names the rules that paint it, so a
// reader can check the claim rather than trust it.

type ink struct {
	// token is the CSS custom property, or a literal for the two hard-coded diff colours.
	token string
	// on are opaque background tokens it is painted on.
	on []string
	// onTints are translucent tint tokens it is painted on, each flattened over BOTH the card
	// surface and the page canvas — a chip's tint sits on either, and both have to clear.
	onTints []string
	why     string
}

var inks = []ink{
	{"--hf-ice", []string{"--hf-surface", "--hf-surface-2", "--hf-canvas", "--hf-canvas-2"}, []string{"--hf-ice-soft"},
		".hf-sha, .hf-age.t-cold, .hf-path-crumb links, .hf-tag, .hf-more, .hf-sha-link code"},
	{"--hf-ember", []string{"--hf-surface", "--hf-surface-2", "--hf-canvas", "--hf-canvas-2"}, []string{"--hf-ember-soft"},
		".hf-age.t-hot, .hf-tag.hf-release-latest, .hf-tagkind--annotated"},
	{"--hf-amber", []string{"--hf-surface", "--hf-surface-2", "--hf-canvas", "--hf-canvas-2"}, []string{"--hf-amber-soft"},
		".hf-footer-warn, .hf-release-pre, .hf-age.t-warm, .hf-tag--template"},
	{"--hf-text-2", []string{"--hf-surface", "--hf-surface-2", "--hf-canvas", "--hf-canvas-2"}, nil,
		"body copy in .hf-md, .hf-commit-author, .hf-blob-meta"},
	{"--hf-text-3", []string{"--hf-surface", "--hf-surface-2", "--hf-canvas", "--hf-canvas-2"}, []string{"--hf-code-bg"},
		".hf-muted, table headers, .hf-kpi-sub, inline <code> in prose"},
}

// palettes are the four token sets the site can render in: two palettes × two themes.
var palettes = []struct {
	name     string
	selector string // layered on top of :root; empty means :root alone
	dark     bool   // selects the dark overrides of the two literal diff colours
}{
	{"hearth light", "", false},
	{"frost light", `:root[data-palette="frost"]`, false},
	{"hearth dark", `:root[data-theme="dark"]`, true},
	{"frost dark", `:root[data-palette="frost"][data-theme="dark"]`, true},
}

func TestPalettesClearAA(t *testing.T) {
	globalCSS, repoCSS := stylesheets(t)

	for _, p := range palettes {
		t.Run(p.name, func(t *testing.T) {
			tokens := paletteTokens(t, globalCSS, p.selector)
			rgb := func(token string) RGB {
				t.Helper()
				v, ok := tokens[token]
				if !ok {
					t.Fatalf("no such token: %s", token)
				}
				c, err := ParseHex(Resolve(tokens, v))
				if err != nil {
					t.Fatalf("%s: %v", token, err)
				}
				return c
			}
			check := func(label string, got float64) {
				t.Helper()
				if got < AA {
					t.Errorf("%s: %.2f, below the AA floor of %.1f", label, got, AA)
				}
			}

			for _, in := range inks {
				fg := rgb(in.token)
				for _, bg := range in.on {
					check(fmt.Sprintf("%s on %s (%s)", in.token, bg, in.why), Contrast(fg, rgb(bg)))
				}
				for _, tint := range in.onTints {
					tintRGB, alpha, err := ParseRGBA(tokens[tint])
					if err != nil {
						t.Fatalf("%s: %v", tint, err)
					}
					for _, under := range []string{"--hf-surface", "--hf-canvas"} {
						flat := Composite(tintRGB, alpha, rgb(under))
						check(fmt.Sprintf("%s on %s over %s", in.token, tint, under), Contrast(fg, flat))
					}
				}
			}

			// The code gutter is a `::before`, so no browser-based checker will ever look at it —
			// axe and Lighthouse both skip pseudo-element text. It is still text, and still
			// --hf-text-3 behind an opacity, so it is checked here or nowhere.
			opacity := gutterOpacity(t, repoCSS)
			for _, bg := range []string{"--hf-surface", "--hf-surface-2"} {
				painted := Composite(rgb("--hf-text-3"), opacity, rgb(bg))
				check(fmt.Sprintf("code line numbers on %s", bg), Contrast(painted, rgb(bg)))
			}

			// The diff colours are literals in repo.css rather than tokens — they are github's,
			// not the palette's — but they are still 12px text on a card.
			for _, literal := range diffColours(t, repoCSS, p.dark) {
				c, err := ParseHex(literal)
				if err != nil {
					t.Fatal(err)
				}
				for _, bg := range []string{"--hf-surface", "--hf-surface-2", "--hf-canvas"} {
					check(fmt.Sprintf("diff colour %s on %s", literal, bg), Contrast(c, rgb(bg)))
				}
			}

			// --hf-accent is the colour a primary button is painted IN, with --hf-on-accent
			// written on top. Darkening the accent for contrast in one direction must not break
			// it in the other.
			check("--hf-on-accent on --hf-accent", Contrast(rgb("--hf-accent"), rgb("--hf-on-accent")))
		})
	}
}

// TestContrastMathsMatchesKnownValues pins the formulae themselves against values anyone can
// verify by hand, so a failure above is read as "the palette moved" and not "the maths is wrong".
func TestContrastMathsMatchesKnownValues(t *testing.T) {
	white, black := RGB{255, 255, 255}, RGB{0, 0, 0}
	if got := Contrast(white, black); got < 20.99 || got > 21.01 {
		t.Errorf("black on white = %.2f, want 21", got)
	}
	if got := Contrast(white, white); got < 0.99 || got > 1.01 {
		t.Errorf("white on white = %.2f, want 1", got)
	}
	// Half-transparent black over white is mid grey.
	if got := Composite(black, 0.5, white); got[0] < 127 || got[0] > 128 {
		t.Errorf("compositing gave %v, want ~127.5 per channel", got)
	}
	if c, err := ParseHex("#abc"); err != nil || c != (RGB{0xAA, 0xBB, 0xCC}) {
		t.Errorf("three-digit hex: %v %v", c, err)
	}
}

func stylesheets(t *testing.T) (globalCSS, repoCSS string) {
	t.Helper()
	root := moduleRoot(t)
	read := func(name string) string {
		raw, err := os.ReadFile(filepath.Join(root, "web", "css", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(raw)
	}
	return read("global.css"), read("repo.css")
}

func paletteTokens(t *testing.T, css, selector string) map[string]string {
	t.Helper()
	base, err := TokenBlock(css, ":root")
	if err != nil {
		t.Fatal(err)
	}
	if selector == "" {
		return base
	}
	over, err := TokenBlock(css, selector)
	if err != nil {
		t.Fatal(err)
	}
	return Layer(base, over)
}

var (
	gutterRe = regexp.MustCompile(`(?s)\.hf-code \.line::before \{.*?opacity: ([\d.]+);`)
	addsRe   = regexp.MustCompile(`(?m)^\.hf-adds \{ color: (#[0-9a-f]{6});`)
	delsRe   = regexp.MustCompile(`(?m)^\.hf-dels \{ color: (#[0-9a-f]{6});`)
	// The dark theme repaints both, so checking the light literals against dark surfaces would
	// invent a failure that no reader ever sees.
	darkAddsRe = regexp.MustCompile(`:root\[data-theme="dark"\] \.hf-adds \{ color: (#[0-9a-f]{6});`)
	darkDelsRe = regexp.MustCompile(`:root\[data-theme="dark"\] \.hf-dels \{ color: (#[0-9a-f]{6});`)
)

func gutterOpacity(t *testing.T, repoCSS string) float64 {
	t.Helper()
	m := gutterRe.FindStringSubmatch(repoCSS)
	if m == nil {
		t.Fatal("line-number opacity not found in repo.css — the gutter rule moved, and this check went quiet with it")
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil || v <= 0 {
		t.Fatalf("line-number opacity %q is not a positive number", m[1])
	}
	return v
}

func diffColours(t *testing.T, repoCSS string, dark bool) []string {
	t.Helper()
	a, d := addsRe, delsRe
	if dark {
		a, d = darkAddsRe, darkDelsRe
	}
	adds := a.FindStringSubmatch(repoCSS)
	dels := d.FindStringSubmatch(repoCSS)
	if adds == nil || dels == nil {
		t.Fatalf(".hf-adds / .hf-dels colours not found in repo.css (dark=%v) — the rules moved, and this check went quiet with them", dark)
	}
	return []string{adds[1], dels[1]}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("could not find the module root")
	return ""
}
