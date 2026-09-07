package main

import (
	"bytes"
	"strings"
	"testing"
)

// The event loop takes a reader, a writer and a size function rather than a terminal, which is
// what makes these tests possible: everything below drives the real UI with a scripted string of
// keystrokes and reads the frames back out of a buffer. The only part not covered here is
// putting the console into raw mode, which is four kernel32 calls in term_windows.go.

// drive runs the UI over a fixed screen and returns every frame it drew.
func drive(t *testing.T, keys string, opts uiOptions) []string {
	t.Helper()
	var out bytes.Buffer
	if err := runUI(strings.NewReader(keys), &out, func() (int, int) { return 120, 14 }, opts); err != nil {
		t.Fatalf("runUI: %v", err)
	}
	return frames(out.String())
}

// frames splits the output on the cursor-home each redraw starts with.
func frames(out string) []string {
	parts := strings.Split(out, ansiHome)
	if len(parts) > 1 {
		return parts[1:]
	}
	return parts
}

func fixtureUI(t *testing.T) uiOptions {
	t.Helper()
	return uiOptions{Data: Load(fixtureDir(t))}
}

func TestUIOpensOnTheUnfinishedStepAndQuitsOnQ(t *testing.T) {
	got := drive(t, "q", fixtureUI(t))
	if len(got) != 1 {
		t.Fatalf("got %d frames, want one before the q", len(got))
	}
	first := got[0]
	if !strings.Contains(first, "1 UNFINISHED step") {
		t.Errorf("the first screen must lead with the step that never came back:\n%s", first)
	}
	if !strings.Contains(first, "[1 unfinished]") {
		t.Error("the unfinished tab should be the selected one when there is something in it")
	}
	if !strings.Contains(first, "#3") {
		t.Error("the missing id belongs on the first screen")
	}
}

func TestUIRestoresTheScreenOnTheWayOut(t *testing.T) {
	var out bytes.Buffer
	if err := runUI(strings.NewReader("q"), &out, nil, uiOptions{}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.HasPrefix(text, ansiAltEnter+ansiHideCursor) {
		t.Error("the alternate screen must be entered before anything is drawn")
	}
	if !strings.HasSuffix(text, ansiShowCursor+ansiAltExit) {
		t.Error("the cursor and the user's scrollback must come back")
	}
}

func TestUIQuitsOnCtrlCAndOnTheInputEnding(t *testing.T) {
	if got := drive(t, "\x03", fixtureUI(t)); len(got) != 1 {
		t.Errorf("ctrl-c did not quit after the first frame (%d frames)", len(got))
	}
	// An empty reader is an input that has already ended, which is how a closed terminal stops
	// the loop rather than spinning it.
	if got := drive(t, "", fixtureUI(t)); len(got) != 1 {
		t.Errorf("an ended input did not stop the loop (%d frames)", len(got))
	}
}

func TestUISwitchesViews(t *testing.T) {
	got := drive(t, "2q", fixtureUI(t))
	if !strings.Contains(last(got), "[2 timings]") || !strings.Contains(last(got), "build site") {
		t.Errorf("2 did not reach the timings view:\n%s", last(got))
	}
	got = drive(t, "3q", fixtureUI(t))
	if !strings.Contains(last(got), "clone failed") {
		t.Errorf("3 did not reach the log view:\n%s", last(got))
	}
	// Tab cycles: unfinished -> timings.
	got = drive(t, "\tq", fixtureUI(t))
	if !strings.Contains(last(got), "[2 timings]") {
		t.Errorf("tab did not advance the view:\n%s", last(got))
	}
}

func TestUIOpensAStepToShowWhatItContained(t *testing.T) {
	closed := drive(t, "2q", fixtureUI(t))
	if strings.Contains(last(closed), "ingest.repo") {
		t.Fatal("children should be hidden until the row is opened")
	}
	opened := drive(t, "2\rq", fixtureUI(t))
	if !strings.Contains(last(opened), "ingest.repo kieran/alpha") {
		t.Errorf("enter did not open the step:\n%s", last(opened))
	}
}

func TestUISortsByEveryColumn(t *testing.T) {
	// A duration column opens largest-first and a name column A-first, so the second press is
	// the rare one.
	for key, want := range map[string]string{
		"n": "step^", "m": "meanv", "b": "bestv", "w": "worstv", "f": "failv",
	} {
		got := drive(t, "2"+key+"q", fixtureUI(t))
		if !strings.Contains(last(got), want) {
			t.Errorf("key %q did not mark %q in the header:\n%s", key, want, last(got))
		}
	}
	// The same key again reverses, the way a table header does everywhere else. Total is the
	// column the screen opens on, so one press of t is already the second press.
	if got := drive(t, "2tq", fixtureUI(t)); !strings.Contains(last(got), "total^") {
		t.Errorf("t on the already-sorted column did not reverse it:\n%s", last(got))
	}
	if got := drive(t, "2nnq", fixtureUI(t)); !strings.Contains(last(got), "stepv") {
		t.Errorf("pressing the sort key twice did not reverse it:\n%s", last(got))
	}
	// The default is the order timings.Aggregate itself produces: slowest first.
	if got := drive(t, "2q", fixtureUI(t)); !strings.Contains(last(got), "totalv") {
		t.Errorf("the timings view should open slowest-first:\n%s", last(got))
	}
}

func TestUIFiltersTheLogByText(t *testing.T) {
	got := drive(t, "/beta\rq", fixtureUI(t))
	final := last(got)
	if !strings.Contains(final, "cache miss") {
		t.Errorf("the matching record is missing:\n%s", final)
	}
	if strings.Contains(final, "git start") {
		t.Errorf("a non-matching record survived the filter:\n%s", final)
	}
	if !strings.Contains(final, "match:beta") {
		t.Errorf("the context line should say what is being filtered on:\n%s", final)
	}
}

func TestUICancelsAFilterOnEscape(t *testing.T) {
	got := drive(t, "3/beta\x1bq", fixtureUI(t))
	if !strings.Contains(last(got), "git start") {
		t.Errorf("escape must abandon the filter rather than apply it:\n%s", last(got))
	}
}

func TestUICyclesTheLevelFilter(t *testing.T) {
	// "" -> debug -> info: info and above drops the DEBUG record and keeps the rest.
	got := drive(t, "llq", fixtureUI(t))
	final := last(got)
	if strings.Contains(final, "git start") {
		t.Errorf("the DEBUG record should be hidden at level info:\n%s", final)
	}
	if !strings.Contains(final, "ingest finished") || !strings.Contains(final, "???") {
		t.Errorf("info and above, plus the unreadable line, should remain:\n%s", final)
	}
}

func TestUIClearsFilters(t *testing.T) {
	got := drive(t, "3/beta\rcq", fixtureUI(t))
	if !strings.Contains(last(got), "git start") {
		t.Errorf("c did not clear the filter:\n%s", last(got))
	}
}

func TestUIJumpsToTheLastRecord(t *testing.T) {
	// A 14-row screen leaves 8 body rows for 5 records, so make the log longer than the window.
	data := Load(fixtureDir(t))
	for i := 0; i < 40; i++ {
		data.Log = append(data.Log, ParseLog("time=x level=INFO msg=filler-"+string(rune('a'+i%26))+"\n")...)
	}
	data.Log = append(data.Log, ParseLog("time=x level=ERROR msg=the-very-last-line\n")...)

	top := drive(t, "3q", uiOptions{Data: data})
	if strings.Contains(last(top), "the-very-last-line") {
		t.Fatal("the end of a long log should be below the fold before G is pressed")
	}
	end := drive(t, "3Gq", uiOptions{Data: data})
	if !strings.Contains(last(end), "the-very-last-line") {
		t.Errorf("G did not reach the last record:\n%s", last(end))
	}
}

func TestUIWalksBetweenRuns(t *testing.T) {
	opts := fixtureUI(t)
	got := drive(t, "2[q", opts)
	if !strings.Contains(last(got), "20260905T101010Z-100") {
		t.Errorf("[ did not step back a run:\n%s", last(got))
	}
	got = drive(t, "2aq", opts)
	if !strings.Contains(last(got), "run: all (2 in file)") {
		t.Errorf("a did not widen to every run:\n%s", last(got))
	}
}

func TestUIReloadsFromDisk(t *testing.T) {
	calls := 0
	opts := fixtureUI(t)
	opts.Reload = func() Data {
		calls++
		return opts.Data
	}
	drive(t, "rq", opts)
	if calls != 1 {
		t.Errorf("r read the files %d times, want 1", calls)
	}
}

func TestUISaysWhenThereIsNothingToShow(t *testing.T) {
	got := drive(t, "q", uiOptions{Data: Load(t.TempDir())})
	first := got[0]
	if !strings.Contains(first, "no unfinished steps") {
		t.Errorf("an empty directory is a clean run, not a broken one:\n%s", first)
	}
	got = drive(t, "3q", uiOptions{Data: Load(t.TempDir())})
	if !strings.Contains(last(got), "no log at") {
		t.Errorf("the log view should name the file it did not find:\n%s", last(got))
	}
}

func TestUIDrawsIntoAWindowTooSmallToBeReasonable(t *testing.T) {
	var out bytes.Buffer
	err := runUI(strings.NewReader("2\rq"), &out, func() (int, int) { return 5, 2 }, uiOptions{
		Data: Load(fixtureDir(t)),
	})
	if err != nil {
		t.Fatalf("a tiny window must not be an error: %v", err)
	}
}

/* ---- the key decoder ----------------------------------------------------- */

func TestKeyDecoderReadsEscapeSequences(t *testing.T) {
	src := newByteSource(strings.NewReader("\x1b[A\x1b[B\x1b[5~\x1b[6~\x1b[H\x1b[F\x1b[1;5D\x1bOC"))
	defer src.Close()
	want := []KeyCode{KeyUp, KeyDown, KeyPageUp, KeyPageDown, KeyHome, KeyEnd, KeyLeft, KeyRight}
	for i, code := range want {
		key, ok := src.next(realTimer)
		if !ok {
			t.Fatalf("input ended at %d", i)
		}
		if key.Code != code {
			t.Errorf("key %d = %v, want %v", i, key.Code, code)
		}
	}
}

func TestKeyDecoderReadsPlainKeys(t *testing.T) {
	src := newByteSource(strings.NewReader("q\r\t\x7f\x03"))
	defer src.Close()
	want := []Key{
		{Code: KeyRune, Rune: 'q'}, {Code: KeyEnter}, {Code: KeyTab},
		{Code: KeyBackspace}, {Code: KeyInterrupt},
	}
	for i, w := range want {
		got, ok := src.next(realTimer)
		if !ok {
			t.Fatalf("input ended at %d", i)
		}
		if got != w {
			t.Errorf("key %d = %+v, want %+v", i, got, w)
		}
	}
	if _, ok := src.next(realTimer); ok {
		t.Error("the decoder must report the end of the input")
	}
}

func TestKeyDecoderReadsAMultiByteRune(t *testing.T) {
	src := newByteSource(strings.NewReader("naïve"))
	defer src.Close()
	var got []rune
	for {
		key, ok := src.next(realTimer)
		if !ok {
			break
		}
		got = append(got, key.Rune)
	}
	if string(got) != "naïve" {
		t.Errorf("decoded %q; a filter typed with an accented repository name has to work", string(got))
	}
}

func TestKeyDecoderTreatsALoneEscapeAsEscape(t *testing.T) {
	src := newByteSource(strings.NewReader("\x1b"))
	defer src.Close()
	key, ok := src.next(realTimer)
	if !ok || key.Code != KeyEscape {
		t.Errorf("lone ESC = %+v/%v", key, ok)
	}
}

func last(frames []string) string {
	if len(frames) == 0 {
		return ""
	}
	return frames[len(frames)-1]
}
