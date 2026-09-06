package ingest

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"frznforge/internal/model"
)

// BranchRef is a local branch and the date of its head commit, as for-each-ref reports them.
type BranchRef struct {
	Name string
	Head string
	// HeadDate is the head commit's committer date, normalised to ISO UTC — which makes a
	// plain string compare a date compare.
	HeadDate string
}

// recordSep and fieldSep are what the for-each-ref formats below separate with. A refname
// cannot contain either, and neither can an object id, so the only field that can carry them
// is an annotated tag's message — which is why the tag parser rejoins its tail.
const (
	recordSep = "\x1e"
	fieldSep  = "\x00"
	// The format ARGUMENT spells those two bytes as git's own escapes rather than carrying
	// them literally. That is not cosmetic: an argv entry containing a real NUL cannot be
	// passed to a process on Windows at all (Go rejects it outright, and a command line is
	// NUL-terminated), so a literal separator here fails on one platform and works on the
	// other.
	fieldSepFormat  = "%00"
	recordSepFormat = "%1e"
)

// ListBranchRefs lists every local branch that has at least one commit, sorted by name.
func ListBranchRefs(ctx context.Context, repo string) ([]BranchRef, error) {
	out, err := GitOutput(ctx, repo,
		"for-each-ref",
		"--format=%(refname)%00%(objectname)%00%(committerdate:iso-strict)%1e",
		"refs/heads",
	)
	if err != nil {
		return nil, err
	}
	var refs []BranchRef
	for _, rec := range strings.Split(out, recordSep) {
		// for-each-ref terminates each ref's output with a newline, so every record after the
		// first arrives with that newline still attached.
		r := strings.TrimLeftFunc(rec, isJSWhitespace)
		if r == "" {
			continue
		}
		parts := strings.Split(r, fieldSep)
		if len(parts) < 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
			continue
		}
		date, err := ToISOUTC(parts[2])
		if err != nil {
			return nil, fmt.Errorf("branch %s: %w", parts[0], err)
		}
		refs = append(refs, BranchRef{
			Name:     strings.TrimPrefix(parts[0], "refs/heads/"),
			Head:     parts[1],
			HeadDate: date,
		})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Name < refs[j].Name })
	return refs, nil
}

// DefaultBranchResult is the detected default branch plus any fallback warning.
type DefaultBranchResult struct {
	// Name is "" when no branch has any commit — i.e. the repository is empty.
	Name     string
	Warnings []model.Warning
}

// DetectDefaultBranch picks the default branch: HEAD's branch when it has commits, otherwise
// `main`, `master`, then the branch with the most recent commit — the fallback raising a
// `default-branch-fallback` warning so the choice is never silent.
func DetectDefaultBranch(ctx context.Context, repo string, branches []BranchRef) (DefaultBranchResult, error) {
	res := DefaultBranchResult{Warnings: []model.Warning{}}

	headBranch := ""
	if out, ok, err := GitMaybe(ctx, repo, "symbolic-ref", "--quiet", "HEAD"); err != nil {
		return res, err
	} else if ok {
		ref := strings.TrimSpace(out)
		if strings.HasPrefix(ref, "refs/heads/") {
			headBranch = strings.TrimPrefix(ref, "refs/heads/")
		}
	}

	if headBranch != "" {
		for _, b := range branches {
			if b.Name == headBranch {
				res.Name = headBranch
				return res, nil
			}
		}
	}
	if len(branches) == 0 {
		return res, nil
	}

	pick := ""
	for _, want := range []string{"main", "master"} {
		for _, b := range branches {
			if b.Name == want {
				pick = b.Name
				break
			}
		}
		if pick != "" {
			break
		}
	}
	if pick == "" {
		byDate := append([]BranchRef(nil), branches...)
		sort.Slice(byDate, func(i, j int) bool {
			a, b := byDate[i], byDate[j]
			if a.HeadDate != b.HeadDate {
				return a.HeadDate > b.HeadDate
			}
			return a.Name < b.Name
		})
		pick = byDate[0].Name
	}

	why := "HEAD is detached or unreadable"
	if headBranch != "" {
		why = fmt.Sprintf("HEAD points at '%s' which has no commits", headBranch)
	}
	res.Name = pick
	res.Warnings = append(res.Warnings, model.Warning{
		Code:    "default-branch-fallback",
		Repo:    nil,
		Message: fmt.Sprintf("%s; using '%s' as the default branch", why, pick),
	})
	return res, nil
}

// BranchesResult is the per-branch commit lists plus the union of every sha they name.
type BranchesResult struct {
	Branches []model.Branch
	// Shas is the union of every sha listed in any branch, after capping. It is a set, so it
	// carries no order — LoadCommits sorts what it is given.
	Shas     map[string]struct{}
	Warnings []model.Warning
}

const dayMillis = 86_400_000

// LoadBranches builds each branch's commit list (topological, newest first), capped at
// maxCommits (nil = uncapped) and, with maxCommitAgeDays set, limited to commits from the
// last N days.
//
// The age cutoff is anchored to the newest branch-head commit date across refs, NEVER to the
// clock, so the same repo at the same commits emits the same lists on any machine on any day.
// `--since-as-filter` (git ≥ 2.37) is used rather than `--since`, which stops traversal at the
// first old commit and can drop in-window commits reachable only through clock-skewed older
// ones. Every branch keeps its head commit whatever the filter says — a Branch.head missing
// from its own commit list would blank the branches page.
func LoadBranches(ctx context.Context, repo string, refs []BranchRef, maxCommits, maxCommitAgeDays *int64) (BranchesResult, error) {
	res := BranchesResult{
		Branches: []model.Branch{},
		Shas:     map[string]struct{}{},
		Warnings: []model.Warning{},
	}
	capped, aged := false, false

	// Newest head date across all branches; HeadDate is ISO UTC, so string max is date max.
	cutoffISO := ""
	if maxCommitAgeDays != nil && len(refs) > 0 {
		anchor := refs[0].HeadDate
		for _, r := range refs {
			if r.HeadDate > anchor {
				anchor = r.HeadDate
			}
		}
		at, err := time.Parse("2006-01-02T15:04:05Z", anchor)
		if err != nil {
			return res, fmt.Errorf("anchor date %q: %w", anchor, err)
		}
		cutoff := at.Add(-time.Duration(*maxCommitAgeDays) * dayMillis * time.Millisecond)
		// Spelled the way Date#toISOString spells it, milliseconds included, so the argument
		// handed to git is character-for-character the TypeScript's.
		cutoffISO = cutoff.UTC().Format("2006-01-02T15:04:05.000Z")
	}

	for _, ref := range refs {
		args := []string{"rev-list", "--topo-order"}
		if maxCommits != nil {
			args = append(args, "--max-count="+strconv.FormatInt(*maxCommits+1, 10))
		}
		if cutoffISO != "" {
			args = append(args, "--since-as-filter="+cutoffISO)
		}
		args = append(args, ref.Head, "--")
		out, err := GitOutput(ctx, repo, args...)
		if err != nil {
			return res, err
		}
		commits := splitLines(out)

		if cutoffISO != "" {
			// The head commit is ALWAYS kept — not only when the whole branch aged out. With
			// committer-date skew, --since-as-filter can drop an old-dated head while keeping a
			// newer-dated ancestor.
			if !containsString(commits, ref.Head) {
				commits = append([]string{ref.Head}, commits...)
			}
			// Cheap drop detection: total history size versus what the filter kept. Skipped
			// once --max-count already truncated the walk — commits-capped explains that case.
			if maxCommits == nil || int64(len(commits)) <= *maxCommits {
				countOut, err := GitOutput(ctx, repo, "rev-list", "--count", ref.Head, "--")
				if err != nil {
					return res, err
				}
				if total, err := strconv.Atoi(strings.TrimSpace(countOut)); err == nil && len(commits) < total {
					aged = true
				}
			}
		}
		if maxCommits != nil && int64(len(commits)) > *maxCommits {
			commits = commits[:*maxCommits]
			capped = true
		}
		for _, s := range commits {
			res.Shas[s] = struct{}{}
		}
		res.Branches = append(res.Branches, model.Branch{
			Name:           ref.Name,
			Head:           ref.Head,
			Commits:        commits,
			LastCommitDate: ref.HeadDate,
		})
	}

	if capped {
		res.Warnings = append(res.Warnings, model.Warning{
			Code:    "commits-capped",
			Repo:    nil,
			Message: fmt.Sprintf("commit lists were capped at %d per branch (ingest.maxCommits)", *maxCommits),
		})
	}
	if aged {
		res.Warnings = append(res.Warnings, model.Warning{
			Code: "commits-aged-out",
			Repo: nil,
			Message: fmt.Sprintf(
				"commits older than %d days (measured from the newest commit) were left out (ingest.maxCommitAgeDays)",
				*maxCommitAgeDays),
		})
	}
	return res, nil
}

// tagFields is the for-each-ref format for tags, in the order the parser below unpacks them.
// `%(contents)` is last on purpose: it is the only field that can itself contain the
// separator, so everything from its index onward is rejoined.
var tagFields = []string{
	"%(objecttype)",
	"%(objectname)",
	"%(*objectname)",
	"%(*objecttype)",
	"%(refname)",
	"%(taggername)",
	"%(taggeremail)",
	"%(taggerdate:iso-strict)",
	"%(committerdate:iso-strict)",
	"%(*committerdate:iso-strict)",
	"%(contents)",
}

// LoadTags lists annotated and lightweight tags, sorted by name.
//
// A tag that peels to a tree or a blob is skipped rather than represented: the artifact's Tag
// carries a commit target, and there is no honest value to put there.
func LoadTags(ctx context.Context, repo string) ([]model.Tag, error) {
	format := "--format=" + strings.Join(tagFields, fieldSepFormat) + recordSepFormat
	out, err := GitOutput(ctx, repo, "for-each-ref", format, "refs/tags")
	if err != nil {
		return nil, err
	}
	tags := []model.Tag{}
	for _, rec := range strings.Split(out, recordSep) {
		r := strings.TrimLeftFunc(rec, isJSWhitespace)
		if r == "" {
			continue
		}
		parts := strings.Split(r, fieldSep)
		if len(parts) < len(tagFields) {
			continue
		}
		objType, objName := parts[0], parts[1]
		peeledName, peeledType := parts[2], parts[3]
		refname := parts[4]
		taggerName, taggerEmail, taggerDate := parts[5], parts[6], parts[7]
		commitDate, peeledCommitDate := parts[8], parts[9]
		contents := strings.Join(parts[len(tagFields)-1:], fieldSep)
		name := strings.TrimPrefix(refname, "refs/tags/")

		switch objType {
		case "tag":
			if peeledType != "commit" || peeledName == "" {
				continue // tag → tree/blob: not representable
			}
			var tagger *model.Person
			if taggerName != "" || taggerEmail != "" {
				tagger = &model.Person{Name: taggerName, Email: StripAngleBrackets(taggerEmail)}
			}
			raw := taggerDate
			if raw == "" {
				raw = peeledCommitDate
			}
			date, err := ToISOUTC(raw)
			if err != nil {
				return nil, fmt.Errorf("tag %s: %w", name, err)
			}
			message := jsTrim(contents)
			tags = append(tags, model.Tag{
				Name: name, Target: peeledName, Annotated: true,
				Message: &message, Tagger: tagger, Date: date,
			})
		case "commit":
			date, err := ToISOUTC(commitDate)
			if err != nil {
				return nil, fmt.Errorf("tag %s: %w", name, err)
			}
			tags = append(tags, model.Tag{
				Name: name, Target: objName, Annotated: false,
				Message: nil, Tagger: nil, Date: date,
			})
		}
		// lightweight tags pointing at trees or blobs are skipped
	}
	sort.Slice(tags, func(i, j int) bool { return tags[i].Name < tags[j].Name })
	return tags, nil
}

// splitLines splits newline-delimited git output, dropping empties and tolerating CRLF —
// which is what a repo committed on Windows with a CR in a ref listing would produce.
func splitLines(out string) []string {
	lines := []string{}
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSuffix(l, "\r")
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
