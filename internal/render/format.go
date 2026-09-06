package render

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"frznforge/internal/config"
)

// This file is the SERVER half of a genuine two-language pair. The browser half is
// web/js/format.js, which `<hf-repo-listing>` uses to rebuild cards client-side; the two must
// produce identical strings or a card changes the moment it is re-rendered.
//
// Unlike the TypeScript/JavaScript split — which 0.4.0 collapsed into one implementation —
// this duplication is unavoidable: the same logic has to run in Go at build time and in a
// browser at view time. It is held together by tests/fixtures/format-cases.json, a shared
// golden that both test suites read. Neither side may gain a behaviour the other lacks
// without that file growing in the same change.

const dayMillis = 86_400_000

// HeatFor maps an age to a heat bucket: hot, warm, neutral, cool or cold.
//
// `now` is a parameter rather than a call to time.Now so a build is reproducible and a test
// can pin it — the same reason the JavaScript takes it.
func HeatFor(date string, now time.Time, t config.HeatConfig) string {
	if date == "" {
		return "cold"
	}
	d, err := time.Parse(time.RFC3339, date)
	if err != nil {
		return "cold"
	}
	age := now.Sub(d).Milliseconds()
	switch {
	case age < int64(t.Hot)*dayMillis:
		return "hot"
	case age < int64(t.Warm)*dayMillis:
		return "warm"
	case age < int64(t.Neutral)*dayMillis:
		return "neutral"
	case age < int64(t.Cool)*dayMillis:
		return "cool"
	}
	return "cold"
}

// relativeUnit mirrors the JS units table exactly, including 4.345 weeks to a month — an
// approximation, but it has to be the SAME approximation on both sides.
type relativeUnit struct {
	div  float64
	name string
}

var relativeUnits = []relativeUnit{
	{60, "second"},
	{60, "minute"},
	{24, "hour"},
	{7, "day"},
	{4.345, "week"},
	{12, "month"},
	{-1, "year"}, // -1 stands for Infinity: the loop always breaks here
}

// RelativeTime renders "2 hours ago", "3 weeks ago", "2 years ago".
func RelativeTime(date string, now time.Time) string {
	d, ok := parseDate(date)
	if !ok {
		return "—"
	}
	seconds := math0(round(now.Sub(d).Seconds()))
	value := seconds
	name := "second"
	for _, u := range relativeUnits {
		name = u.name
		if u.div < 0 || value < u.div {
			break
		}
		value = value / u.div
	}
	if name == "second" {
		return "just now"
	}
	n := int64(value) // JS Math.floor on a non-negative value
	suffix := "s"
	if n == 1 {
		suffix = ""
	}
	return fmt.Sprintf("%d %s%s ago", n, name, suffix)
}

// RelativeTimeShort renders a compact age: "2h", "3w", "4mo", "1y".
func RelativeTimeShort(date string, now time.Time) string {
	d, ok := parseDate(date)
	if !ok {
		return "—"
	}
	s := math0(round(now.Sub(d).Seconds()))
	if s < 60 {
		return "now"
	}
	m := s / 60
	if m < 60 {
		return fmt.Sprintf("%dm", int64(m))
	}
	h := m / 60
	if h < 24 {
		return fmt.Sprintf("%dh", int64(h))
	}
	days := h / 24
	switch {
	case days < 7:
		return fmt.Sprintf("%dd", int64(days))
	case days < 30:
		return fmt.Sprintf("%dw", int64(days/7))
	case days < 365:
		return fmt.Sprintf("%dmo", int64(days/30.44))
	}
	return fmt.Sprintf("%dy", int64(days/365.25))
}

var monthNames = [...]string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}

// MonthYear renders "Sep 2020", in UTC — matching the JS call, which pins timeZone: 'UTC'
// precisely so the string does not depend on where the build ran.
func MonthYear(date string) string {
	d, ok := parseDate(date)
	if !ok {
		return "—"
	}
	u := d.UTC()
	return fmt.Sprintf("%s %d", monthNames[int(u.Month())-1], u.Year())
}

// IsoDay renders "2026-08-23" — the first ten characters, exactly as the JS slice does, so a
// value that is not a date comes out the same way on both sides rather than being reformatted.
func IsoDay(date string) string {
	if date == "" {
		return "—"
	}
	if len(date) < 10 {
		return date
	}
	return date[:10]
}

// FormatInt groups thousands with commas, matching toLocaleString('en').
func FormatInt(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := fmt.Sprintf("%d", n)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// FormatBytes renders a byte count for humans: 947 B, 12.4 KB, 3.1 MB (binary units, one
// decimal).
//
// Deliberately not locale-aware: this string is baked into static HTML, so it must not vary
// with the build machine's locale the way FormatInt may.
func FormatBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	if n < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
}

var spaceRe = regexp.MustCompile(`\s+`)

// Initials renders the avatar fallback: "Kieran Wood" → "KW".
func Initials(name string) string {
	parts := []string{}
	for _, p := range spaceRe.Split(strings.TrimSpace(name), -1) {
		if p != "" {
			parts = append(parts, p)
		}
	}
	s := ""
	if len(parts) > 0 {
		s += firstRune(parts[0])
	}
	if len(parts) > 1 {
		s += firstRune(parts[len(parts)-1])
	}
	if s == "" {
		s = truncateRunes(name, 2)
	}
	return strings.ToUpper(s)
}

// chooseALicenseSlugs are the SPDX ids choosealicense.com publishes a page for, keyed by their
// URL slug.
//
// The slug is the SPDX id lowercased AFTER dropping the GNU disambiguation suffix — our own
// detector emits GPL-3.0-only / AGPL-3.0-only / LGPL-2.1-only, while choosealicense serves
// /licenses/gpl-3.0/. Lowercasing alone would link every GNU license to a 404.
var chooseALicenseSlugs = map[string]bool{
	"0bsd": true, "afl-3.0": true, "agpl-3.0": true, "apache-2.0": true, "artistic-2.0": true,
	"bsd-2-clause": true, "bsd-3-clause": true, "bsd-3-clause-clear": true, "bsd-4-clause": true,
	"bsl-1.0": true, "cecill-2.1": true, "ecl-2.0": true, "epl-1.0": true, "epl-2.0": true,
	"eupl-1.1": true, "eupl-1.2": true, "gfdl-1.3": true, "gpl-2.0": true, "gpl-3.0": true,
	"isc": true, "lgpl-2.1": true, "lgpl-3.0": true, "lppl-1.3c": true, "mit": true, "mit-0": true,
	"mpl-2.0": true, "ms-pl": true, "ms-rl": true, "mulanpsl-2.0": true, "ncsa": true,
	"odbl-1.0": true, "ofl-1.1": true, "osl-3.0": true, "postgresql": true, "unlicense": true,
	"upl-1.0": true, "vim": true, "wtfpl": true, "zlib": true,
}

var (
	gnuSuffixRe = regexp.MustCompile(`-(only|or-later)$`)
	ccRe        = regexp.MustCompile(`^cc-(by(?:-nc)?(?:-sa|-nd)?)-(\d+\.\d+)$`)
)

// LicenseURL is the canonical human-readable page for a license, or "" when we do not
// recognise it — the caller then renders plain text rather than guessing a URL.
func LicenseURL(spdx string) string {
	if spdx == "" {
		return ""
	}
	// GPL-3.0-only and GPL-3.0-or-later describe the same license document.
	key := gnuSuffixRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(spdx)), "")
	if key == "" {
		return ""
	}
	if key == "cc0-1.0" {
		return "https://creativecommons.org/publicdomain/zero/1.0/"
	}
	if m := ccRe.FindStringSubmatch(key); m != nil {
		return "https://creativecommons.org/licenses/" + m[1] + "/" + m[2] + "/"
	}
	if chooseALicenseSlugs[key] {
		return "https://choosealicense.com/licenses/" + key + "/"
	}
	return ""
}

var schemeStripRe = regexp.MustCompile(`(?i)^[a-z]+://`)

// PrettyURL displays a URL without protocol or trailing slash.
func PrettyURL(url string) string {
	return strings.TrimSuffix(schemeStripRe.ReplaceAllString(url, ""), "/")
}

/* ---- helpers ------------------------------------------------------------- */

func parseDate(date string) (time.Time, bool) {
	if date == "" {
		return time.Time{}, false
	}
	d, err := time.Parse(time.RFC3339, date)
	if err != nil {
		return time.Time{}, false
	}
	return d, true
}

// round matches JavaScript's Math.round: halfway cases go towards +Infinity, not away from
// zero the way Go's math.Round does. Only matters for negative halves, which ages should never
// be — but "should never be" is how the two implementations drift.
func round(f float64) float64 {
	i := float64(int64(f))
	frac := f - i
	if f >= 0 {
		if frac >= 0.5 {
			return i + 1
		}
		return i
	}
	if frac < -0.5 {
		return i - 1
	}
	return i
}

// math0 clamps to zero, matching Math.max(0, …) — a clock skew must not produce "in 3 hours".
func math0(f float64) float64 {
	if f < 0 {
		return 0
	}
	return f
}

func firstRune(s string) string {
	for _, r := range s {
		return string(r)
	}
	return ""
}

func truncateRunes(s string, n int) string {
	out := []rune(s)
	if len(out) > n {
		out = out[:n]
	}
	return string(out)
}
