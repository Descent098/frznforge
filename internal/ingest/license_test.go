package ingest

import (
	"strings"
	"testing"
)

func TestIsLicenseFilename(t *testing.T) {
	for name, want := range map[string]bool{
		"LICENSE":        true,
		"license":        true,
		"LICENSE.md":     true,
		"LICENSE.txt":    true,
		"LICENSE-MIT":    true,
		"licence":        true,
		"LICENCE.md":     true,
		"licence-apache": true,
		"COPYING":        true,
		"copying.txt":    true,
		"UNLICENSE":      true,
		"unlicense.txt":  true,
		// A prefix match needs the separator: only "license." and "license-" continue the name.
		"licenses":      false,
		"license_old":   false,
		"copying-extra": false,
		"unlicense.md":  false,
		"mylicense":     false,
		"readme.md":     false,
		"":              false,
	} {
		if got := isLicenseFilename(name); got != want {
			t.Errorf("isLicenseFilename(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestFindLicenseEntry(t *testing.T) {
	blob := func(name string) RawTreeEntry {
		return RawTreeEntry{Type: "blob", Name: name, Path: name, Sha: "sha-" + name}
	}
	cases := []struct {
		name    string
		entries []RawTreeEntry
		want    string // "" means nil
	}{
		{"none", []RawTreeEntry{blob("README.md"), blob("main.go")}, ""},
		// A plain LICENSE outranks every qualified spelling, whatever order git listed them in.
		{"plain wins", []RawTreeEntry{blob("LICENSE-MIT"), blob("COPYING"), blob("LICENSE")}, "LICENSE"},
		{"license.md is still plain", []RawTreeEntry{blob("LICENSE-MIT"), blob("LICENSE.md")}, "LICENSE.md"},
		// Same rank: the name breaks the tie, in code-point order.
		{"rank tie", []RawTreeEntry{blob("LICENSE-MIT"), blob("LICENSE-APACHE")}, "LICENSE-APACHE"},
		{"licence beats copying", []RawTreeEntry{blob("COPYING"), blob("LICENCE.md")}, "LICENCE.md"},
		// A directory named LICENSE is not a license file.
		{"blobs only", []RawTreeEntry{{Type: "tree", Name: "LICENSE", Path: "LICENSE"}}, ""},
	}
	for _, c := range cases {
		got := findLicenseEntry(c.entries)
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

func TestDetectSpdx(t *testing.T) {
	for text, want := range map[string]string{
		mitLicense: "MIT",
		"Permission is hereby granted, free of charge, to any person obtaining a copy\nof this software\n": "MIT",
		"ISC License\n\nCopyright (c) 2024\n":                                                                "ISC",
		"                Apache License\n         Version 2.0, January 2004\n":                               "Apache-2.0",
		"Mozilla Public License Version 2.0\n":                                                               "MPL-2.0",
		"GNU AFFERO GENERAL PUBLIC LICENSE\nVersion 3, 19 November 2007\n":                                   "AGPL-3.0-only",
		"GNU LESSER GENERAL PUBLIC LICENSE\nVersion 3, 29 June 2007\n":                                       "LGPL-3.0-only",
		"GNU LESSER GENERAL PUBLIC LICENSE\nVersion 2.1, February 1999\n":                                    "LGPL-2.1-only",
		"GNU GENERAL PUBLIC LICENSE\nVersion 3, 29 June 2007\n":                                              "GPL-3.0-only",
		"GNU GENERAL PUBLIC LICENSE\nVersion 2, June 1991\n":                                                 "GPL-2.0-only",
		"This is free and unencumbered software released into the public domain.\n":                          "Unlicense",
		"Creative Commons Legal Code\n\nCC0 1.0 Universal\n":                                                 "CC0-1.0",
		"Redistribution and use in source and binary forms, with or without\nmodification, are permitted.\n": "BSD-2-Clause",
		"Redistribution and use in source and binary forms are permitted provided that\n" +
			"neither the name of the holder nor the names of its contributors may be used\n": "BSD-3-Clause",
		"nothing recognisable here\n": "",
		"":                            "",
	} {
		if got := detectSpdx(text); got != want {
			t.Errorf("detectSpdx(%.48q) = %q, want %q", text, got, want)
		}
	}
}

// The ladder is ordered, and more than one rule only gives the right answer because of it.
func TestDetectSpdxLadderOrder(t *testing.T) {
	// 0BSD's own text opens with ISC's distinctive sentence; 0BSD is tested first.
	zero := "Zero-Clause BSD\n\nPermission to use, copy, modify, and/or distribute this software " +
		"for any purpose with or without fee is hereby granted.\n"
	if got := detectSpdx(zero); got != "0BSD" {
		t.Errorf("0BSD text detected as %q", got)
	}
	// The affero and lesser spellings both contain the plainer names; their rules come first.
	if got := detectSpdx("GNU Affero General Public License\nVersion 3\n"); got != "AGPL-3.0-only" {
		t.Errorf("AGPL text detected as %q", got)
	}
	if got := detectSpdx("GNU Lesser General Public License\nVersion 3\n"); got != "LGPL-3.0-only" {
		t.Errorf("LGPL text detected as %q", got)
	}
	// A BSD body naming its contributors clause is 3-clause; the same body without it is 2.
	body := "Redistribution and use in source and binary forms are permitted.\n"
	if got := detectSpdx(body); got != "BSD-2-Clause" {
		t.Errorf("plain BSD body detected as %q", got)
	}
	if got := detectSpdx(body + "The names of its contributors may be used to endorse products.\n"); got != "BSD-3-Clause" {
		t.Errorf("BSD body with a contributors clause detected as %q", got)
	}
}

// Only the first 40 LINES are examined — a license appended below a long preamble is not
// detected, and that boundary has to fall in the same place as the TypeScript's slice(0, 40).
func TestDetectSpdxReadsOnlyTheHead(t *testing.T) {
	filler := strings.Repeat("x\n", 39) // lines 0..38
	if got := detectSpdx(filler + "MIT License\n"); got != "MIT" {
		t.Errorf("keyword on line 40 = %q, want MIT", got)
	}
	if got := detectSpdx(filler + "x\nMIT License\n"); got != "" {
		t.Errorf("keyword on line 41 = %q, want no detection", got)
	}
}

// The head is flattened with JavaScript's notion of whitespace, which is not Go's. A license
// pasted out of a word processor separates its words with U+00A0, and JavaScript also counts
// U+FEFF — get the set wrong and the Go build alone stops recognising the file.
func TestDetectSpdxCollapsesJavaScriptWhitespace(t *testing.T) {
	for _, sep := range []string{
		" ", "   ", "\t\t", "\r", "\f", "\v",
		"\u00a0", // NBSP: JavaScript whitespace, not Go's
		"\u2009", // thin space
		"\u3000", // ideographic space
		"\ufeff", // BOM: JavaScript whitespace, not Go's (a literal one is illegal here)
	} {
		if got := detectSpdx("MIT" + sep + "License\n"); got != "MIT" {
			t.Errorf("separator %q: detectSpdx = %q, want MIT", sep, got)
		}
	}
	// U+0085 NEL is the other direction: Go's strings.TrimSpace treats it as space and
	// JavaScript does not, so it must NOT collapse — the two words stay glued together.
	if got := detectSpdx("MIT\u0085License\n"); got != "" {
		t.Errorf("U+0085 collapsed like whitespace: detectSpdx = %q, want no detection", got)
	}
}

// CRLF is one line separator, not two: a Windows checkout must detect the same id, and its
// lines have to be counted the same way for the 40-line cap.
func TestDetectSpdxHandlesCRLF(t *testing.T) {
	if got := detectSpdx("MIT License\r\n\r\nCopyright (c) 2024\r\n"); got != "MIT" {
		t.Errorf("CRLF license = %q, want MIT", got)
	}
	if got := detectSpdx(strings.Repeat("x\r\n", 40) + "MIT License\r\n"); got != "" {
		t.Errorf("CRLF keyword past line 40 = %q, want no detection", got)
	}
	// A lone CR is not a line break, but it IS whitespace, so it collapses to a space.
	if got := detectSpdx("MIT\rLicense\n"); got != "MIT" {
		t.Errorf("lone CR = %q, want MIT", got)
	}
}

func TestResolveLicense(t *testing.T) {
	if resolveLicense(nil, "") != nil {
		t.Error("no file and no override should be no license")
	}
	fromConfig := resolveLicense(nil, "MIT")
	if fromConfig.Source != "config" || fromConfig.File != nil || *fromConfig.Spdx != "MIT" {
		t.Errorf("config license = %+v", fromConfig)
	}
	// A configured id wins, but the detected file is still recorded so the page can link it.
	both := resolveLicense(&detectedLicense{File: "LICENSE", Spdx: "Apache-2.0"}, "MIT")
	if *both.Spdx != "MIT" || both.File == nil || *both.File != "LICENSE" || both.Source != "config" {
		t.Errorf("layered license = %+v", both)
	}
	// A file we could not identify is still a license: the row links the text, the id is null.
	unknown := resolveLicense(&detectedLicense{File: "COPYING", Spdx: ""}, "")
	if unknown.Spdx != nil || unknown.Source != "file" || *unknown.File != "COPYING" {
		t.Errorf("unrecognised license text = %+v", unknown)
	}
	detected := resolveLicense(&detectedLicense{File: "LICENSE.md", Spdx: "MIT"}, "")
	if *detected.Spdx != "MIT" || *detected.File != "LICENSE.md" || detected.Source != "file" {
		t.Errorf("detected license = %+v", detected)
	}
}
