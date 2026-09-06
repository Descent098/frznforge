package ingest

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"frznforge/internal/model"
)

// commitFieldSep separates the fields of one commit record. A commit message can contain it
// (nothing stops an author typing a unit separator), which is why the body — the last field —
// is rejoined from everything past its index rather than read as a single part.
const commitFieldSep = "\x1f"

// commitFormat is the pretty format the parser below unpacks, in order. The separator is
// spelled as git's `%x1f` escape rather than carried literally, for the same reason
// fieldSepFormat is: control bytes in an argv entry are a platform hazard.
var commitFormat = strings.Join([]string{
	"%H", "%P", "%an", "%ae", "%aI", "%cn", "%ce", "%cI", "%s", "%b",
}, "%x1f")

const commitFormatFields = 10

// ParseCommitRecord parses one `-z` record produced by commitFormat. ok is false for a record
// that is not a commit (a stray empty chunk), which the caller skips.
func ParseCommitRecord(rec string) (c model.Commit, ok bool, err error) {
	parts := strings.Split(rec, commitFieldSep)
	if len(parts) < commitFormatFields {
		return model.Commit{}, false, nil
	}
	authorDate, err := ToISOUTC(parts[4])
	if err != nil {
		return model.Commit{}, false, fmt.Errorf("commit %s author date: %w", jsTrim(parts[0]), err)
	}
	commitDate, err := ToISOUTC(parts[7])
	if err != nil {
		return model.Commit{}, false, fmt.Errorf("commit %s commit date: %w", jsTrim(parts[0]), err)
	}
	parents := []string{}
	if p := jsTrim(parts[1]); p != "" {
		parents = splitJSWhitespace(p)
	}
	// One trailing line break only, exactly `replace(/\r?\n$/, '')`: a lone trailing CR is
	// part of the subject and must survive.
	subject := parts[8]
	if strings.HasSuffix(subject, "\n") {
		subject = strings.TrimSuffix(strings.TrimSuffix(subject, "\n"), "\r")
	}
	return model.Commit{
		Sha:        jsTrim(parts[0]),
		Parents:    parents,
		Author:     model.Person{Name: parts[2], Email: parts[3]},
		AuthorDate: authorDate,
		Committer:  model.Person{Name: parts[5], Email: parts[6]},
		CommitDate: commitDate,
		// %b is last; a message containing the separator would be split further — rejoin.
		Subject: subject,
		Body:    jsTrim(strings.Join(parts[commitFormatFields-1:], commitFieldSep)),
		Files:   []model.CommitFileChange{},
		Stats:   model.CommitStats{},
	}, true, nil
}

// StatsFor sums a commit's file changes. Binary files carry null counts and so add to
// filesChanged only.
func StatsFor(files []model.CommitFileChange) model.CommitStats {
	var additions, deletions int64
	for _, f := range files {
		if f.Additions != nil {
			additions += *f.Additions
		}
		if f.Deletions != nil {
			deletions += *f.Deletions
		}
	}
	return model.CommitStats{FilesChanged: int64(len(files)), Additions: additions, Deletions: deletions}
}

var shaLineRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// numstatRe matches one numstat record. (?s) so a path containing a newline still matches to
// the end, matching the TypeScript's `s` flag.
var numstatRe = regexp.MustCompile(`(?s)^(\d+|-)\t(\d+|-)\t(.*)$`)

// LoadNumstats reads per-commit file changes from `git log --numstat -z`.
//
// The diff is against the first parent, and root commits count every file as added. With
// plain `git log` (no -m / --first-parent) merge commits emit no numstat records at all, so a
// merge maps to an empty list — deliberately, since a merge's "changes" are not its own.
// Binary files appear as `-`/`-` and become null counts.
func LoadNumstats(ctx context.Context, repo string, shas []string) (map[string][]model.CommitFileChange, error) {
	result := map[string][]model.CommitFileChange{}
	if len(shas) == 0 {
		return result, nil
	}
	r, err := GitRun(ctx, repo,
		[]string{"log", "--no-walk=unsorted", "--stdin", "-z", "--format=%x1e%H", "--numstat", "--"},
		GitOptions{Input: []byte(strings.Join(shas, "\n") + "\n")},
	)
	if err != nil {
		return nil, err
	}
	for _, chunk := range strings.Split(string(r.Stdout), recordSep) {
		if chunk == "" {
			continue
		}
		fields := strings.Split(chunk, fieldSep)
		sha := jsTrim(fields[0])
		if !shaLineRe.MatchString(sha) {
			continue
		}
		files := []model.CommitFileChange{}
		for i := 1; i < len(fields); i++ {
			rec := strings.TrimLeft(fields[i], "\n")
			m := numstatRe.FindStringSubmatch(rec)
			if m == nil {
				continue
			}
			path := m[3]
			if path == "" {
				// A rename with -z is `adds\tdels\t` followed by two more NUL fields:
				// the old path, then the new one. The new path is what the artifact records.
				if i+2 >= len(fields) || fields[i+2] == "" {
					i += 2
					continue
				}
				path = fields[i+2]
				i += 2
			}
			files = append(files, model.CommitFileChange{
				Path:      path,
				Additions: parseCount(m[1]),
				Deletions: parseCount(m[2]),
			})
		}
		result[sha] = files
	}
	return result, nil
}

// parseCount turns a numstat count into a value, or nil for the `-` a binary file reports.
func parseCount(v string) *int64 {
	if v == "-" {
		return nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return nil
	}
	return &n
}

// LoadCommits loads the given commits keyed by sha.
//
// Shas are de-duplicated and sorted before the request so the git invocation — and therefore
// anything derived from its order — is the same on every run.
func LoadCommits(ctx context.Context, repo string, shas []string) (map[string]model.Commit, error) {
	list := dedupeStrings(shas)
	sort.Strings(list)
	result := map[string]model.Commit{}
	if len(list) == 0 {
		return result, nil
	}
	r, err := GitRun(ctx, repo,
		[]string{"log", "--no-walk=unsorted", "--stdin", "-z", "--format=" + commitFormat, "--"},
		GitOptions{Input: []byte(strings.Join(list, "\n") + "\n")},
	)
	if err != nil {
		return nil, err
	}
	numstats, err := LoadNumstats(ctx, repo, list)
	if err != nil {
		return nil, err
	}

	bySha := map[string]model.Commit{}
	for _, rec := range strings.Split(string(r.Stdout), fieldSep) {
		trimmed := strings.TrimLeft(rec, "\n")
		if trimmed == "" {
			continue
		}
		c, ok, err := ParseCommitRecord(trimmed)
		if err != nil {
			return nil, err
		}
		if ok {
			bySha[c.Sha] = c
		}
	}
	for _, sha := range list {
		c, ok := bySha[sha]
		if !ok {
			continue // a sha that no longer resolves is skipped, not fatal
		}
		files, ok := numstats[sha]
		if !ok {
			files = []model.CommitFileChange{}
		}
		c.Files = files
		c.Stats = StatsFor(files)
		result[sha] = c
	}
	return result, nil
}

// shasOf returns a set's members as a slice. LoadCommits sorts what it is given, so the
// randomised map order this produces never reaches the artifact.
func shasOf(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	return out
}
