package ingest

import (
	"regexp"
	"sort"
	"strings"

	"frznforge/internal/model"
)

// isLicenseFilename reports whether a root-level file name holds a license, case-insensitively.
func isLicenseFilename(name string) bool {
	n := strings.ToLower(name)
	switch {
	case n == "license", strings.HasPrefix(n, "license."), strings.HasPrefix(n, "license-"),
		n == "licence", strings.HasPrefix(n, "licence."), strings.HasPrefix(n, "licence-"),
		n == "copying", strings.HasPrefix(n, "copying."),
		n == "unlicense", n == "unlicense.txt":
		return true
	}
	return false
}

// findLicenseEntry picks the best license file among root entries: a plain LICENSE first,
// then the other spellings, ties broken by name so the choice never depends on tree order.
func findLicenseEntry(rootEntries []RawTreeEntry) *RawTreeEntry {
	candidates := []RawTreeEntry{}
	for _, e := range rootEntries {
		if e.Type == "blob" && isLicenseFilename(e.Name) {
			candidates = append(candidates, e)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	rank := func(n string) int {
		l := strings.ToLower(n)
		switch {
		case l == "license" || l == "license.md" || l == "license.txt":
			return 0
		case strings.HasPrefix(l, "license"):
			return 1
		case strings.HasPrefix(l, "licence"):
			return 2
		case strings.HasPrefix(l, "copying"):
			return 3
		}
		return 4
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		ri, rj := rank(candidates[i].Name), rank(candidates[j].Name)
		if ri != rj {
			return ri < rj
		}
		return candidates[i].Name < candidates[j].Name
	})
	best := candidates[0]
	return &best
}

// spdxRule is one keyword test, in the order detectSpdx applies them. Order is the whole
// algorithm: "GNU lesser general public license version 3" must be tested before the plainer
// "GNU general public license version 3" pattern, which would otherwise claim it.
type spdxRule struct {
	re   *regexp.Regexp
	spdx string
}

var spdxRules = []spdxRule{
	{regexp.MustCompile(`\bzero-clause bsd\b|\b0bsd\b|\bbsd zero clause\b`), "0BSD"},
	{regexp.MustCompile(`\bmit license\b|\bmit licence\b|permission is hereby granted, free of charge, to any person obtaining a copy`), "MIT"},
	{regexp.MustCompile(`\bisc license\b|\bisc licence\b|permission to use, copy, modify, and/or distribute this software for any purpose with or without fee`), "ISC"},
	{regexp.MustCompile(`apache license.*version 2\.0|apache-2\.0|apache license 2\.0`), "Apache-2.0"},
	{regexp.MustCompile(`mozilla public license.*(version 2\.0|v\. 2\.0|2\.0)`), "MPL-2.0"},
	{regexp.MustCompile(`gnu affero general public license.*version 3|agpl-3\.0|agplv3`), "AGPL-3.0-only"},
	{regexp.MustCompile(`gnu lesser general public license.*version 3|lgpl-3\.0|lgplv3`), "LGPL-3.0-only"},
	{regexp.MustCompile(`gnu lesser general public license.*version 2\.1|lgpl-2\.1|lgplv2\.1`), "LGPL-2.1-only"},
	{regexp.MustCompile(`gnu general public license.*version 3|gpl-3\.0|gplv3`), "GPL-3.0-only"},
	{regexp.MustCompile(`gnu general public license.*version 2|gpl-2\.0|gplv2`), "GPL-2.0-only"},
	{regexp.MustCompile(`\bthe unlicense\b|this is free and unencumbered software released into the public domain`), "Unlicense"},
	{regexp.MustCompile(`cc0 1\.0|cc0-1\.0|creative commons zero|creative commons legal code cc0`), "CC0-1.0"},
}

var (
	bsdBodyRe    = regexp.MustCompile(`redistribution and use in source and binary forms`)
	bsd3ClauseRe = regexp.MustCompile(`neither the name of .* nor the names of its contributors|names of its contributors may be used to endorse`)
	bsd3NameRe   = regexp.MustCompile(`bsd 3-clause|bsd-3-clause|3-clause bsd`)
	bsd2NameRe   = regexp.MustCompile(`bsd 2-clause|bsd-2-clause|2-clause bsd|simplified bsd`)
)

// detectSpdx guesses an SPDX id from the first 40 lines of a license text, or "" for none.
//
// Only the head is examined, and it is flattened to single-spaced lowercase first, so a
// license wrapped at 72 columns matches the same patterns as one on a single line.
func detectSpdx(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if len(lines) > 40 {
		lines = lines[:40]
	}
	t := jsCollapseWhitespace(strings.ToLower(strings.Join(lines, "\n")))
	for _, rule := range spdxRules {
		if rule.re.MatchString(t) {
			return rule.spdx
		}
	}
	if bsdBodyRe.MatchString(t) {
		if bsd3ClauseRe.MatchString(t) || bsd3NameRe.MatchString(t) {
			return "BSD-3-Clause"
		}
		return "BSD-2-Clause"
	}
	if bsd3NameRe.MatchString(t) {
		return "BSD-3-Clause"
	}
	if bsd2NameRe.MatchString(t) {
		return "BSD-2-Clause"
	}
	return ""
}

// jsCollapseWhitespace is `replace(/\s+/g, ' ')` with JavaScript's notion of whitespace,
// which is wider than Go's `\s` — see jsTrim.
func jsCollapseWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inRun := false
	for _, r := range s {
		if isJSWhitespace(r) {
			if !inRun {
				b.WriteByte(' ')
				inRun = true
			}
			continue
		}
		inRun = false
		b.WriteRune(r)
	}
	return b.String()
}

// detectedLicense is a license file found in the tree, with whatever SPDX id it looked like.
type detectedLicense struct {
	File string
	Spdx string // "" when unrecognised
}

// resolveLicense builds the artifact record: a configured SPDX id wins, but the detected file
// is still recorded so the page can link to the actual text.
func resolveLicense(detected *detectedLicense, override string) *model.License {
	if override != "" {
		var file *string
		if detected != nil {
			f := detected.File
			file = &f
		}
		spdx := override
		return &model.License{Spdx: &spdx, File: file, Source: "config"}
	}
	if detected == nil {
		return nil
	}
	var spdx *string
	if detected.Spdx != "" {
		s := detected.Spdx
		spdx = &s
	}
	file := detected.File
	return &model.License{Spdx: spdx, File: &file, Source: "file"}
}
