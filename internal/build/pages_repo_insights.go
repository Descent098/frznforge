package build

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"frznforge/internal/model"
	"frznforge/internal/render"
	"frznforge/internal/routes"
)

// Per-repo Insights — the port of src/pages/repos/[slug]/insights/index.astro and
// InsightsChart.astro, the two largest components in the project.
//
// Every chart is a build-time inline SVG: no chart library, no client JavaScript, and every
// number the picture shows is repeated in a visually hidden data table beside it, because a
// shape's <title> is a mouse affordance and is not exposed at all when the SVG is one image
// node. tests/e2e/insights.spec.ts compares the two, value for value.
//
// The page is built only where routes.HasInsights says it exists — the same predicate the
// Insights tab and the route list use, so a tab can never point at a page that was not built.

func emitRepoInsights(b *Builder, repo *model.Repo) error {
	if !routes.HasInsights(repo) {
		return nil
	}
	page := newInsightsPage(b, repo)
	return b.WritePage(b.Router.InsightsURL(repo.Slug), "page-insights", render.Page{
		Title:       "Insights · " + repo.Name,
		Description: deref(repo.Description),
		Active:      "repos",
		Payload:     page,
		ExtraStyles: []string{b.Router.WithBase("/css/insights.css")},
	})
}

/* ---- the page ------------------------------------------------------------ */

// insightsPage is the whole page: four KPI tiles and up to three charts.
type insightsPage struct {
	Head          repoSubHead
	DefaultBranch string
	// RangeText is the span the series covers, "2026-08 → 2026-09", or one month, or "—".
	RangeText string
	// HasSeries is false only for an artifact that reached this URL with nothing to plot.
	HasSeries   bool
	Approximate bool

	Commits      string
	Contributors string
	// Elsewhere counts authors who only ever landed on other refs — worth a word, since the
	// repo listing counts them and this page does not.
	Elsewhere string
	Months    string
	MonthsSub string

	// The code-size tile. 0.3.0 made LINES the headline when the latest checkpoint managed to
	// count them, demoting the byte size to the sub-line; a checkpoint that went over the read
	// budget keeps bytes as the headline, because there is nothing else honest to show.
	SizeLabel   string
	SizeValue   string
	SizeSampled bool
	SizeByLines bool
	SizeBytes   string
	SizeMonth   string

	Charts []chartView
	// NoCodeSize replaces the third chart with an empty state.
	NoCodeSize bool
}

func newInsightsPage(b *Builder, repo *model.Repo) insightsPage {
	branch := deref(repo.DefaultBranch)
	p := insightsPage{
		Head:          newRepoSubHead(b, repo).forPage("insights", "", true),
		DefaultBranch: branch,
		RangeText:     "—",
	}
	if repo.Insights == nil || len(repo.Insights.Commits) == 0 {
		return p
	}
	ins := repo.Insights
	commitPoints, codePoints := ins.Commits, ins.CodeSize
	months := len(commitPoints)
	p.HasSeries = true
	p.Approximate = ins.Approximate

	// The SPAN, and the months inside it that actually saw a commit. Saying "active" when you
	// mean "span" is a lie the reader can check: ingest only measures a code-size checkpoint in
	// a month that has a commit, so activeMonths is the number sampleCount compares against.
	var totalCommits, activeMonths int64
	busiest, peopleMonth := &commitPoints[0], &commitPoints[0]
	for i := range commitPoints {
		pt := &commitPoints[i]
		totalCommits += pt.Commits
		if pt.Commits > 0 {
			activeMonths++
		}
		if pt.Commits > busiest.Commits {
			busiest = pt
		}
		if pt.Contributors > peopleMonth.Contributors {
			peopleMonth = pt
		}
	}

	first, last := commitPoints[0].Month, commitPoints[months-1].Month
	p.RangeText = first
	if first != last {
		p.RangeText = first + " → " + last
	}

	branchContributors := defaultBranchAuthors(repo)
	elsewhere := int64(len(repo.Contributors) - branchContributors)
	if elsewhere < 0 {
		elsewhere = 0
	}
	p.Commits = render.FormatInt(totalCommits)
	p.Contributors = render.FormatInt(int64(branchContributors))
	if elsewhere > 0 {
		p.Elsewhere = render.FormatInt(elsewhere)
	}
	p.Months = render.FormatInt(int64(months))
	p.MonthsSub = p.RangeText
	if activeMonths < int64(months) {
		p.MonthsSub = render.FormatInt(activeMonths) + " with commits"
	}

	// Lines are only honest when EVERY checkpoint counted them: one point over the ingest byte
	// budget makes the whole series bytes-only, so the axis and the label stay stricter than
	// the tile, which is about the latest point alone.
	hasLines := len(codePoints) > 0
	for _, pt := range codePoints {
		if pt.Lines == nil {
			hasLines = false
			break
		}
	}
	sizeLabel := "Code size"
	if hasLines {
		sizeLabel = "Lines of code"
	}
	sizePoints := make([]chartPoint, 0, len(codePoints))
	for _, pt := range codePoints {
		value := pt.Bytes
		if hasLines {
			value = *pt.Lines
		}
		sizePoints = append(sizePoints, chartPoint{Month: pt.Month, Value: value})
	}

	p.SizeLabel = "Code size"
	p.SizeValue = "—"
	if len(codePoints) > 0 {
		latest := codePoints[len(codePoints)-1]
		p.SizeSampled = true
		p.SizeMonth = latest.Month
		p.SizeBytes = render.FormatBytes(latest.Bytes)
		p.SizeValue = p.SizeBytes
		if latest.Lines != nil {
			p.SizeByLines = true
			p.SizeLabel = "Lines of code"
			p.SizeValue = render.FormatInt(*latest.Lines)
		}
	}

	/* ---- honesty notes ---- */
	var notes []string
	if !hasLines && len(codePoints) > 0 {
		notes = append(notes, "Sampled by size: counting lines means reading every text file at that checkpoint, so a checkpoint over the ingest byte budget records bytes only.")
	}
	if ins.Sampled && len(codePoints) > 0 && int64(len(codePoints)) < activeMonths {
		notes = append(notes, fmt.Sprintf("Measured at %s out of %s with commits — evenly spaced, always including the first and the last.",
			only(int64(len(codePoints)), "checkpoint"), only(activeMonths, "month")))
	}
	if len(codePoints) == 1 {
		notes = append(notes, "Only one checkpoint was measured, so there is no trend to read yet.")
	}
	// Shared by the bar chart and the line chart, so the wording cannot say "bar".
	singleMonth := ""
	if months == 1 {
		singleMonth = "One month of history so far — a single point is all there is to plot."
	}

	/* ---- charts ---- */
	commitSeries := make([]chartPoint, 0, months)
	peopleSeries := make([]chartPoint, 0, months)
	for _, pt := range commitPoints {
		commitSeries = append(commitSeries, chartPoint{Month: pt.Month, Value: pt.Commits})
		peopleSeries = append(peopleSeries, chartPoint{Month: pt.Month, Value: pt.Contributors})
	}

	p.Charts = append(p.Charts, newChart(chartSpec{
		ID: "chart-commits", Kind: "bars", Tone: "ember",
		Heading:    "Commits per month",
		Caption:    "Every commit on " + branch + ", bucketed by the month it was authored (UTC). Exact — no sampling.",
		ValueLabel: "Commits", Unit: "count", Noun: "commit",
		Points: commitSeries,
		Summary: fmt.Sprintf("%s across %s, peaking at %d in %s.",
			only(totalCommits, "commit"), only(int64(months), "month"), busiest.Commits, busiest.Month),
		Note: singleMonth,
	}))
	p.Charts = append(p.Charts, newChart(chartSpec{
		ID: "chart-contributors", Kind: "line", Tone: "ice",
		Heading:    "Contributors per month",
		Caption:    "Distinct author email addresses that landed a commit in each month. People who come and go are counted only in the months they were active.",
		ValueLabel: "Contributors", Unit: "count", Noun: "author",
		Points: peopleSeries,
		Summary: fmt.Sprintf("Distinct monthly authors across %s, peaking at %s in %s.",
			only(int64(months), "month"), only(peopleMonth.Contributors, "author"), peopleMonth.Month),
		Note: singleMonth,
	}))

	if len(sizePoints) == 0 {
		p.NoCodeSize = true
		return p
	}
	caption := "Bytes of every non-vendored, non-binary tracked file at the sampled checkpoints — a wider set than the language bar, which counts only source files in a detected language. Checkpoints are dated by when the commit landed on " + branch + "."
	unit, noun := "bytes", ""
	if hasLines {
		caption = "Lines in every non-vendored, non-binary tracked file at the sampled checkpoints — a wider set than the language bar, which counts only source files in a detected language. Checkpoints are dated by when the commit landed on " + branch + "."
		unit, noun = "count", "line"
	}
	sizeAt := func(pt chartPoint) string {
		if hasLines {
			return only(pt.Value, "line")
		}
		return render.FormatBytes(pt.Value)
	}
	summary := fmt.Sprintf("%s: a single checkpoint of %s in %s.", sizeLabel, sizeAt(sizePoints[0]), sizePoints[0].Month)
	if len(sizePoints) > 1 {
		last := sizePoints[len(sizePoints)-1]
		summary = fmt.Sprintf("%s at %s, from %s in %s to %s in %s.",
			sizeLabel, only(int64(len(sizePoints)), "checkpoint"),
			sizeAt(sizePoints[0]), sizePoints[0].Month, sizeAt(last), last.Month)
	}
	p.Charts = append(p.Charts, newChart(chartSpec{
		ID: "chart-size", Kind: "area", Tone: "ice",
		Heading:    sizeLabel + " over time",
		Caption:    caption,
		ValueLabel: sizeLabel, Unit: unit, Noun: noun,
		Points:  sizePoints,
		Summary: summary,
		Note:    strings.Join(notes, " "),
	}))
	return p
}

// defaultBranchAuthors counts distinct author addresses ON THE DEFAULT BRANCH.
//
// Not repo.Contributors, which is every author on every ref: this page is scoped to one branch
// everywhere else — the header, the Commits tile, both series — so borrowing the all-refs total
// here put a number on the tile that the sub-line right beneath it flatly contradicted. The
// series cannot be summed to get it either, since a person active in three months would count
// three times, so it is recomputed from the same commit list ingest bucketed.
//
// repo.Commits, never CommitFor: an aggregate must respect the history-narrowing knobs.
func defaultBranchAuthors(repo *model.Repo) int {
	if repo.DefaultBranch == nil {
		return 0
	}
	seen := map[string]bool{}
	for _, br := range repo.Branches {
		if br.Name != *repo.DefaultBranch {
			continue
		}
		for _, sha := range br.Commits {
			if c, ok := repo.Commits[sha]; ok {
				if email := strings.ToLower(strings.TrimSpace(c.Author.Email)); email != "" {
					seen[email] = true
				}
			}
		}
		break
	}
	return len(seen)
}

// only is the page's "3 commits" / "1 month" phrasing.
func only(n int64, one string) string {
	if n == 1 {
		return render.FormatInt(n) + " " + one
	}
	return render.FormatInt(n) + " " + one + "s"
}

/* ---- one chart ----------------------------------------------------------- */

// chartPoint is one plotted month. Values are whole numbers in every series — commits, authors,
// lines, bytes — so they stay integers until the geometry needs them.
type chartPoint struct {
	Month string
	Value int64
}

// chartSpec is what the page asks for; chartView is the drawn result.
type chartSpec struct {
	ID   string
	Kind string // "bars" | "line" | "area"
	Tone string // "ember" | "ice"
	Heading,
	Caption,
	ValueLabel string
	Unit    string // "count" | "bytes"
	Noun    string // singular noun for count values; ignored for bytes
	Points  []chartPoint
	Summary string
	Note    string
}

type chartTick struct{ Y, TextY, Label string }
type chartBar struct{ X, Y, W, H, Title string }
type chartMark struct{ X, Y string }
type chartHit struct{ X, W, Title string }
type chartLabel struct{ X, Text string }
type chartRow struct{ Month, Value string }

// chartView is one figure, fully resolved: every coordinate is already a string, because a
// template is the wrong place to do arithmetic and this arithmetic is the component.
type chartView struct {
	ID, Kind, Tone               string
	Heading, Caption, ValueLabel string
	Summary, Note                string
	CapID, TableID               string
	Empty                        bool
	W, MaxW                      int
	ViewBox                      string
	// The fixed frame: the axis span, the baseline, the top of the plot area, its height, the
	// y of the month labels and the x the y-axis ticks are right-aligned at.
	AxisX1, AxisX2, BaseY, TickTop, InnerH, XLabelY, TickX int
	Ticks                                                  []chartTick
	Stroke, Fill                                           string
	AreaPath, LinePath                                     string
	Bars                                                   []chartBar
	Marks                                                  []chartMark
	Hits                                                   []chartHit
	Labels                                                 []chartLabel
	Rows                                                   []chartRow
}

// The chart's fixed geometry. A fixed viewBox drawn at (or below) natural size: the aspect is
// preserved and CSS pins the height, so a wide container never inflates the type and a narrow
// one scrolls the chart rather than the page.
const (
	chartPadLeft   = 62
	chartPadRight  = 18
	chartPadTop    = 20
	chartPadBottom = 40
	chartHeight    = 250
	chartInnerH    = chartHeight - chartPadTop - chartPadBottom
	chartBaseY     = chartPadTop + chartInnerH
	chartSlotPx    = 34
	chartMinWidth  = 640
)

func newChart(spec chartSpec) chartView {
	bytesUnit := spec.Unit == "bytes"
	v := chartView{
		ID: spec.ID, Kind: spec.Kind, Tone: spec.Tone,
		Heading: spec.Heading, Caption: spec.Caption, ValueLabel: spec.ValueLabel,
		Summary: spec.Summary, Note: spec.Note,
		CapID: spec.ID + "-cap", TableID: spec.ID + "-table",
		AxisX1: chartPadLeft, BaseY: chartBaseY, TickTop: chartPadTop,
		InnerH: chartInnerH, XLabelY: chartBaseY + 20, TickX: chartPadLeft - 8,
		Stroke: "var(--hf-" + spec.Tone + ")", Fill: "var(--hf-" + spec.Tone + "-soft)",
	}
	for _, p := range spec.Points {
		v.Rows = append(v.Rows, chartRow{Month: p.Month, Value: valueText(p.Value, bytesUnit, spec.Noun)})
	}
	n := len(spec.Points)
	if n == 0 {
		v.Empty = true
		return v
	}

	/* ---- scale: round the max up to a whole number of nice steps ---- */
	var maxValue int64
	for _, p := range spec.Points {
		if p.Value > maxValue {
			maxValue = p.Value
		}
	}
	step := niceStep(float64(maxValue), 4, bytesUnit)
	axisMax := math.Max(step, math.Ceil(float64(maxValue)/step)*step)

	/* ---- geometry: the x axis is a MONTH axis, not a list of points ----
	 * The commit and contributor series are gap-filled by ingest, so for them the two are the
	 * same thing. The code-size series is not — it only has a checkpoint in months that had a
	 * commit — so indexing by array position would draw a five-month gap and a one-month gap
	 * as the same step, and put the area chart's slope out of time scale with no way for the
	 * reader to tell. */
	firstSlot := monthSlot(spec.Points[0].Month)
	slotOf := func(i int) int { return monthSlot(spec.Points[i].Month) - firstSlot }
	slots := n
	if s := slotOf(n-1) + 1; s > slots {
		slots = s
	}
	width := chartPadLeft + chartPadRight + slots*chartSlotPx
	if width < chartMinWidth {
		width = chartMinWidth
	}
	innerW := float64(width - chartPadLeft - chartPadRight)
	slot := innerW / float64(slots)
	barW := math.Min(52, math.Max(3, slot*0.62))
	y := func(value float64) float64 {
		if axisMax == 0 {
			return chartBaseY
		}
		return chartBaseY - (value/axisMax)*chartInnerH
	}
	cx := func(i int) float64 { return chartPadLeft + slot*(float64(slotOf(i))+0.5) }

	v.W = width
	v.MaxW = int(math.Floor(float64(width)*1.2 + 0.5))
	v.AxisX2 = width - chartPadRight
	v.ViewBox = fmt.Sprintf("0 0 %d %d", width, chartHeight)

	for t := 0.0; t <= axisMax+step/1000; t += step {
		v.Ticks = append(v.Ticks, chartTick{
			Y: fixed2(y(t)), TextY: fixed2(y(t) + 4), Label: tickText(t, bytesUnit),
		})
	}

	/* ---- marks ---- */
	var line strings.Builder
	for i, p := range spec.Points {
		if i == 0 {
			line.WriteString("M")
		} else {
			line.WriteString(" L")
		}
		line.WriteString(fixed2(cx(i)))
		line.WriteByte(' ')
		line.WriteString(fixed2(y(float64(p.Value))))
	}
	if (spec.Kind == "line" || spec.Kind == "area") && n > 1 {
		v.LinePath = line.String()
		if spec.Kind == "area" {
			v.AreaPath = fmt.Sprintf("%s L%s %d L%s %d Z", v.LinePath, fixed2(cx(n-1)), chartBaseY, fixed2(cx(0)), chartBaseY)
		}
	}
	for i, p := range spec.Points {
		top := y(float64(p.Value))
		switch spec.Kind {
		case "bars":
			// A zero-height bar would be invisible; a month with commits gets 2px so the
			// reader can tell "a little" from "none".
			minHeight := 0.0
			if p.Value > 0 {
				minHeight = 2
			}
			v.Bars = append(v.Bars, chartBar{
				X: fixed2(cx(i) - barW/2), Y: fixed2(top),
				W: fixed2(barW), H: fixed2(math.Max(minHeight, chartBaseY-top)),
				Title: p.Month + ": " + valueText(p.Value, bytesUnit, spec.Noun),
			})
		case "line":
			v.Marks = append(v.Marks, chartMark{X: fixed2(cx(i) - 3.5), Y: fixed2(top - 3.5)})
		default:
			v.Marks = append(v.Marks, chartMark{X: fixed2(cx(i)), Y: fixed2(top)})
		}
		if spec.Kind != "bars" {
			// A 7px marker is a miserable thing to hit with a mouse, so the value's <title>
			// lives on a transparent full-height column over it instead — exactly one titled
			// element per point either way, since bars are big enough to carry their own.
			v.Hits = append(v.Hits, chartHit{
				X: fixed2(cx(i) - slot/2), W: fixed2(slot),
				Title: p.Month + ": " + valueText(p.Value, bytesUnit, spec.Noun),
			})
		}
	}

	/* ---- x labels: thinned until they stop colliding ----
	 * Distance is measured in SLOTS, not array positions, so a sparse series is thinned by how
	 * far apart its labels actually sit. The last month is always shown. */
	maxLabels := int(math.Floor(innerW / 66))
	if maxLabels < 2 {
		maxLabels = 2
	}
	every := int(math.Ceil(float64(slots) / float64(maxLabels)))
	if every < 1 {
		every = 1
	}
	labelled := make([]bool, n)
	lastSlot := math.Inf(-1)
	for i := 0; i < n; i++ {
		if float64(slotOf(i))-lastSlot < float64(every) {
			continue
		}
		lastSlot = float64(slotOf(i))
		labelled[i] = true
	}
	// Drop the penultimate tick if the always-on last one would sit on top of it.
	for i := 0; i < n-1; i++ {
		if labelled[i] && float64(slotOf(n-1)-slotOf(i)) < float64(every)/2 {
			labelled[i] = false
		}
	}
	labelled[n-1] = true
	for i, p := range spec.Points {
		if labelled[i] {
			v.Labels = append(v.Labels, chartLabel{X: fixed2(cx(i)), Text: monthLabel(p.Month)})
		}
	}
	return v
}

// niceStep rounds an axis top up to a whole number of readable steps: decimal steps for counts,
// 1024-based ones for bytes, so a KB axis lands on KB boundaries.
func niceStep(maxValue, target float64, bytesUnit bool) float64 {
	if maxValue <= 0 {
		return 1
	}
	raw := maxValue / target
	if bytesUnit {
		base := 1.0
		for base*1024 <= raw {
			base *= 1024
		}
		for _, m := range []float64{1, 2, 5, 10, 20, 50, 100, 200, 500} {
			if raw <= m*base {
				return m * base
			}
		}
		return 1024 * base
	}
	base := math.Pow(10, math.Floor(math.Log10(raw)))
	for _, m := range []float64{1, 2, 2.5, 3, 4, 5} {
		if raw <= m*base {
			return math.Max(1, math.Ceil(m*base))
		}
	}
	return math.Max(1, math.Ceil(10*base))
}

func valueText(value int64, bytesUnit bool, noun string) string {
	if bytesUnit {
		return render.FormatBytes(value)
	}
	if noun != "" {
		return only(value, noun)
	}
	return render.FormatInt(value)
}

// tickText is the short form for a y-axis tick, where there is no room for a unit word.
func tickText(value float64, bytesUnit bool) string {
	n := int64(math.Round(value))
	if bytesUnit {
		return render.FormatBytes(n)
	}
	return render.FormatInt(n)
}

// monthSlot turns YYYY-MM into an absolute month number, so a gap in a series is a gap on the
// axis. A month string the artifact could not have produced lands on slot 0 rather than
// aborting a build over a label.
func monthSlot(month string) int {
	if len(month) < 7 {
		return 0
	}
	year, err1 := strconv.Atoi(month[:4])
	m, err2 := strconv.Atoi(month[5:7])
	if err1 != nil || err2 != nil {
		return 0
	}
	return year*12 + (m - 1)
}

var shortMonths = [...]string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}

// monthLabel renders "Aug 2026". Static month names, never toLocaleDateString: this string is
// baked into the HTML, so it must not depend on the build machine's locale data.
func monthLabel(month string) string {
	if len(month) < 7 {
		return month
	}
	if m, err := strconv.Atoi(month[5:7]); err == nil && m >= 1 && m <= 12 {
		return shortMonths[m-1] + " " + month[:4]
	}
	return month[5:] + " " + month[:4]
}

/* ---- coordinates --------------------------------------------------------- */

func fixed2(v float64) string { return fixed(v, 2) }

// fixed formats a coordinate exactly the way JavaScript's Number.prototype.toFixed does.
//
// One implementation, in render, because FormatBytes needs the same rule: Go's %.1f rounds an
// exact half to even and toFixed rounds the magnitude up. Chart arithmetic and file sizes both
// land on exact halves (slot widths and byte counts are powers of two often enough), and one
// differing digit is a diff in the built site that nobody can explain.
func fixed(v float64, digits int) string { return render.ToFixed(v, digits) }
