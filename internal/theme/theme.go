// Package theme reads the palette tokens out of the stylesheets and does the WCAG colour maths
// on them.
//
// It exists as build-time code rather than only as a browser check for a mundane reason, worth
// writing down because it cost six phases: the light palettes shipped with every brand hue
// failing WCAG AA — ember 2.7:1, amber 2.2:1, ice 3.1:1, on every page type — and nothing
// caught it. A browser-based checker only sees the pairs a page happens to render in the theme
// it happens to be in, and the code gutter is a `::before`, which axe and Lighthouse both skip
// entirely. Reading the tokens directly checks every pair someone thought to list, in both
// palettes and both themes, without rendering anything.
//
// tests/e2e/a11y.spec.ts does the complementary half: it pins `data-theme` explicitly and
// measures what the pages actually paint, catching pairs nobody listed here.
package theme

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// RGB is an 0-255 colour. Channels are float64 because compositing a translucent tint over an
// opaque background lands between integers, and rounding there would move a ratio.
type RGB [3]float64

// AA is the WCAG 1.4.3 contrast floor for text below 18.66px bold / 24px regular — which every
// pair this package checks is.
const AA = 4.5

var (
	rgbaRe   = regexp.MustCompile(`rgba?\(\s*([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)(?:[,\s/]+([\d.]+))?\s*\)`)
	tokenRe  = regexp.MustCompile(`(--hf-[\w-]+)\s*:\s*([^;]+);`)
	varRefRe = regexp.MustCompile(`var\((--hf-[\w-]+)\)`)
)

// ParseHex reads #abc or #aabbcc.
func ParseHex(value string) (RGB, error) {
	h := strings.TrimPrefix(strings.TrimSpace(value), "#")
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	if len(h) != 6 {
		return RGB{}, fmt.Errorf("not a hex colour: %q", value)
	}
	var out RGB
	for i := 0; i < 3; i++ {
		v, err := strconv.ParseUint(h[i*2:i*2+2], 16, 8)
		if err != nil {
			return RGB{}, fmt.Errorf("not a hex colour: %q", value)
		}
		out[i] = float64(v)
	}
	return out, nil
}

// ParseRGBA reads `rgba(r, g, b, a)` into channels plus alpha. A colour with no alpha is opaque.
func ParseRGBA(value string) (RGB, float64, error) {
	m := rgbaRe.FindStringSubmatch(value)
	if m == nil {
		return RGB{}, 0, fmt.Errorf("not an rgb(a) value: %q", value)
	}
	var out RGB
	for i := 0; i < 3; i++ {
		v, err := strconv.ParseFloat(m[i+1], 64)
		if err != nil {
			return RGB{}, 0, err
		}
		out[i] = v
	}
	alpha := 1.0
	if m[4] != "" {
		a, err := strconv.ParseFloat(m[4], 64)
		if err != nil {
			return RGB{}, 0, err
		}
		alpha = a
	}
	return out, alpha, nil
}

// Composite flattens a translucent colour onto an opaque one — what the browser actually paints,
// and therefore what the ratio has to be measured against.
func Composite(fg RGB, alpha float64, bg RGB) RGB {
	var out RGB
	for i := range out {
		out[i] = alpha*fg[i] + (1-alpha)*bg[i]
	}
	return out
}

func relativeLuminance(c RGB) float64 {
	chan_ := func(v float64) float64 {
		s := v / 255
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*chan_(c[0]) + 0.7152*chan_(c[1]) + 0.0722*chan_(c[2])
}

// Contrast is the WCAG contrast ratio between two colours.
func Contrast(a, b RGB) float64 {
	l1, l2 := relativeLuminance(a), relativeLuminance(b)
	return (math.Max(l1, l2) + 0.05) / (math.Min(l1, l2) + 0.05)
}

// TokenBlock reads every `--hf-*` declaration inside the block that starts with `selector {`.
func TokenBlock(css, selector string) (map[string]string, error) {
	start := strings.Index(css, selector+" {")
	if start == -1 {
		return nil, fmt.Errorf("no such block: %s", selector)
	}
	open := strings.Index(css[start:], "{")
	if open == -1 {
		return nil, fmt.Errorf("unterminated block: %s", selector)
	}
	open += start
	end := strings.Index(css[open:], "\n}")
	if end == -1 {
		return nil, fmt.Errorf("unterminated block: %s", selector)
	}
	body := css[open+1 : open+end]

	tokens := map[string]string{}
	for _, m := range tokenRe.FindAllStringSubmatch(body, -1) {
		tokens[m[1]] = strings.TrimSpace(m[2])
	}
	return tokens, nil
}

// Layer copies base and applies overrides on top — the frost and dark blocks only override, so
// they layer onto `:root`.
func Layer(base, overrides map[string]string) map[string]string {
	out := make(map[string]string, len(base))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overrides {
		out[k] = v
	}
	return out
}

// Resolve expands a `var(--hf-x)` reference one level, which is how `--hf-accent` is written.
func Resolve(tokens map[string]string, value string) string {
	return varRefRe.ReplaceAllStringFunc(value, func(ref string) string {
		m := varRefRe.FindStringSubmatch(ref)
		if v, ok := tokens[m[1]]; ok {
			return v
		}
		return ref
	})
}
