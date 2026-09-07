package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Serialize writes the artifact exactly as serializeForgeData does in
// src/lib/ingest/index.ts: `JSON.stringify(data, null, 2)` followed by a newline.
//
// Three things here are load-bearing, and each has a test that fails without it:
//
//   - SetEscapeHTML(false). Go's default marshaller rewrites <, > and & into six-character
//     unicode escapes; JSON.stringify leaves them alone. The real artifact contains raw `<`
//     (any README with HTML in it does), so the default would diverge on essentially every
//     repository.
//   - SetIndent("", "  "). Two spaces, matching JSON.stringify's third argument.
//   - No trailing newline of our own. Encoder.Encode already appends one, which is the newline
//     serializeForgeData adds explicitly. Adding a second would double it.
//
// json.MarshalIndent cannot express the first of those, which is why this uses an Encoder.
func Serialize(data ForgeData) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(data); err != nil {
		return nil, fmt.Errorf("serialize artifact: %w", err)
	}
	return buf.Bytes(), nil
}

// Parse decodes an artifact and validates it. A parse error names the offending field.
func Parse(raw []byte) (ForgeData, error) {
	var data ForgeData
	dec := json.NewDecoder(bytes.NewReader(raw))
	// Unknown fields are a schema mismatch, not something to shrug at: an artifact written by
	// a NEWER frznforge would otherwise round-trip through here losing whatever it added.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&data); err != nil {
		return ForgeData{}, fmt.Errorf("parse artifact: %w", err)
	}
	if err := Validate(&data); err != nil {
		return ForgeData{}, err
	}
	return data, nil
}

var (
	shaRe     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	isoDateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`)
	monthRe   = regexp.MustCompile(`^\d{4}-\d{2}$`)
	slugRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
)

// Validate checks the invariants the zod schema enforced on the TypeScript side.
//
// It is deliberately not exhaustive about *shape* — encoding/json already rejected anything
// that is not the right type — but it does check the string formats that the site relies on
// silently: a sha that is not 40 hex chars, a date that is not the artifact's ISO profile, a
// slug that would not survive a URL. Those are the ones where a bad value produces a broken
// page rather than an error.
func Validate(d *ForgeData) error {
	if d.SchemaVersion != SchemaVersion {
		return fmt.Errorf(
			"artifact is schema version %d, this build reads version %d — re-run `frznforge build` to regenerate it",
			d.SchemaVersion, SchemaVersion)
	}
	var problems []string
	bad := func(format string, args ...any) {
		if len(problems) < 20 {
			problems = append(problems, fmt.Sprintf(format, args...))
		}
	}

	checkSha := func(where, v string) {
		if !shaRe.MatchString(v) {
			bad("%s: %q is not a 40-character lowercase hex sha", where, v)
		}
	}
	checkDate := func(where, v string) {
		if !isoDateRe.MatchString(v) {
			bad("%s: %q is not an ISO-8601 UTC timestamp (YYYY-MM-DDTHH:MM:SSZ)", where, v)
		}
	}

	for i := range d.Repos {
		r := &d.Repos[i]
		at := "repos[" + r.Slug + "]"
		if !slugRe.MatchString(r.Slug) {
			bad("%s: slug is not URL-safe (lowercase, digits, dashes)", at)
		}
		if r.ReleaseMode != "tags" && r.ReleaseMode != "provider" {
			bad("%s.releaseMode: %q is not \"tags\" or \"provider\"", at, r.ReleaseMode)
		}
		if r.Description != nil && len([]rune(*r.Description)) > 300 {
			bad("%s.description: %d characters, over the 300 limit", at, len([]rune(*r.Description)))
		}
		for sha, c := range r.Commits {
			checkSha(at+".commits key", sha)
			if c.Sha != sha {
				bad("%s.commits[%s]: keyed by a different sha than it carries (%s)", at, sha, c.Sha)
			}
			checkDate(at+".commits["+sha+"].authorDate", c.AuthorDate)
			checkDate(at+".commits["+sha+"].commitDate", c.CommitDate)
		}
		for sha := range r.ExtraCommits {
			checkSha(at+".extraCommits key", sha)
			if _, clash := r.Commits[sha]; clash {
				bad("%s.extraCommits[%s]: also present in commits; the two maps must be disjoint", at, sha)
			}
		}
		for _, b := range r.Branches {
			checkSha(at+".branches["+b.Name+"].head", b.Head)
			checkDate(at+".branches["+b.Name+"].lastCommitDate", b.LastCommitDate)
		}
		for _, t := range r.GitTags {
			checkSha(at+".gitTags["+t.Name+"].target", t.Target)
			checkDate(at+".gitTags["+t.Name+"].date", t.Date)
		}
		for _, e := range r.Tree {
			checkSha(at+".tree["+e.Path+"].sha", e.Sha)
			switch e.Type {
			case "blob", "tree", "commit", "symlink":
			default:
				bad("%s.tree[%s]: type %q is not blob/tree/commit/symlink", at, e.Path, e.Type)
			}
		}
		if r.Insights != nil {
			for _, p := range r.Insights.Commits {
				if !monthRe.MatchString(p.Month) {
					bad("%s.insights.commits: %q is not a YYYY-MM month", at, p.Month)
				}
			}
			for _, p := range r.Insights.CodeSize {
				if !monthRe.MatchString(p.Month) {
					bad("%s.insights.codeSize: %q is not a YYYY-MM month", at, p.Month)
				}
			}
			if int(r.Insights.SampleCount) != len(r.Insights.CodeSize) {
				bad("%s.insights: sampleCount %d but %d codeSize points", at, r.Insights.SampleCount, len(r.Insights.CodeSize))
			}
		}
		for _, w := range r.Warnings {
			if !WarningCodes[w.Code] {
				bad("%s.warnings: unknown code %q", at, w.Code)
			}
		}
	}

	for _, n := range d.Notes {
		if !slugRe.MatchString(n.Slug) {
			bad("notes[%s]: slug is not URL-safe", n.Slug)
		}
		if n.Kind != "file" && n.Kind != "folder" {
			bad("notes[%s].kind: %q is not \"file\" or \"folder\"", n.Slug, n.Kind)
		}
	}
	for _, o := range d.Organizations {
		if !slugRe.MatchString(o.Slug) {
			bad("organizations[%s]: slug is not URL-safe", o.Slug)
		}
	}
	for _, w := range d.Warnings {
		if !WarningCodes[w.Code] {
			bad("warnings: unknown code %q", w.Code)
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("artifact failed validation:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}
