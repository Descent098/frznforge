package ingest

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"frznforge/internal/model"
)

// InsightsOptions mirrors the `ingest.insights` config block one for one.
type InsightsOptions struct {
	// Enabled false makes ComputeInsights return nothing at all.
	Enabled bool
	// Samples caps the monthly code-size checkpoints; the first and last month always survive.
	Samples int
	// MaxBytesPerSample is the byte budget for line counting at ONE checkpoint. Past it, that
	// point gets a null line count.
	MaxBytesPerSample int64
}

// DefaultInsightsOptions matches the `ingest.insights` defaults in the config schema.
var DefaultInsightsOptions = InsightsOptions{Enabled: true, Samples: 24, MaxBytesPerSample: 20 * 1024 * 1024}

// ComputeInsightsArgs is everything the insights pass needs from the scanner — plain data the
// scanner already holds, so no refs are re-read and no commit is loaded twice.
type ComputeInsightsArgs struct {
	// Commits is every commit in the artifact, keyed by sha: exactly Repo.commits.
	//
	// The two series read two different clocks on purpose: the commit/contributor buckets use
	// AuthorDate (who wrote code when), the code-size checkpoints use CommitDate (when that
	// tree landed on the branch). See monthlyCheckpoints.
	Commits map[string]model.Commit
	// BranchCommits is the default branch's shas, newest first, already truncated by
	// ingest.maxCommits. Empty for a repo with no default branch.
	BranchCommits []string
	// Head is the default branch head sha, "" for an empty repo.
	Head    string
	Options InsightsOptions
}

// ComputeInsightsResult is what the scanner splices into the Repo.
type ComputeInsightsResult struct {
	// Insights is nil for an empty repo, or when insights are switched off.
	Insights *model.RepoInsights
	// Warnings carry repo: null; scanRepo stamps the slug, as it does for every extractor.
	Warnings []model.Warning
}

/* ---- month arithmetic ---------------------------------------------------- */

// monthOf is the YYYY-MM bucket of an ISO UTC instant.
func monthOf(isoUTC string) string {
	if len(isoUTC) < 7 {
		return isoUTC
	}
	return isoUTC[:7]
}

// monthIndex counts months since year 0, so gaps can be filled by counting.
func monthIndex(month string) int {
	if len(month) < 7 {
		return 0
	}
	year, _ := strconv.Atoi(month[:4])
	m, _ := strconv.Atoi(month[5:7])
	return year*12 + (m - 1)
}

func monthFromIndex(index int) string {
	return fmt.Sprintf("%04d-%02d", index/12, index%12+1)
}

// countLines counts lines the way an editor does — and, crucially, the way the rest of the
// site does: a trailing newline closes the last line rather than opening a new one, and a file
// that ends without one still ends in a line.
//
// This is countLines() from src/lib/highlight.ts transposed to bytes. Counting raw newlines
// instead undercounts by exactly one for every file with no final newline, which made a blob
// page print "2 lines" while insights counted it as 1.
func countLines(buf []byte) int64 {
	if len(buf) == 0 {
		return 0
	}
	count := int64(bytes.Count(buf, []byte{'\n'}))
	if buf[len(buf)-1] == '\n' {
		return count
	}
	return count + 1
}

// pickEvenly thins items to at most max entries, evenly spaced, always keeping the first and
// the last. Indices are computed by rounding, so the choice depends only on the list length —
// two runs over the same history pick the same entries.
func pickEvenly[T any](items []T, max int) []T {
	if len(items) == 0 {
		return nil
	}
	if max >= len(items) {
		return append([]T(nil), items...)
	}
	// A cap of one cannot hold both ends; keep the newest, which is the checkpoint that
	// matches the repo's current tree.
	if max <= 1 {
		return []T{items[len(items)-1]}
	}
	picked := make([]T, 0, max)
	previous := -1
	for i := 0; i < max; i++ {
		index := int(math.Round(float64(i*(len(items)-1)) / float64(max-1)))
		if index == previous {
			continue // rounding collision; indices are non-decreasing
		}
		previous = index
		picked = append(picked, items[index])
	}
	return picked
}

/* ---- the two series ------------------------------------------------------ */

// bucketCommits counts commits and distinct authors per month over branchCommits, oldest
// month first.
//
// Months inside the span with no commits are emitted as zeros rather than omitted, so a chart
// drawn straight from this array shows a quiet period as quiet instead of closing the gap and
// implying steady activity.
func bucketCommits(commits map[string]model.Commit, branchCommits []string) []model.CommitPoint {
	type bucket struct {
		commits int64
		emails  map[string]struct{}
	}
	perMonth := map[string]*bucket{}
	for _, sha := range branchCommits {
		commit, ok := commits[sha]
		if !ok {
			continue
		}
		month := monthOf(commit.AuthorDate)
		b := perMonth[month]
		if b == nil {
			b = &bucket{emails: map[string]struct{}{}}
			perMonth[month] = b
		}
		b.commits++
		b.emails[strings.ToLower(jsTrim(commit.Author.Email))] = struct{}{}
	}
	if len(perMonth) == 0 {
		return []model.CommitPoint{}
	}
	first, last := 0, 0
	seen := false
	for month := range perMonth {
		i := monthIndex(month)
		if !seen {
			first, last, seen = i, i, true
			continue
		}
		if i < first {
			first = i
		}
		if i > last {
			last = i
		}
	}
	points := make([]model.CommitPoint, 0, last-first+1)
	for i := first; i <= last; i++ {
		month := monthFromIndex(i)
		p := model.CommitPoint{Month: month}
		if b := perMonth[month]; b != nil {
			p.Commits = b.commits
			p.Contributors = int64(len(b.emails))
		}
		points = append(points, p)
	}
	return points
}

// checkpoint is one monthly measurement: the commit whose tree gets measured, and its month.
type checkpoint struct {
	Month string
	Sha   string
}

// monthlyCheckpoints picks one checkpoint per month, oldest first, guaranteed to be in true
// history order.
//
// Two decisions, both because this series measures TREES, not authorship:
//
//   - Bucketed by CommitDate, not AuthorDate. A commit's tree is the state of the branch at
//     the point that commit was applied; AuthorDate says when the patch was written, which a
//     rebase leaves in the past while the tree it produces is brand new.
//   - Ranked by history position, not by date. Within a month the commit closest to the branch
//     head wins (branchCommits is newest first, so the lowest index), and a month whose pick
//     sits newer in history than a later month's pick is dropped rather than plotted out of
//     order.
func monthlyCheckpoints(commits map[string]model.Commit, branchCommits []string) []checkpoint {
	type pick struct {
		sha   string
		index int
	}
	best := map[string]pick{}
	headMonth := ""
	for index, sha := range branchCommits {
		commit, ok := commits[sha]
		if !ok {
			continue
		}
		month := monthOf(commit.CommitDate)
		if headMonth == "" {
			headMonth = month
		}
		if current, ok := best[month]; !ok || index < current.index {
			best[month] = pick{sha: sha, index: index}
		}
	}
	if headMonth == "" {
		return nil
	}
	headMonthIndex := monthIndex(headMonth)

	months := make([]string, 0, len(best))
	for month := range best {
		if monthIndex(month) <= headMonthIndex {
			months = append(months, month)
		}
	}
	sort.Slice(months, func(i, j int) bool { return monthIndex(months[i]) > monthIndex(months[j]) })

	kept := []checkpoint{}
	newestKeptIndex := -1
	for _, month := range months {
		p := best[month]
		// Walking back in time, each older month must sit further from the head. A pick closer
		// to the head than a LATER month's pick would draw the series backwards.
		if p.index <= newestKeptIndex {
			continue
		}
		newestKeptIndex = p.index
		kept = append(kept, checkpoint{Month: month, Sha: p.sha})
	}
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	return kept
}

// measurement is one checkpoint's tree size. Lines is nil when the budget ran out.
type measurement struct {
	Bytes int64
	Lines *int64
}

// measureCheckpoint measures the tracked code at one commit.
//
// One ls-tree lists the tree; candidates are its blobs and symlinks minus vendored locations.
// Content is read in a single cat-file --batch over the candidates in path order, stopping
// before the read would exceed maxBytes.
//
// When the budget cannot cover every candidate the checkpoint is approximate: the unread blobs
// contribute their ls-tree size without a binary check (so bytes can be inflated by binaries),
// and lines is null, because a partial newline count would be a lie.
func measureCheckpoint(ctx context.Context, repoPath, sha string, maxBytes int64) (measurement, error) {
	all, err := ListTree(ctx, repoPath, sha)
	if err != nil {
		return measurement{}, err
	}
	entries := make([]RawTreeEntry, 0, len(all))
	for _, e := range all {
		if (e.Type == "blob" || e.Type == "symlink") && !IsVendoredPath(e.Path) {
			entries = append(entries, e)
		}
	}

	// Split candidates into "we can afford to read this" and "we cannot", in path order. A sha
	// that appears at several paths is read once and costs its bytes once.
	readable := []RawTreeEntry{}
	unread := []RawTreeEntry{}
	chargedSet := map[string]struct{}{}
	charged := []string{}
	var budget int64
	for _, entry := range entries {
		size := sizeOf(entry)
		if _, done := chargedSet[entry.Sha]; done {
			readable = append(readable, entry)
			continue
		}
		if budget+size <= maxBytes {
			chargedSet[entry.Sha] = struct{}{}
			charged = append(charged, entry.Sha)
			budget += size
			readable = append(readable, entry)
		} else {
			unread = append(unread, entry)
		}
	}

	contents, err := ReadBlobs(ctx, repoPath, charged)
	if err != nil {
		return measurement{}, err
	}

	var totalBytes, totalLines int64
	for _, entry := range readable {
		content, ok := contents[entry.Sha]
		// A symlink's "blob" is its target path; an unreadable sha (a gitlink slipping through,
		// a corrupt object) contributes nothing rather than failing the scan.
		if !ok || LooksBinary(content) {
			continue
		}
		totalBytes += sizeOf(entry)
		totalLines += countLines(content)
	}
	if len(unread) == 0 {
		lines := totalLines
		return measurement{Bytes: totalBytes, Lines: &lines}, nil
	}
	for _, entry := range unread {
		totalBytes += sizeOf(entry)
	}
	return measurement{Bytes: totalBytes, Lines: nil}, nil
}

// ComputeInsights builds the insights series for one repository.
func ComputeInsights(ctx context.Context, repoPath string, args ComputeInsightsArgs) (ComputeInsightsResult, error) {
	empty := ComputeInsightsResult{Warnings: []model.Warning{}}
	if !args.Options.Enabled || len(args.BranchCommits) == 0 || args.Head == "" {
		return empty, nil
	}
	commitPoints := bucketCommits(args.Commits, args.BranchCommits)
	if len(commitPoints) == 0 {
		return empty, nil
	}

	candidates := monthlyCheckpoints(args.Commits, args.BranchCommits)
	samples := args.Options.Samples
	if samples < 1 {
		samples = 1
	}
	chosen := pickEvenly(candidates, samples)

	codeSize := []model.CodeSizePoint{}
	approximateMonths := []string{}
	for _, cp := range chosen {
		measured, err := measureCheckpoint(ctx, repoPath, cp.Sha, args.Options.MaxBytesPerSample)
		if err != nil {
			return empty, err
		}
		if measured.Lines == nil {
			approximateMonths = append(approximateMonths, cp.Month)
		}
		codeSize = append(codeSize, model.CodeSizePoint{Month: cp.Month, Bytes: measured.Bytes, Lines: measured.Lines})
	}

	warnings := []model.Warning{}
	approximate := len(approximateMonths) > 0
	if approximate {
		shown := approximateMonths
		rest := ""
		if len(shown) > 5 {
			rest = fmt.Sprintf(" and %d more", len(shown)-5)
			shown = shown[:5]
		}
		warnings = append(warnings, model.Warning{
			Code: "insights-approximate",
			Repo: nil,
			Message: fmt.Sprintf(
				"%d of %d code-size checkpoints (%s%s) hold more content than "+
					"ingest.insights.maxBytesPerSample (%d bytes), so their line counts were skipped and their "+
					"byte totals may include binary files (the blobs past the budget were never read, so they "+
					"could not be classified)",
				len(approximateMonths), len(codeSize), strings.Join(shown, ", "), rest, args.Options.MaxBytesPerSample),
		})
	}

	return ComputeInsightsResult{
		Insights: &model.RepoInsights{
			Commits:  commitPoints,
			CodeSize: codeSize,
			// "Sampled" means checkpoints were thinned — fewer months measured than months that
			// actually have commits. Zero-filled quiet months in Commits never had a tree of
			// their own to measure, so they do not count here.
			Sampled:     len(codeSize) < len(candidates),
			SampleCount: int64(len(codeSize)),
			Approximate: approximate,
		},
		Warnings: warnings,
	}, nil
}
