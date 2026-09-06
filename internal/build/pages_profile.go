package build

// The owner profile page at / — the port of src/pages/index.astro.
//
// It also carries the two libraries that page is the only caller of: src/lib/contrib.ts (the
// 52-week contribution graph) and src/lib/activity.ts (the recent-activity feed). They live
// here rather than in a shared package because nothing else on the site reads them, and a
// family that owns its own derivations is what lets the families be written independently.
//
// Everything below takes `now` as a parameter rather than reading the clock, for the reason
// the package comment gives: two machines must emit the same HTML from the same artifact.

import (
	"fmt"
	"html/template"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"frznforge/internal/config"
	"frznforge/internal/frontmatter"
	"frznforge/internal/markdown"
	"frznforge/internal/model"
	"frznforge/internal/render"
)

// utcDay is the graph's unit of date arithmetic. contrib.ts adds a literal 86_400_000 ms; in UTC
// — which is the only zone anything here works in — that is exactly 24 hours, and staying in
// UTC is what keeps the grid identical on a machine in any timezone.
const utcDay = 24 * time.Hour

/* ---- page payload -------------------------------------------------------- */

// profilePayload is what page-profile renders. Field for field it is the set of values
// index.astro computes in its frontmatter, pre-shaped so the template holds markup and
// nothing else.
type profilePayload struct {
	// Hero.
	Bio       string
	Location  string
	Workplace string
	School    string
	Email     string
	Links     []heroLink

	// KPIs.
	RepoCount       int64
	TouchedRecently int
	Templates       int
	// HotWindow is the copy for the `theme.heat.hot` window, so the KPI text and the orange
	// accent always agree on what counts as recent.
	HotWindow       string
	HotDays         int
	CommitsThisYear int64
	CommitsRecent   int64
	Years           int
	FirstCommit     string
	Languages       []profileLang

	// Body columns.
	Readme    template.HTML
	HasReadme bool
	Activity  []activityRow

	// Pinned / fallback cards.
	Pinned bool
	Cards  []render.RepoSummary

	Graph     contribGraph
	ShowGraph bool
}

// heroLink is one pill under the owner's name: a personal site, LinkedIn, or a forge account.
// The three sources render the same markup, so they are flattened into one list here rather
// than into three near-identical loops in the template.
type heroLink struct {
	Class string
	Href  string
	Icon  string
	Label string
}

// profileLang is one row of the "Top languages" KPI.
//
// Dot is template.CSS rather than a string because html/template's CSS filter rejects
// `var(--hf-lang-other)` outright and replaces it with ZgotmplZ. swatch() is what makes the
// bypass safe: a colour is either a literal hex triple or the one fixed fallback token, so
// nothing from the artifact can reach the stylesheet uninspected.
type profileLang struct {
	Name    string
	Percent float64
	Dot     template.CSS
}

// activityRow is one line of the recent-activity list, with the heat bucket and the age string
// already resolved so the template does not compute the same value three times.
type activityRow struct {
	IsTag  bool
	Icon   string
	Heat   string
	Repo   string
	Branch string
	Count  int
	Tag    string
	Date   string
	Age    string
	// URL is the branch's commit list for a push, the repo's tag list for a tag.
	URL string
}

/* ---- the emitter --------------------------------------------------------- */

// emitProfile writes /.
func emitProfile(b *Builder) error {
	fm := b.Site.Profile.Frontmatter
	heat := b.Cfg.Theme.Heat
	now := b.Site.Now
	repos := b.Data.Repos

	p := &profilePayload{
		Bio:             fmString(fm, "bio"),
		Location:        fmString(fm, "location"),
		Workplace:       fmString(fm, "workplace"),
		School:          fmString(fm, "school"),
		Email:           fmString(fm, "email"),
		Links:           heroLinks(fm),
		RepoCount:       int64(len(repos)),
		HotDays:         heat.Hot,
		HotWindow:       hotWindowCopy(heat.Hot),
		CommitsThisYear: commitsSince(repos, 365, now),
		CommitsRecent:   commitsSince(repos, heat.Hot, now),
		FirstCommit:     earliestCommit(repos),
		Languages:       aggregateLanguages(repos, 5),
	}
	p.Years = yearsSince(p.FirstCommit, now)
	for i := range repos {
		if repos[i].Template {
			p.Templates++
		}
		if render.HeatFor(deref(repos[i].UpdatedAt), now, heat) == "hot" {
			p.TouchedRecently++
		}
	}

	// The profile README is rendered by loadContent (trusted: it is the site owner's own
	// file). An entry whose body is only whitespace gets no card at all, matching the
	// `profile?.body?.trim()` guard in index.astro.
	p.Readme = b.Site.Profile.Body
	p.HasReadme = b.Site.Profile.Exists && strings.TrimSpace(string(p.Readme)) != ""

	for _, e := range buildActivity(repos, 6) {
		row := activityRow{
			IsTag: e.Type == "tag",
			Icon:  "#i-commit",
			Heat:  render.HeatFor(e.Date, now, heat),
			Repo:  e.Repo, Branch: e.Branch, Count: e.Count, Tag: e.Tag, Date: e.Date,
			Age: render.RelativeTimeShort(e.Date, now),
			URL: b.Router.CommitsURL(e.Repo, e.Branch, 1),
		}
		if row.IsTag {
			row.Icon = "#i-tag"
			row.URL = b.Router.TagsURL(e.Repo)
		}
		p.Activity = append(p.Activity, row)
	}

	p.Cards, p.Pinned = profileCards(b, fmList(fm, "pinned"))
	p.Graph = buildContribGraph(repos, fmList(fm, "identities"), now, heat)
	// The graph is hidden only for a site that has neither commits nor repositories — a
	// configured repo with no commits still gets an (empty) year, because its emptiness is
	// the thing worth showing.
	p.ShowGraph = p.Graph.Total != 0 || len(repos) != 0

	page := render.Page{
		Title:       b.Cfg.Site.Title,
		Description: firstNonEmpty(p.Bio, b.Cfg.Site.Description),
		Active:      "profile",
		Payload:     p,
	}
	if p.HasReadme && markdown.ContainsMermaid(string(p.Readme)) {
		page.ExtraScripts = []string{b.Router.WithBase("/js/mermaid.js")}
	}
	return b.WritePage(b.Router.HomeURL(), "page-profile", page)
}

// profileCards resolves the pinned repos, falling back to the first six in artifact order.
//
// A pin that names no repo is reported rather than dropped in silence: the slug is written by
// hand in profile.md, so a typo otherwise removes a card the owner believes is there.
func profileCards(b *Builder, pinned []string) ([]render.RepoSummary, bool) {
	bySlug := make(map[string]*model.Repo, len(b.Data.Repos))
	for i := range b.Data.Repos {
		bySlug[b.Data.Repos[i].Slug] = &b.Data.Repos[i]
	}
	var cards []render.RepoSummary
	for _, slug := range pinned {
		repo, ok := bySlug[slug]
		if !ok {
			fmt.Fprintf(os.Stderr, "[frznforge] profile.md pins unknown repo %q\n", slug)
			continue
		}
		cards = append(cards, render.Summarize(repo))
	}
	if len(cards) > 0 {
		return cards, true
	}
	for i := range b.Data.Repos {
		if i == 6 {
			break
		}
		cards = append(cards, render.Summarize(&b.Data.Repos[i]))
	}
	return cards, false
}

// hotWindowCopy names the recency window in prose. The default seven days reads better as
// "this week" than as "in the last 7 days"; anything else is spelled out.
func hotWindowCopy(hotDays int) string {
	if hotDays == 7 {
		return "this week"
	}
	return fmt.Sprintf("in the last %d days", hotDays)
}

// heroLinks flattens the frontmatter's sites, LinkedIn and forge accounts into one pill list.
// The first personal site carries the accent modifier.
func heroLinks(fm map[string]any) []heroLink {
	var out []heroLink
	for i, url := range fmList(fm, "sites") {
		class := "hf-pill"
		if i == 0 {
			class = "hf-pill hf-pill--accent"
		}
		out = append(out, heroLink{Class: class, Href: url, Icon: "#i-globe", Label: render.PrettyURL(url)})
	}
	if in := fmString(fm, "linkedin"); in != "" {
		out = append(out, heroLink{Class: "hf-pill", Href: in, Icon: "#i-linkedin", Label: "LinkedIn"})
	}
	// Forge order is the declaration order of the `forges` block in src/content.config.ts, not
	// map order: a Go map has none, and the pills must not shuffle between builds.
	forges := fmMap(fm, "forges")
	for _, key := range forgeOrder {
		url, ok := forges[key]
		if !ok || url == "" {
			continue
		}
		icon := "#i-forge"
		if key == "github" {
			icon = "#i-github"
		}
		label := forgeLabels[key]
		if label == "" {
			label = key
		}
		out = append(out, heroLink{Class: "hf-pill", Href: url, Icon: icon, Label: label})
	}
	return out
}

var (
	forgeOrder  = []string{"github", "gitlab", "codeberg", "forgejo", "gitea"}
	forgeLabels = map[string]string{
		"github": "GitHub", "gitlab": "GitLab", "codeberg": "Codeberg",
		"forgejo": "Forgejo", "gitea": "Gitea",
	}
)

/* ---- frontmatter access -------------------------------------------------- */

// The profile's front matter arrives as map[string]any holding strings and string lists (see
// build.contentFrom). These three readers are the only place that shape is assumed, so a key
// of the wrong type renders as absent rather than as a panic in the middle of a build.

func fmString(fm map[string]any, key string) string {
	s, _ := fm[key].(string)
	return s
}

func fmList(fm map[string]any, key string) []string {
	l, _ := fm[key].([]string)
	return l
}

// fmMap reads a nested mapping such as `forges:`.
//
// build.contentFrom carries these as frontmatter's ordered entry slice; the map shapes are
// accepted too so a caller that builds a payload by hand (a test, a future loader) works.
// Order does not matter to this reader — forgeOrder decides the pills.
func fmMap(fm map[string]any, key string) map[string]string {
	out := map[string]string{}
	switch v := fm[key].(type) {
	case []frontmatter.MapEntry:
		for _, e := range v {
			out[e.Key] = e.Value
		}
	case map[string]string:
		for k, s := range v {
			out[k] = s
		}
	case map[string]any:
		for k, raw := range v {
			if s, ok := raw.(string); ok {
				out[k] = s
			}
		}
	}
	return out
}

/* ---- aggregates (port of src/lib/format.ts) ------------------------------ */

// commitsSince counts commits across every repo whose COMMIT date falls inside the window.
//
// Not deduplicated by sha, matching format.ts: the same commit present in two scanned repos is
// counted twice. Left as it is deliberately — this is the number the KPI has always shown, and
// changing it here would make the Go and TypeScript builds disagree about a headline figure.
func commitsSince(repos []model.Repo, days int, now time.Time) int64 {
	cutoff := now.Add(-time.Duration(days) * utcDay)
	var n int64
	for i := range repos {
		// Unsorted map iteration is fine here and only here: the result is a count, so the
		// order the commits are visited in cannot change it.
		for _, c := range repos[i].Commits {
			d, err := time.Parse(time.RFC3339, c.CommitDate)
			if err != nil {
				continue // `new Date(x) >= cutoff` is false for an unparseable date
			}
			if !d.Before(cutoff) {
				n++
			}
		}
	}
	return n
}

// earliestCommit is the oldest createdAt across repos, or "". ISO timestamps compare correctly
// as plain strings, which is why this never parses one.
func earliestCommit(repos []model.Repo) string {
	oldest := ""
	for i := range repos {
		c := deref(repos[i].CreatedAt)
		if c != "" && (oldest == "" || c < oldest) {
			oldest = c
		}
	}
	return oldest
}

// yearsSince counts whole years between a date and now, floored at zero — 365.25 days to the
// year, the same approximation format.ts makes. An unparseable date counts as no history rather
// than as the NaN the JavaScript would render into the page.
func yearsSince(from string, now time.Time) int {
	if from == "" {
		return 0
	}
	d, err := time.Parse(time.RFC3339, from)
	if err != nil {
		return 0
	}
	years := int(float64(now.Sub(d).Milliseconds()) / (365.25 * 86_400_000))
	if years < 0 {
		return 0
	}
	return years
}

// hexColor is the shape swatch() will pass through untouched: CSS hex notation, 3/4/6/8 digits.
var hexColor = regexp.MustCompile(`^#(?:[0-9A-Fa-f]{3,4}|[0-9A-Fa-f]{6}|[0-9A-Fa-f]{8})$`)

// swatch turns a language colour into a CSS value the template may interpolate.
//
// The typed return bypasses html/template's CSS filter, which is the only way to emit
// `var(--hf-lang-other)` at all — the filter rejects it. That bypass is safe ONLY because
// everything else is rejected here first: a value that is not literal hex notation becomes the
// fallback token, so no string from the artifact reaches a style attribute unexamined.
func swatch(color *string) template.CSS {
	if color != nil && hexColor.MatchString(*color) {
		return template.CSS(*color)
	}
	return template.CSS("var(--hf-lang-other)")
}

// aggregateLanguages sums language bytes across repos and recomputes the shares, keeping the
// top n and folding the rest into "Other".
func aggregateLanguages(repos []model.Repo, n int) []profileLang {
	type agg struct {
		bytes int64
		color *string
	}
	totals := map[string]*agg{}
	var names []string
	for i := range repos {
		for _, l := range repos[i].Languages {
			cur, ok := totals[l.Name]
			if !ok {
				// Colour comes from the first repo to report the language, as in format.ts.
				cur = &agg{color: l.Color}
				totals[l.Name] = cur
				names = append(names, l.Name)
			}
			cur.bytes += l.Bytes
		}
	}
	var total int64
	for _, a := range totals {
		total += a.bytes
	}
	if total == 0 {
		return nil
	}
	// Bytes descending, then name — code-point order, never a locale-aware compare, which
	// would depend on the build machine's ICU data.
	sort.Slice(names, func(i, j int) bool {
		a, b := totals[names[i]], totals[names[j]]
		if a.bytes != b.bytes {
			return a.bytes > b.bytes
		}
		return names[i] < names[j]
	})

	head := names
	var rest int64
	if len(names) > n {
		head = names[:n]
		for _, name := range names[n:] {
			rest += totals[name].bytes
		}
	}
	out := make([]profileLang, 0, len(head)+1)
	for _, name := range head {
		a := totals[name]
		out = append(out, profileLang{Name: name, Percent: sharePercent(a.bytes, total), Dot: swatch(a.color)})
	}
	if len(names) > n {
		out = append(out, profileLang{Name: "Other", Percent: sharePercent(rest, total), Dot: swatch(nil)})
	}
	return out
}

// sharePercent is `Math.round((bytes / total) * 1000) / 10` — one decimal place, rounded the
// way JavaScript rounds so the two builds print the same number.
func sharePercent(bytes, total int64) float64 {
	return jsRound(float64(bytes)/float64(total)*1000) / 10
}

// jsRound matches Math.round: halfway cases go towards +Infinity, not away from zero the way
// Go's math.Round does.
func jsRound(f float64) float64 {
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

/* ---- recent activity (port of src/lib/activity.ts) ----------------------- */

// activityEvent is one derived event: a day's worth of commits pushed to a branch, or a tag.
type activityEvent struct {
	Type    string // "push" | "tag"
	Repo    string
	Branch  string
	Count   int
	Tag     string
	Date    string
	Subject string
}

// buildActivity derives the merged event list, newest first, capped at limit.
func buildActivity(repos []model.Repo, limit int) []activityEvent {
	var events []activityEvent
	for i := range repos {
		repo := &repos[i]
		for _, branch := range repo.Branches {
			// One "push" per UTC day. Insertion order is kept explicitly: the JavaScript reads
			// a Map, and a Go map has no order at all.
			var days []string
			byDay := map[string]*activityEvent{}
			for _, sha := range branch.Commits {
				// Repo.Commits only — never CommitFor. An aggregate that also read the
				// display-support map would count commits the history knobs excluded.
				c, ok := repo.Commits[sha]
				if !ok {
					continue
				}
				d := isoDayOf(c.CommitDate)
				cur, seen := byDay[d]
				if !seen {
					byDay[d] = &activityEvent{
						Type: "push", Repo: repo.Slug, Branch: branch.Name,
						Count: 1, Date: c.CommitDate, Subject: c.Subject,
					}
					days = append(days, d)
					continue
				}
				cur.Count++
				if c.CommitDate > cur.Date {
					cur.Date = c.CommitDate
					cur.Subject = c.Subject
				}
			}
			for _, d := range days {
				events = append(events, *byDay[d])
			}
		}
		for _, t := range repo.GitTags {
			events = append(events, activityEvent{Type: "tag", Repo: repo.Slug, Tag: t.Name, Date: t.Date})
		}
	}
	// Newest first. A plain code-point compare, not localeCompare: these are ISO timestamps,
	// where the two agree, and only one of them gives the same answer on every machine.
	sort.SliceStable(events, func(i, j int) bool { return events[i].Date > events[j].Date })

	seen := map[string]bool{}
	out := make([]activityEvent, 0, limit)
	for _, e := range events {
		// Drop a push already shown from another branch — a feature branch merged into main
		// contains the same day's commits and would otherwise appear twice.
		key := "t:" + e.Repo + ":" + e.Tag
		if e.Type == "push" {
			key = fmt.Sprintf("p:%s:%s:%d:%s", e.Repo, isoDayOf(e.Date), e.Count, e.Subject)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, e)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// isoDayOf is the first ten characters of an ISO timestamp, matching `date.slice(0, 10)` —
// including for a string that is not one, which comes back unchanged rather than reformatted.
func isoDayOf(date string) string {
	if len(date) < 10 {
		return date
	}
	return date[:10]
}

/* ---- contribution graph (port of src/lib/contrib.ts) --------------------- */

// contribCell is one day in the grid.
type contribCell struct {
	Day   string
	Count int
	// Level is 0 (none) … 4 (highest quartile); it picks the opacity, Heat picks the hue.
	Level int
	Heat  string
	// Family is the colour class for the cell's heat bucket, or "" at level 0 — the level-0
	// cell carries no class at all, which is what leaves it the neutral canvas colour.
	Family string
}

// contribMonth is a month label and the grid columns it spans.
//
// Col/EndCol are 1-based CSS grid lines, filled in once the whole list is known: a label runs
// from its own week to the start of the next month, and the last one runs to the end of the
// graph. They are numbers rather than a ready-made `grid-column: a / b` string because
// html/template's CSS filter inspects whatever a style attribute interpolates, and two ints
// pass it where the assembled declaration would not.
type contribMonth struct {
	Week   int
	Label  string
	Col    int
	EndCol int
}

// contribGraph is 52 weeks × 7 days, column-major, ending on the Saturday of now's week.
type contribGraph struct {
	// Weeks[w][d] is nil for a day past today — the tail of the current week.
	Weeks         [][]*contribCell
	Months        []contribMonth
	Total         int64
	LongestStreak int
	BusiestDay    *contribCell
}

// commitsByDay counts commits per UTC authoring day, deduped by sha across repos. identities
// are author emails to count; empty counts everyone.
func commitsByDay(repos []model.Repo, identities []string) map[string]int {
	ids := map[string]bool{}
	for _, e := range identities {
		ids[strings.ToLower(e)] = true
	}
	seen := map[string]bool{}
	days := map[string]int{}
	for i := range repos {
		// Sorted, not map order: the counts come out the same either way (a sha names one
		// commit), but a build that iterates a map is one refactor away from not being
		// reproducible.
		shas := make([]string, 0, len(repos[i].Commits))
		for sha := range repos[i].Commits {
			shas = append(shas, sha)
		}
		sort.Strings(shas)
		for _, sha := range shas {
			if seen[sha] {
				continue
			}
			seen[sha] = true
			c := repos[i].Commits[sha]
			if len(ids) > 0 && !ids[strings.ToLower(c.Author.Email)] {
				continue
			}
			days[isoDayOf(c.AuthorDate)]++
		}
	}
	return days
}

// contribFamily maps a heat bucket to the colour class the .c-* rules key on.
var contribFamily = map[string]string{
	"hot": "c-hot", "warm": "c-warm", "neutral": "c-neutral", "cool": "c-cool", "cold": "c-cold",
}

// buildContribGraph builds the 52-week grid ending on the Saturday of now's UTC week.
//
// Intensity is a quartile of the commit counts INSIDE the window, not an absolute scale: a
// year with three commits and a year with three thousand both use the full palette, which is
// the only way one set of five colours reads on every profile.
func buildContribGraph(repos []model.Repo, identities []string, now time.Time, heat config.HeatConfig) contribGraph {
	counts := commitsByDay(repos, identities)
	y, m, d := now.UTC().Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	end := today.Add(time.Duration(6-int(today.Weekday())) * utcDay) // Saturday of this week
	start := end.Add(-time.Duration(52*7-1) * utcDay)                // 52 weeks back, a Sunday

	// Quartile thresholds over the non-empty days already inside the window.
	var window []int
	for t := start; !t.After(today); t = t.Add(utcDay) {
		if c := counts[isoDayUTC(t)]; c > 0 {
			window = append(window, c)
		}
	}
	sort.Ints(window)
	quantile := func(p float64) int {
		if len(window) == 0 {
			return 1
		}
		i := int(p * float64(len(window)))
		if i > len(window)-1 {
			i = len(window) - 1
		}
		return window[i]
	}
	t1, t2, t3 := quantile(0.25), quantile(0.5), quantile(0.75)
	levelOf := func(c int) int {
		switch {
		case c == 0:
			return 0
		case c <= t1:
			return 1
		case c <= t2:
			return 2
		case c <= t3:
			return 3
		}
		return 4
	}

	g := contribGraph{Weeks: make([][]*contribCell, 0, 52)}
	streak, lastMonth := 0, ""
	for w := 0; w < 52; w++ {
		col := make([]*contribCell, 0, 7)
		for i := 0; i < 7; i++ {
			t := start.Add(time.Duration(w*7+i) * utcDay)
			if t.After(today) {
				col = append(col, nil)
				continue
			}
			count := counts[isoDayUTC(t)]
			cell := &contribCell{
				Day: isoDayUTC(t), Count: count, Level: levelOf(count),
				Heat: render.HeatFor(t.Format(time.RFC3339), now, heat),
			}
			if cell.Level > 0 {
				cell.Family = contribFamily[cell.Heat]
			}
			col = append(col, cell)
			g.Total += int64(count)
			if count > 0 {
				streak++
				if streak > g.LongestStreak {
					g.LongestStreak = streak
				}
			} else {
				streak = 0
			}
			if count > 0 && (g.BusiestDay == nil || count > g.BusiestDay.Count) {
				g.BusiestDay = cell
			}
		}
		g.Weeks = append(g.Weeks, col)

		wt := start.Add(time.Duration(w*7) * utcDay)
		if month := isoDayUTC(wt)[:7]; month != lastMonth {
			lastMonth = month
			g.Months = append(g.Months, contribMonth{Week: w, Label: wt.Format("Jan")})
		}
	}
	// A leading label crowded by the next one has no room to render; drop it.
	if len(g.Months) > 1 && g.Months[1].Week-g.Months[0].Week < 2 {
		g.Months = g.Months[1:]
	}
	for i := range g.Months {
		next := 52
		if i+1 < len(g.Months) {
			next = g.Months[i+1].Week
		}
		g.Months[i].Col, g.Months[i].EndCol = g.Months[i].Week+1, next+1
	}
	return g
}

// isoDayUTC formats a UTC instant as the ISO day the graph keys on.
func isoDayUTC(t time.Time) string { return t.UTC().Format("2006-01-02") }
