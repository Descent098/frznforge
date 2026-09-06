package render

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"frznforge/internal/config"
)

// TestFormatMatchesBrowser is the standing guard on the version's one genuine two-language
// pair: internal/render/format.go (build time, Go) and web/js/format.js (view time, browser).
//
// The listing server-renders a card and the browser rebuilds the same card from a template, so
// a disagreement between these two is visible as a card silently changing the moment a filter
// is touched. tests/fixtures/format-cases.json is generated FROM the JavaScript
// (`node tests/fixtures/gen-format-cases.mjs`), which makes the JavaScript the reference and
// this test the check.
//
// Neither side may gain a behaviour the other lacks without regenerating that fixture in the
// same change.
func TestFormatMatchesBrowser(t *testing.T) {
	type stringCase struct {
		In  string `json:"in"`
		Out string `json:"out"`
	}
	type numberCase struct {
		In  int64  `json:"in"`
		Out string `json:"out"`
	}
	var golden struct {
		Now               string            `json:"now"`
		Heat              config.HeatConfig `json:"heat"`
		RelativeTime      []stringCase      `json:"relativeTime"`
		RelativeTimeShort []stringCase      `json:"relativeTimeShort"`
		HeatFor           []stringCase      `json:"heatFor"`
		FormatInt         []numberCase      `json:"formatInt"`
		FormatBytes       []numberCase      `json:"formatBytes"`
		Initials          []stringCase      `json:"initials"`
		LicenseURL        []stringCase      `json:"licenseUrl"`
		PrettyURL         []stringCase      `json:"prettyUrl"`
		MonthYear         []stringCase      `json:"monthYear"`
		IsoDay            []stringCase      `json:"isoDay"`
	}

	raw, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "format-cases.json"))
	if err != nil {
		t.Fatalf("read shared golden: %v (regenerate with `node tests/fixtures/gen-format-cases.mjs`)", err)
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("parse shared golden: %v", err)
	}
	now, err := time.Parse(time.RFC3339, golden.Now)
	if err != nil {
		t.Fatalf("golden `now` is not a date: %v", err)
	}

	check := func(name string, cases []stringCase, fn func(string) string) {
		t.Run(name, func(t *testing.T) {
			for _, c := range cases {
				if got := fn(c.In); got != c.Out {
					t.Errorf("%s(%q) = %q, browser says %q", name, c.In, got, c.Out)
				}
			}
		})
	}
	checkNum := func(name string, cases []numberCase, fn func(int64) string) {
		t.Run(name, func(t *testing.T) {
			for _, c := range cases {
				if got := fn(c.In); got != c.Out {
					t.Errorf("%s(%d) = %q, browser says %q", name, c.In, got, c.Out)
				}
			}
		})
	}

	check("relativeTime", golden.RelativeTime, func(s string) string { return RelativeTime(s, now) })
	check("relativeTimeShort", golden.RelativeTimeShort, func(s string) string { return RelativeTimeShort(s, now) })
	check("heatFor", golden.HeatFor, func(s string) string { return HeatFor(s, now, golden.Heat) })
	check("initials", golden.Initials, Initials)
	check("licenseUrl", golden.LicenseURL, LicenseURL)
	check("prettyUrl", golden.PrettyURL, PrettyURL)
	check("monthYear", golden.MonthYear, MonthYear)
	check("isoDay", golden.IsoDay, IsoDay)
	checkNum("formatInt", golden.FormatInt, FormatInt)
	checkNum("formatBytes", golden.FormatBytes, FormatBytes)

	if len(golden.RelativeTime) == 0 {
		t.Fatal("golden is empty — a vacuous pass is worse than no test")
	}
}
