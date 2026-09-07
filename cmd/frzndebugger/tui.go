package main

// The event loop.
//
// It takes an io.Reader, an io.Writer and a function that says how big the screen is — not a
// terminal. Everything that needs a console lives in term_windows.go / term_other.go and is
// arranged by the caller, which means the whole interface can be driven by a test with a
// scripted string of keystrokes and no tty anywhere. That was the design constraint: a TUI
// nobody can test is a TUI that rots.

import (
	"io"
	"strconv"
	"strings"
	"time"

	"frznforge/internal/timings"
)

const (
	// defaultCols and defaultRows are what a screen of unknown size is assumed to be. Both
	// platform sizers fall back to these, so they are also what a redirected stdout gets.
	defaultCols = 100
	defaultRows = 30
	// chromeRows is the furniture around the scrolling body: title, the unfinished banner,
	// the view's context line and a column header at the top; the detail line and the key help
	// at the bottom.
	chromeRows = 6
)

type viewID int

const (
	viewUnfinished viewID = iota
	viewTimings
	viewLog
	viewCount
)

type ui struct {
	out    io.Writer
	size   func() (int, int)
	reload func() Data
	after  timer

	data       Data
	runs       []timings.Run
	run        string
	tree       []Node
	nodes      map[string]Node
	unfinished []Unfinished
	truncated  int
	logged     []LogRecord

	view     viewID
	sortBy   SortKey
	sortDesc bool
	open     map[string]bool
	filter   LogFilter

	sel [viewCount]int
	top [viewCount]int

	// gen changes whenever anything that would change a row changes. Together with the view and
	// the width it keys the row cache below, which exists because a debug-level run log is tens
	// of thousands of records and rebuilding every row on every keypress — twice, once to count
	// them and once to draw them — is how a viewer of a slow build becomes the slow part.
	gen      int
	cacheKey string
	cache    []Row

	// typing is the "/" filter prompt. While it is on, every printable key is text rather than a
	// command — otherwise a search for "log" would switch views twice on the way in.
	typing bool
	buffer string

	status string
	quit   bool
}

// uiOptions is what the command line decided before the screen existed.
type uiOptions struct {
	Data   Data
	Filter LogFilter
	Run    string
	// Reload re-reads both files. Nil disables the r key, which is what a test wants.
	Reload func() Data
	// After is the escape-gap clock; nil uses the real one.
	After timer
}

func realTimer(ms int) <-chan struct{} {
	ch := make(chan struct{})
	time.AfterFunc(time.Duration(ms)*time.Millisecond, func() { close(ch) })
	return ch
}

// runUI draws and reads keys until the user quits or the input ends.
//
// It returns nil on a clean exit including "the input ended", because a piped stdin closing is
// how a non-interactive invocation of the interactive path finishes, not a failure.
func runUI(in io.Reader, out io.Writer, size func() (int, int), opts uiOptions) error {
	u := &ui{
		out:    out,
		size:   size,
		reload: opts.Reload,
		after:  opts.After,
		data:   opts.Data,
		filter: opts.Filter,
		run:    opts.Run,
		open:   map[string]bool{},
		// Slowest first, which is the question the timings file answers. It is also what
		// timings.Aggregate already sorted by, so the first screen matches the package's own order.
		sortDesc: true,
	}
	if u.after == nil {
		u.after = realTimer
	}
	if u.size == nil {
		u.size = func() (int, int) { return defaultCols, defaultRows }
	}
	u.recompute()

	// Open on the unfinished view when there is one to open on. The reason this program exists is
	// a run that stopped, and making someone press a key to find that out would bury the answer
	// under the table it is the explanation for.
	if len(u.unfinished) > 0 || u.truncated > 0 {
		u.view = viewUnfinished
		u.status = "a step started and never finished -- 2 for timings, 3 for the log"
	} else {
		u.view = viewTimings
		u.status = "every step that started also finished"
	}

	src := newByteSource(in)
	defer src.Close()

	if _, err := io.WriteString(out, ansiAltEnter+ansiHideCursor); err != nil {
		return err
	}
	// The restore is deferred rather than run at the end of the loop, so a panic inside the loop
	// still leaves the user with a working shell. main's recover prints the panic afterwards.
	defer io.WriteString(out, ansiShowCursor+ansiAltExit)

	for !u.quit {
		if err := u.draw(); err != nil {
			return err
		}
		key, ok := src.next(u.after)
		if !ok {
			return nil
		}
		u.handle(key)
	}
	return nil
}

/* ---- state --------------------------------------------------------------- */

func (u *ui) recompute() {
	u.gen++
	u.runs = timings.Runs(u.data.Records)
	scoped, run := Scope(u.data.Records, u.run)
	u.run = run
	u.tree = BuildTree(scoped)
	SortNodes(u.tree, u.sortBy, u.sortDesc)
	u.nodes = map[string]Node{}
	indexNodes(u.tree, u.nodes)
	u.unfinished, u.truncated = UnfinishedsUntil(scoped, LastLogTime(u.data.Log, run))
	u.logged = FilterLog(u.data.Log, u.filter)
}

func indexNodes(nodes []Node, into map[string]Node) {
	for _, n := range nodes {
		into[n.Key] = n
		indexNodes(n.Children, into)
	}
}

// rows is the current view's whole body, laid out for this width and cached.
//
// The log view is deliberately NOT built through here — see count and window. Its row list is as
// long as the file, and the only rows worth formatting are the twenty on screen.
func (u *ui) rows(width int) []Row {
	key := strconv.Itoa(int(u.view)) + ":" + strconv.Itoa(width) + ":" + strconv.Itoa(u.gen)
	if key == u.cacheKey && u.cache != nil {
		return u.cache
	}
	switch u.view {
	case viewUnfinished:
		u.cache = UnfinishedRows(u.unfinished, u.truncated, width)
	case viewLog:
		u.cache = LogRows(u.logged, width)
	default:
		u.cache = TimingsRows(u.tree, u.open, width)
	}
	u.cacheKey = key
	return u.cache
}

// count is how many rows the current view has, without formatting any of them.
func (u *ui) count(width int) int {
	if u.view == viewLog {
		return len(u.logged)
	}
	return len(u.rows(width))
}

// window formats only the rows that will actually be on screen.
func (u *ui) window(top, height, width int) []Row {
	if u.view != viewLog {
		rows := u.rows(width)
		return rows[min(top, len(rows)):min(top+height, len(rows))]
	}
	lo := min(top, len(u.logged))
	return LogRows(u.logged[lo:min(top+height, len(u.logged))], width)
}

/* ---- drawing ------------------------------------------------------------- */

func (u *ui) draw() error {
	w, h := u.size()
	if w < 20 {
		w = 20
	}
	if h < chromeRows+1 {
		h = chromeRows + 1
	}
	bodyHeight := h - chromeRows
	u.clamp(u.count(w), bodyHeight)
	sel, top := u.sel[u.view], u.top[u.view]
	body := u.window(top, bodyHeight, w)

	var b strings.Builder
	b.Grow(w * h)
	b.WriteString(ansiHome)

	line := func(row int, text string, style Style, selected bool) {
		b.WriteString(moveTo(row, 1))
		b.WriteString(ansiClearLine)
		if selected {
			b.WriteString(ansiSelected)
			b.WriteString(pad(text, w))
		} else {
			b.WriteString(styleCode(style))
			b.WriteString(clip(text, w))
		}
		b.WriteString(ansiReset)
	}

	line(1, u.titleBar(), StyleHead, false)
	summary, style := UnfinishedSummary(u.unfinished, u.truncated)
	line(2, summary, style, false)
	line(3, u.contextLine(), StyleDim, false)
	if u.view == viewTimings {
		header := TimingsHeader(u.sortBy, u.sortDesc, w)
		line(4, header.Text, header.Style, false)
	} else {
		line(4, "", StylePlain, false)
	}

	for i := 0; i < bodyHeight; i++ {
		row := 5 + i
		if i >= len(body) {
			line(row, "", StylePlain, false)
			continue
		}
		line(row, body[i].Text, body[i].Style, top+i == sel && body[i].Selectable)
	}

	line(h-1, u.detailLine(w, sel), StyleAccent, false)
	line(h, u.helpLine(), StyleDim, false)

	_, err := io.WriteString(u.out, b.String())
	return err
}

// titleBar puts the tabs before the directory, because the directory is the part that can be a
// hundred characters of temp path and the tabs are the part that says where you are.
func (u *ui) titleBar() string {
	tabs := []string{"1 unfinished", "2 timings", "3 log"}
	for i := range tabs {
		if viewID(i) == u.view {
			tabs[i] = "[" + tabs[i] + "]"
		} else {
			tabs[i] = " " + tabs[i] + " "
		}
	}
	return strings.Join(tabs, " ") + "   " + u.data.Dir
}

func (u *ui) contextLine() string {
	switch u.view {
	case viewLog:
		line := "log: " + strconv.Itoa(len(u.logged)) + " of " + strconv.Itoa(len(u.data.Log)) + " records"
		if u.filter.HasLevel {
			line += "  level>=" + u.filter.MinLevel.String()
		}
		if u.filter.Text != "" {
			line += "  match:" + u.filter.Text
		}
		if u.data.LogErr != nil {
			line += "  (" + u.data.LogErr.Error() + ")"
		}
		if len(u.data.Log) == 0 && u.data.LogErr == nil {
			line += "  -- no log at " + u.data.LogPath
		}
		return line
	case viewUnfinished:
		return "the steps with a start and no finish, newest run first"
	default:
		line := RunLine(u.runs, u.run)
		if u.data.TimingsErr != nil {
			line += "  (" + u.data.TimingsErr.Error() + ")"
		}
		if len(u.data.Records) == 0 && u.data.TimingsErr == nil {
			line += "  -- no timings at " + u.data.TimingsPath
		}
		return line
	}
}

// detailLine is the line under the table: what is being typed, what just happened, or everything
// about the selected row that did not fit on it.
func (u *ui) detailLine(width, sel int) string {
	if u.typing {
		return "/" + u.buffer + "_   (enter to apply, esc to cancel)"
	}
	if u.status != "" {
		return u.status
	}
	if sel < 0 {
		return ""
	}
	switch u.view {
	case viewTimings:
		rows := u.rows(width)
		if sel < len(rows) {
			if n, ok := u.nodes[rows[sel].Key]; ok {
				return NodeDetail(n)
			}
		}
	case viewLog:
		if sel < len(u.logged) {
			// The whole record, including whatever the row was clipped at: a log line's answer is
			// usually in the attribute that fell off the right-hand edge.
			return u.logged[sel].Raw
		}
	}
	return ""
}

func (u *ui) helpLine() string {
	common := "  tab/1/2/3 view  r reload  q quit"
	switch u.view {
	case viewLog:
		return "j/k move  g/G top/end  l level  / find  c clear" + common
	case viewTimings:
		return "j/k move  enter open  o sort  O reverse  [ ] run  a all runs" + common
	default:
		return "j/k move  g/G top/end" + common
	}
}

// clamp keeps the selection inside the rows that exist and drags the viewport after it.
func (u *ui) clamp(count, height int) {
	if count == 0 {
		u.sel[u.view], u.top[u.view] = 0, 0
		return
	}
	if u.sel[u.view] >= count {
		u.sel[u.view] = count - 1
	}
	if u.sel[u.view] < 0 {
		u.sel[u.view] = 0
	}
	if height < 1 {
		height = 1
	}
	if u.sel[u.view] < u.top[u.view] {
		u.top[u.view] = u.sel[u.view]
	}
	if u.sel[u.view] >= u.top[u.view]+height {
		u.top[u.view] = u.sel[u.view] - height + 1
	}
	if max := count - height; u.top[u.view] > max {
		u.top[u.view] = max
	}
	if u.top[u.view] < 0 {
		u.top[u.view] = 0
	}
}

/* ---- keys ---------------------------------------------------------------- */

func (u *ui) handle(k Key) {
	if u.typing {
		u.handleTyping(k)
		return
	}
	u.status = ""

	w, h := u.size()
	page := h - chromeRows
	if page < 1 {
		page = 1
	}
	count := u.count(w)

	switch k.Code {
	case KeyInterrupt:
		u.quit = true
		return
	case KeyUp:
		u.move(-1, count)
		return
	case KeyDown:
		u.move(1, count)
		return
	case KeyPageUp:
		u.move(-page, count)
		return
	case KeyPageDown:
		u.move(page, count)
		return
	case KeyHome:
		u.jump(0, count)
		return
	case KeyEnd:
		u.jump(count-1, count)
		return
	case KeyTab:
		u.view = (u.view + 1) % viewCount
		return
	case KeyLeft:
		u.toggleNode(false)
		return
	case KeyRight, KeyEnter:
		u.toggleNode(true)
		return
	case KeyEscape:
		return
	}
	if k.Code != KeyRune {
		return
	}

	switch k.Rune {
	case 'q':
		u.quit = true
	case '1':
		u.view = viewUnfinished
	case '2':
		u.view = viewTimings
	case '3':
		u.view = viewLog
	case 'j':
		u.move(1, count)
	case 'k':
		u.move(-1, count)
	case 'g':
		u.jump(0, count)
	case 'G':
		// The interesting end of a failed run. In the log view this is literally the requirement
		// the view exists for: the last record before it stopped.
		u.jump(count-1, count)
	case ' ':
		u.toggleNode(true)
	case 'r':
		if u.reload != nil {
			u.data = u.reload()
			u.recompute()
			u.status = "reloaded"
		}
	case '/':
		u.typing, u.buffer = true, u.filter.Text
	case 'c':
		u.filter = LogFilter{}
		u.recompute()
		u.status = "filters cleared"
	case 'l':
		u.cycleLevel()
	case 'o':
		u.sortBy = (u.sortBy + 1) % SortKey(len(sortKeyNames))
		u.applySort()
	case 'O':
		u.sortDesc = !u.sortDesc
		u.applySort()
	case 't':
		u.setSort(SortTotal)
	case 'm':
		u.setSort(SortMean)
	case 'n':
		u.setSort(SortName)
	case 'b':
		u.setSort(SortBest)
	case 'w':
		u.setSort(SortWorst)
	case 'f':
		u.setSort(SortFailed)
	case '[':
		u.stepRun(-1)
	case ']':
		u.stepRun(1)
	case 'a':
		u.run = AllRuns
		u.recompute()
	}
}

func (u *ui) handleTyping(k Key) {
	switch k.Code {
	case KeyEnter:
		u.typing = false
		u.filter.Text = u.buffer
		u.recompute()
		u.view = viewLog
	case KeyEscape:
		u.typing, u.buffer = false, ""
	case KeyInterrupt:
		// Ctrl-C quits from everywhere, the filter prompt included. A key documented as "quit"
		// that does something else in one mode is a key nobody trusts.
		u.quit = true
	case KeyBackspace:
		if r := []rune(u.buffer); len(r) > 0 {
			u.buffer = string(r[:len(r)-1])
		}
	case KeyRune:
		u.buffer += string(k.Rune)
	}
}

func (u *ui) move(delta, count int) {
	u.jump(u.sel[u.view]+delta, count)
}

func (u *ui) jump(to, count int) {
	if count == 0 {
		return
	}
	if to < 0 {
		to = 0
	}
	if to >= count {
		to = count - 1
	}
	u.sel[u.view] = to
}

func (u *ui) setSort(k SortKey) {
	if u.sortBy == k {
		// Pressing the same column again reverses it, which is what a table header does
		// everywhere else and is one fewer key to remember.
		u.sortDesc = !u.sortDesc
	} else {
		u.sortBy = k
		// A duration column reads longest-first and a name column reads A-first. Choosing the
		// useful direction on the first press means the second press is the rare one.
		u.sortDesc = k != SortName && k != SortKind
	}
	u.applySort()
}

func (u *ui) applySort() {
	u.gen++
	SortNodes(u.tree, u.sortBy, u.sortDesc)
	u.view = viewTimings
	u.status = "sorted by " + u.sortBy.String() + direction(u.sortDesc)
}

func direction(desc bool) string {
	if desc {
		return ", largest first"
	}
	return ", smallest first"
}

func (u *ui) toggleNode(open bool) {
	if u.view != viewTimings {
		return
	}
	w, _ := u.size()
	rows := u.rows(w)
	sel := u.sel[u.view]
	if sel < 0 || sel >= len(rows) || rows[sel].Key == "" {
		return
	}
	if !rows[sel].HasChildren {
		return
	}
	u.gen++
	if open && !rows[sel].Open {
		u.open[rows[sel].Key] = true
		return
	}
	// Right on an already-open node and Left on any node both collapse: with one key doing both
	// there is nothing to remember, and the marker in the row says which state it is in.
	delete(u.open, rows[sel].Key)
}

func (u *ui) cycleLevel() {
	current := ""
	if u.filter.HasLevel {
		current = strings.ToLower(u.filter.MinLevel.String())
	}
	next := LevelNames[0]
	for i, name := range LevelNames {
		if name == current {
			next = LevelNames[(i+1)%len(LevelNames)]
			break
		}
	}
	u.filter.MinLevel, u.filter.HasLevel = ParseLevel(next)
	u.recompute()
	u.view = viewLog
	if u.filter.HasLevel {
		u.status = "showing " + u.filter.MinLevel.String() + " and above"
	} else {
		u.status = "showing every level"
	}
}

func (u *ui) stepRun(delta int) {
	if len(u.runs) == 0 {
		return
	}
	at := len(u.runs) - 1
	for i, r := range u.runs {
		if r.ID == u.run {
			at = i
			break
		}
	}
	at += delta
	if at < 0 {
		at = 0
	}
	if at >= len(u.runs) {
		at = len(u.runs) - 1
	}
	u.run = u.runs[at].ID
	u.recompute()
	u.view = viewTimings
}
