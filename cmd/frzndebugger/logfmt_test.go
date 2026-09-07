package main

import (
	"log/slog"
	"strings"
	"testing"
)

func TestParseLogReadsWhatTheHandlerWrites(t *testing.T) {
	records := ParseLog(sampleLog)
	if len(records) != 5 {
		t.Fatalf("got %d records, want 5", len(records))
	}

	first := records[0]
	if first.Level != "DEBUG" || first.LevelValue != int(slog.LevelDebug) {
		t.Errorf("level = %q/%d, want DEBUG/%d", first.Level, first.LevelValue, slog.LevelDebug)
	}
	if first.Msg != "git start" {
		t.Errorf("msg = %q, want %q -- a quoted value must survive the space inside it", first.Msg, "git start")
	}
	if first.Time != "2026-09-06T22:49:03.512-07:00" {
		t.Errorf("time = %q; the stamp is kept verbatim so the UTC offset is not lost", first.Time)
	}
	if len(first.Attrs) != 2 || first.Attrs[0].Key != "args" || first.Attrs[0].Value != "rev-parse HEAD" {
		t.Errorf("attrs = %+v, want args and repo in the order written", first.Attrs)
	}
	if first.Unparsed {
		t.Error("a well-formed record must not be marked unparsed")
	}
}

func TestParseLogKeepsALineItCannotRead(t *testing.T) {
	records := ParseLog(sampleLog)
	last := records[len(records)-1]
	if !last.Unparsed {
		t.Fatal("a line with no level= must be marked unparsed, not silently reinterpreted")
	}
	if last.Msg != "this line is not a record at all" || last.Raw != last.Msg {
		t.Errorf("an unparsed line keeps its whole text: msg=%q raw=%q", last.Msg, last.Raw)
	}
	if !last.Warning() {
		t.Error("an unreadable line deserves the eye as much as a WARN does")
	}
}

func TestParseLogScrubsACredentialTheWriterMissed(t *testing.T) {
	for _, r := range ParseLog(sampleLog) {
		if strings.Contains(r.Raw, "ghs_supersecretvalue") {
			t.Fatalf("a credential survived into a record: %s", r.Raw)
		}
	}
	errRecord := ParseLog(sampleLog)[3]
	if !strings.Contains(errRecord.Attrs[0].Value, "***@example.com") {
		t.Errorf("url attr = %q, want the userinfo replaced", errRecord.Attrs[0].Value)
	}
}

func TestSplitLogfmtHandlesEscapesAndEquals(t *testing.T) {
	pairs := splitLogfmt(`level=INFO msg="a \"quoted\" thing = here" n=3`)
	if len(pairs) != 3 {
		t.Fatalf("got %d pairs: %+v", len(pairs), pairs)
	}
	if pairs[1].Value != `a "quoted" thing = here` {
		t.Errorf("value = %q; an escaped quote and an inner = must not end the value", pairs[1].Value)
	}
	if pairs[2].Key != "n" || pairs[2].Value != "3" {
		t.Errorf("trailing pair = %+v", pairs[2])
	}
}

func TestParseLogReadsALevelWithAnOffset(t *testing.T) {
	records := ParseLog("time=x level=ERROR+2 msg=boom\n")
	if records[0].Unparsed {
		t.Fatal("ERROR+2 is a level slog itself writes")
	}
	if records[0].LevelValue != int(slog.LevelError)+2 {
		t.Errorf("level value = %d, want %d", records[0].LevelValue, int(slog.LevelError)+2)
	}
	if got := shortLevel(records[0].Level); got != "ERROR" {
		t.Errorf("shortLevel = %q, want ERROR -- the part that says how alarmed to be", got)
	}
}

func TestFilterLogByLevelKeepsTheUnreadableLine(t *testing.T) {
	records := ParseLog(sampleLog)
	got := FilterLog(records, LogFilter{MinLevel: slog.LevelWarn, HasLevel: true})
	if len(got) != 3 {
		t.Fatalf("got %d records, want WARN, ERROR and the unparsed line", len(got))
	}
	if !got[2].Unparsed {
		t.Error("a line with no level must survive a level filter: it is the torn tail of a killed run")
	}
}

func TestFilterLogBySubstringSearchesTheWholeLine(t *testing.T) {
	records := ParseLog(sampleLog)
	// "beta" only ever appears in an attribute value, never in a message.
	got := FilterLog(records, LogFilter{Text: "BETA"})
	if len(got) != 1 || got[0].Msg != "cache miss" {
		t.Fatalf("got %+v, want the one record whose attribute mentions beta (case-insensitively)", got)
	}
}

func TestFilterLogWithNoFilterIsIdentity(t *testing.T) {
	records := ParseLog(sampleLog)
	if got := FilterLog(records, LogFilter{}); len(got) != len(records) {
		t.Fatalf("got %d, want all %d", len(got), len(records))
	}
}

func TestParseLevelAgreesWithTheFlagAndTheKey(t *testing.T) {
	cases := []struct {
		name string
		want slog.Level
		on   bool
	}{
		{"", 0, false}, {"all", 0, false},
		{"warn", slog.LevelWarn, true}, {"WARNING", slog.LevelWarn, true},
		{"error", slog.LevelError, true}, {"debug", slog.LevelDebug, true},
		{"nonsense", slog.LevelDebug, true},
	}
	for _, c := range cases {
		got, on := ParseLevel(c.name)
		if got != c.want || on != c.on {
			t.Errorf("ParseLevel(%q) = %v/%v, want %v/%v", c.name, got, on, c.want, c.on)
		}
	}
}

func TestShortTimeDropsTheDateAndOffset(t *testing.T) {
	if got := shortTime("2026-09-06T22:49:03.512-07:00"); got != "22:49:03.512" {
		t.Errorf("shortTime = %q", got)
	}
	if got := shortTime("2026-09-06T22:49:03.512Z"); got != "22:49:03.512" {
		t.Errorf("shortTime(Z) = %q", got)
	}
}
