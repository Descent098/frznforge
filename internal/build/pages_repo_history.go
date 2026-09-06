package build

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"frznforge/internal/model"
	"frznforge/internal/render"
	"frznforge/internal/routes"
)

// A repository's history: the paginated commit list per branch, one page per commit, and the
// branches and tags tables. Port of src/pages/repos/[slug]/commits/**, commit/[sha].astro,
// branches/index.astro, tags/index.astro and src/components/CommitList.astro.
//
// Everything here looks a commit up through Repo.CommitFor rather than indexing Repo.Commits,
// so the display-support commits schema v6 added resolve too: a tag pointing at a
// rebase-orphaned commit, or a file whose last change fell outside the narrowed history, has a
// page and a subject line instead of a blank cell and a dead link.

// emitRepoHistory writes every history page of one repository.
func emitRepoHistory(b *Builder, repo *model.Repo) error {
	for _, branch := range repo.Branches {
		pageCount := routes.CommitsPageCount(repo, branch.Name)
		for p := 1; p <= pageCount; p++ {
			if err := emitCommitsPage(b, repo, branch, p, pageCount); err != nil {
				return err
			}
		}
	}
	// Commits first, then the display-support map. The two are disjoint by construction, and
	// both are keyed maps — sorted here because a map range would emit the same pages in a
	// different order on every run.
	for _, sha := range sortedShas(repo.Commits) {
		if err := emitCommitPage(b, repo, sha); err != nil {
			return err
		}
	}
	for _, sha := range sortedShas(repo.ExtraCommits) {
		if err := emitCommitPage(b, repo, sha); err != nil {
			return err
		}
	}
	if err := emitBranchesPage(b, repo); err != nil {
		return err
	}
	return emitTagsPage(b, repo)
}

/* ---- commit list --------------------------------------------------------- */

// commitsPage is what page-commits.gohtml renders.
type commitsPage struct {
	Head map[string]any
	List *commitList
}

// commitList is one page of one branch's history, as commit-list.gohtml draws it.
type commitList struct {
	Repo    *model.Repo
	RefName string
	Page    int
	// PageCount is at least 1; the pager is drawn only when it is more.
	PageCount int
	// Total is the branch's whole commit count, not this page's.
	Total  int64
	Groups []commitDay
	// PrevURL and NextURL are empty at the ends of the range, where the pager draws a disabled
	// control rather than a link.
	PrevURL string
	NextURL string
	// Branches are the switcher popup's entries, in artifact order.
	Branches []commitBranchLink
}

// commitDay is one day's worth of commits in the list, newest day first.
type commitDay struct {
	Label   string
	Commits []*model.Commit
}

// commitBranchLink is one row of the commit list's branch switcher.
type commitBranchLink struct {
	Name      string
	URL       string
	IsCurrent bool
	IsDefault bool
}

func emitCommitsPage(b *Builder, repo *model.Repo, branch model.Branch, page, pageCount int) error {
	title := fmt.Sprintf("Commits on %s · %s", branch.Name, repo.Name)
	if page > 1 {
		title = fmt.Sprintf("Commits on %s (page %d) · %s", branch.Name, page, repo.Name)
	}
	head := repoHead(b, repo, "commits")
	head["Heading"] = title

	list := &commitList{
		Repo:      repo,
		RefName:   branch.Name,
		Page:      page,
		PageCount: pageCount,
		Total:     int64(len(branch.Commits)),
		Groups:    groupByDay(repo, branch, page),
	}
	if page > 1 {
		list.PrevURL = b.Router.CommitsURL(repo.Slug, branch.Name, page-1)
	}
	if page < pageCount {
		list.NextURL = b.Router.CommitsURL(repo.Slug, branch.Name, page+1)
	}
	defaultBranch := deref(repo.DefaultBranch)
	for _, other := range repo.Branches {
		list.Branches = append(list.Branches, commitBranchLink{
			Name:      other.Name,
			URL:       b.Router.CommitsURL(repo.Slug, other.Name, 1),
			IsCurrent: other.Name == branch.Name,
			IsDefault: other.Name == defaultBranch,
		})
	}

	return b.WritePage(
		b.Router.CommitsURL(repo.Slug, branch.Name, page), "page-commits",
		render.Page{
			Title:        title,
			Description:  deref(repo.Description),
			Active:       "repos",
			Payload:      &commitsPage{Head: head, List: list},
			ExtraScripts: []string{b.Router.WithBase("/js/copy.js")},
		})
}

// groupByDay slices one page out of a branch's history and buckets it by UTC day.
//
// The bucket key is the first ten characters of the commit date, not a parsed one: the
// artifact stores ISO UTC, so the prefix IS the UTC day and no timezone can creep in between
// the grouping and the heading.
func groupByDay(repo *model.Repo, branch model.Branch, page int) []commitDay {
	start := (page - 1) * routes.CommitsPerPage
	end := start + routes.CommitsPerPage
	if start > len(branch.Commits) {
		start = len(branch.Commits)
	}
	if end > len(branch.Commits) {
		end = len(branch.Commits)
	}

	var groups []commitDay
	day := ""
	for _, sha := range branch.Commits[start:end] {
		c := repo.CommitFor(sha)
		if c == nil {
			continue
		}
		d := c.CommitDate
		if len(d) > 10 {
			d = d[:10]
		}
		if len(groups) == 0 || d != day {
			groups = append(groups, commitDay{Label: commitDayLabel(d)})
			day = d
		}
		groups[len(groups)-1].Commits = append(groups[len(groups)-1].Commits, c)
	}
	return groups
}

var commitDayMonths = [...]string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}

// commitDayLabel renders a YYYY-MM-DD bucket as "Sep 6, 2026".
//
// Spelled out rather than handed to a locale formatter, for the reason the TypeScript pinned
// timeZone: 'UTC' on its toLocaleDateString: the string is baked into static HTML and must not
// depend on where the build ran. A value that is not a date passes through unchanged.
func commitDayLabel(day string) string {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return day
	}
	return fmt.Sprintf("%s %d, %d", commitDayMonths[int(t.Month())-1], t.Day(), t.Year())
}

/* ---- one commit ---------------------------------------------------------- */

// commitPage is what page-commit.gohtml renders.
type commitPage struct {
	Head   map[string]any
	Repo   *model.Repo
	Commit *model.Commit
	// Parents are the parent shas this artifact actually holds a commit for — a parent that
	// was narrowed out has no page, so it is listed as neither a link nor bare text.
	Parents []string
	// RootCommit is true when the commit has no parents at all, which is a different fact from
	// having parents nobody kept.
	RootCommit bool
	// SameAuthor suppresses the "committed by" line when author and committer match.
	SameAuthor bool
	Files      []commitFileRow
}

// commitFileRow is one changed path on a commit page.
type commitFileRow struct {
	Path string
	// Href is the file's page on the default branch, or empty when the path is gone from it.
	Href string
	// Binary means git reported no line counts, so Additions and Deletions say nothing.
	Binary    bool
	Additions int64
	Deletions int64
}

func emitCommitPage(b *Builder, repo *model.Repo, sha string) error {
	commit := repo.CommitFor(sha)
	if commit == nil {
		return fmt.Errorf("commit %s: not in the artifact", sha)
	}

	head := repoHead(b, repo, "commits")
	// The commit page draws its own visible <h1> — the subject — so the header must not add a
	// second one above it.
	head["OwnHeading"] = true

	p := &commitPage{
		Head:       head,
		Repo:       repo,
		Commit:     commit,
		RootCommit: len(commit.Parents) == 0,
		SameAuthor: commit.Author.Name == commit.Committer.Name && commit.Author.Email == commit.Committer.Email,
	}
	for _, parent := range commit.Parents {
		if repo.CommitFor(parent) != nil {
			p.Parents = append(p.Parents, parent)
		}
	}

	defaultBranch := deref(repo.DefaultBranch)
	for _, f := range commit.Files {
		row := commitFileRow{Path: f.Path, Binary: f.Additions == nil || f.Deletions == nil}
		if f.Additions != nil {
			row.Additions = *f.Additions
		}
		if f.Deletions != nil {
			row.Deletions = *f.Deletions
		}
		// A changed path links to its file page only while it still exists on the default
		// branch: a deleted or renamed-away file has no page to point at.
		if _, ok := repo.Files[f.Path]; ok && defaultBranch != "" {
			row.Href = b.Router.BlobURL(repo.Slug, defaultBranch, f.Path)
		}
		p.Files = append(p.Files, row)
	}

	return b.WritePage(b.Router.CommitURL(repo.Slug, sha), "page-commit", render.Page{
		Title:        commit.Subject + " · " + repo.Name,
		Description:  deref(repo.Description),
		Active:       "repos",
		Payload:      p,
		ExtraScripts: []string{b.Router.WithBase("/js/copy.js")},
	})
}

/* ---- branches ------------------------------------------------------------ */

// branchesPage is what page-branches.gohtml renders.
type branchesPage struct {
	Head map[string]any
	Repo *model.Repo
	// Capped is how many branches have no file browser, so the page can say why.
	Capped int
	Rows   []branchRow
}

// branchRow is one branch in the table.
type branchRow struct {
	Branch    model.Branch
	IsDefault bool
	// Browsable is false for a branch past ingest.branchTrees: it has history and counts but no
	// tree, so its name must not be a link.
	Browsable bool
	TreeURL   string
	// CommitCount is len(Branch.Commits) as an int64, because the display helpers take one.
	CommitCount int64
	CommitsURL  string
	Head        *model.Commit
}

func emitBranchesPage(b *Builder, repo *model.Repo) error {
	defaultBranch := deref(repo.DefaultBranch)
	// ingest.branchTrees caps how many non-default branches get a tree, so a repo with more
	// than that has branches with no file browser. The default branch always has one and never
	// counts against the cap; everything else has one only if it reached RefTrees.
	browsable := func(name string) bool {
		if name == defaultBranch {
			return true
		}
		_, ok := repo.RefTrees.Get(name)
		return ok
	}

	sorted := append([]model.Branch(nil), repo.Branches...)
	// Default branch first, then most recently committed, then by name. Code-point order
	// throughout — a locale-aware compare would depend on the build machine's ICU data.
	sort.SliceStable(sorted, func(i, j int) bool {
		di, dj := sorted[i].Name == defaultBranch, sorted[j].Name == defaultBranch
		if di != dj {
			return di
		}
		if sorted[i].LastCommitDate != sorted[j].LastCommitDate {
			return sorted[i].LastCommitDate > sorted[j].LastCommitDate
		}
		return sorted[i].Name < sorted[j].Name
	})

	p := &branchesPage{Head: repoHead(b, repo, "branches"), Repo: repo}
	for _, br := range sorted {
		row := branchRow{
			Branch:      br,
			IsDefault:   br.Name == defaultBranch,
			Browsable:   browsable(br.Name),
			CommitCount: int64(len(br.Commits)),
			CommitsURL:  b.Router.CommitsURL(repo.Slug, br.Name, 1),
			Head:        repo.CommitFor(br.Head),
		}
		if row.Browsable {
			row.TreeURL = b.Router.TreeURL(repo.Slug, br.Name, "")
		} else {
			p.Capped++
		}
		p.Rows = append(p.Rows, row)
	}

	return b.WritePage(b.Router.BranchesURL(repo.Slug), "page-branches", render.Page{
		Title:       "Branches · " + repo.Name,
		Description: deref(repo.Description),
		Active:      "repos",
		Payload:     p,
	})
}

/* ---- tags ---------------------------------------------------------------- */

// tagsPage is what page-tags.gohtml renders.
type tagsPage struct {
	Head map[string]any
	Repo *model.Repo
	Rows []tagRow
}

// tagRow is one tag in the table.
type tagRow struct {
	Tag model.Tag
	// Message is the first line of an annotated tag's message, or empty.
	Message string
	// TargetURL is empty when the artifact holds no commit for the target — a tag can outlive
	// the history that reached it.
	TargetURL  string
	ArchiveURL string
	ReleaseURL string
}

func emitTagsPage(b *Builder, repo *model.Repo) error {
	sorted := append([]model.Tag(nil), repo.GitTags...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Date != sorted[j].Date {
			return sorted[i].Date > sorted[j].Date
		}
		return sorted[i].Name < sorted[j].Name
	})

	hasArchive := func(name string) bool {
		for _, a := range repo.Archives {
			if a.Kind == "tag" && a.Ref == name {
				return true
			}
		}
		return false
	}

	p := &tagsPage{Head: repoHead(b, repo, "tags"), Repo: repo}
	for _, t := range sorted {
		row := tagRow{Tag: t}
		if t.Annotated {
			row.Message = strings.SplitN(deref(t.Message), "\n", 2)[0]
			row.ReleaseURL = b.Router.ReleaseURL(repo.Slug, t.Name)
		}
		if repo.CommitFor(t.Target) != nil {
			row.TargetURL = b.Router.CommitURL(repo.Slug, t.Target)
		}
		if hasArchive(t.Name) {
			row.ArchiveURL = b.Router.ArchiveURL(repo.Slug, t.Name)
		}
		p.Rows = append(p.Rows, row)
	}

	return b.WritePage(b.Router.TagsURL(repo.Slug), "page-tags", render.Page{
		Title:       "Tags · " + repo.Name,
		Description: deref(repo.Description),
		Active:      "repos",
		Payload:     p,
	})
}

/* ---- small helpers ------------------------------------------------------- */

// sortedShas orders a commit map's keys. Ranging a map directly would emit the same pages in a
// different order on every run, which is exactly the kind of difference a "static site" is not
// allowed to have.
func sortedShas(m map[string]model.Commit) []string {
	out := make([]string, 0, len(m))
	for sha := range m {
		out = append(out, sha)
	}
	sort.Strings(out)
	return out
}
