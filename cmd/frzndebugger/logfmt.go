package main

// Reading the run log back.
//
// internal/logging writes logfmt — slog's TextHandler — and its package comment says so
// deliberately: "logfmt stays parseable for a tool that wants to read the file back". This is
// that tool. The parser below is the only place in this program that knows the log's shape.
//
// It is written the way internal/timings.Parse is written, and for the same reason: the run
// worth looking at is the one that died. A line that does not parse is KEPT, marked, and shown —
// never dropped. A viewer that hides the one weird line at the end of a broken file is a viewer
// that hides the answer.

import (
	"log/slog"
	"strconv"
	"strings"
	"time"

	"frznforge/internal/logging"
)

// Attr is one key=value pair off a record, in the order the handler wrote it. A slice rather
// than a map because slog's group keys ("git.args") repeat under different prefixes and because
// ranging a map would make the display order depend on Go's hash seed.
type Attr struct {
	Key   string `json:"k"`
	Value string `json:"v"`
}

// LogRecord is one line of <outDir>/frznforge.log.
type LogRecord struct {
	// N is the 1-based line number, so a person can find the line in the file itself.
	N int `json:"n"`
	// Time is the timestamp verbatim, not a time.Time: the file's format carries a UTC offset
	// that a re-formatted local time would silently throw away, and this program never does
	// arithmetic on it.
	Time string `json:"time"`
	// Level is the level name as written ("WARN", "ERROR+2"), empty on an unparsed line.
	Level string `json:"level"`
	// LevelValue is Level as slog's numeric level, for comparing against a filter.
	LevelValue int `json:"levelValue"`
	// Unparsed marks a line with no level= key. Such a line passes every level filter, because
	// evidence you cannot classify is still evidence.
	Unparsed bool   `json:"unparsed"`
	Msg      string `json:"msg"`
	Attrs    []Attr `json:"attrs,omitempty"`
	// Raw is the whole line, which is what the substring filter searches and what is shown when
	// the line did not parse.
	Raw string `json:"raw"`
}

// Warning reports whether this record deserves the eye: WARN and above, or a line that could not
// be read at all.
func (r LogRecord) Warning() bool {
	return r.Unparsed || r.LevelValue >= int(slog.LevelWarn)
}

// Error reports whether the record is at ERROR or above.
func (r LogRecord) Error() bool {
	return !r.Unparsed && r.LevelValue >= int(slog.LevelError)
}

// ParseLog reads a whole run log.
//
// Every string it produces goes through logging.Scrub on the way out. The file was already
// scrubbed when it was written, so this is the second pass, not the first — and it is worth
// having: a log written by a build before redaction existed, or one copied from a colleague's
// machine, gets the shape-based patterns (URL userinfo, token query parameters, Authorization
// headers) applied by the process that is about to put it on a screen and into an HTTP response.
// Doing it here, at the one boundary where bytes become records, is what stops any of the three
// views from having to remember.
func ParseLog(text string) []LogRecord {
	if text == "" {
		return nil
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	out := make([]LogRecord, 0, len(lines))
	for i, line := range lines {
		// The last element of a trailing-newline split is empty and is not a record. Any other
		// blank line is not one either.
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, parseLogLine(i+1, logging.Scrub(line)))
	}
	return out
}

func parseLogLine(n int, line string) LogRecord {
	rec := LogRecord{N: n, Raw: line, Unparsed: true, Msg: line}
	pairs := splitLogfmt(line)
	if len(pairs) == 0 {
		return rec
	}
	var attrs []Attr
	sawLevel := false
	for _, p := range pairs {
		switch p.Key {
		case slog.TimeKey:
			rec.Time = p.Value
		case slog.LevelKey:
			var level slog.Level
			// UnmarshalText handles the offset spellings ("WARN+3") that slog itself writes for a
			// custom level, so this does not have to know about them.
			if err := level.UnmarshalText([]byte(p.Value)); err == nil {
				rec.LevelValue = int(level)
			}
			rec.Level = p.Value
			sawLevel = true
		case slog.MessageKey:
			rec.Msg = p.Value
		default:
			attrs = append(attrs, p)
		}
	}
	if !sawLevel {
		// No level means this is not a record slog wrote — a stray line, a torn write, output from
		// something else that shared the file. Keep it whole rather than half-interpreted.
		return rec
	}
	rec.Unparsed = false
	rec.Attrs = attrs
	return rec
}

// splitLogfmt splits a logfmt line into key=value pairs.
//
// TextHandler quotes a value with strconv.Quote whenever it contains a space, a quote, an equals
// sign or anything unprintable, so a quoted value can contain the separator and must be scanned
// rather than split on. Everything before the first '=' of a token is the key, which is why a
// bare word with no '=' ends the parse: it is not a pair, and guessing what it was would invent
// a record.
func splitLogfmt(line string) []Attr {
	var out []Attr
	i := 0
	for i < len(line) {
		for i < len(line) && line[i] == ' ' {
			i++
		}
		start := i
		for i < len(line) && line[i] != '=' && line[i] != ' ' {
			i++
		}
		if i >= len(line) || line[i] != '=' {
			return out
		}
		key := line[start:i]
		i++ // past '='

		if i < len(line) && line[i] == '"' {
			value, next, ok := readQuoted(line, i)
			if !ok {
				return out
			}
			out = append(out, Attr{Key: key, Value: value})
			i = next
			continue
		}
		vs := i
		for i < len(line) && line[i] != ' ' {
			i++
		}
		out = append(out, Attr{Key: key, Value: line[vs:i]})
	}
	return out
}

// readQuoted reads a strconv-quoted value starting at the opening quote, returning the unquoted
// text and the index just past the closing quote.
func readQuoted(line string, at int) (value string, next int, ok bool) {
	i := at + 1
	for i < len(line) {
		switch line[i] {
		case '\\':
			i += 2 // an escaped character cannot end the string, whatever it is
			continue
		case '"':
			unquoted, err := strconv.Unquote(line[at : i+1])
			if err != nil {
				return "", 0, false
			}
			return unquoted, i + 1, true
		}
		i++
	}
	return "", 0, false
}

/* ---- filtering ----------------------------------------------------------- */

// LogFilter is the log view's two questions: how loud, and about what.
type LogFilter struct {
	// MinLevel drops anything quieter. Zero value is slog.LevelInfo, so HasLevel says whether a
	// filter was asked for at all — "show me everything" and "show me info and up" differ.
	MinLevel slog.Level
	HasLevel bool
	// Text is a case-insensitive substring, matched against the whole raw line so a filter finds
	// a repository name whether it landed in the message or in an attribute.
	Text string
}

// Active reports whether the filter does anything.
func (f LogFilter) Active() bool { return f.HasLevel || f.Text != "" }

// FilterLog applies a filter, preserving file order.
func FilterLog(records []LogRecord, f LogFilter) []LogRecord {
	if !f.Active() {
		return records
	}
	needle := strings.ToLower(f.Text)
	out := make([]LogRecord, 0, len(records))
	for _, r := range records {
		// An unparsed line has no level to judge, and dropping it would hide the torn tail of a
		// killed run — which is the single line most likely to matter.
		if f.HasLevel && !r.Unparsed && r.LevelValue < int(f.MinLevel) {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(r.Raw), needle) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// LevelNames are the level filter's stops, quietest first. The empty string is "no filter".
var LevelNames = []string{"", "debug", "info", "warn", "error"}

// ParseLevel maps a level name onto a filter. It accepts what logging.Level accepts, plus "all"
// and "" for no filter, so --level= and the l key agree about what the words mean.
func ParseLevel(name string) (slog.Level, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "all", "off", "none":
		return 0, false
	case "error":
		return slog.LevelError, true
	case "warn", "warning":
		return slog.LevelWarn, true
	case "info":
		return slog.LevelInfo, true
	case "debug":
		return slog.LevelDebug, true
	default:
		var level slog.Level
		if err := level.UnmarshalText([]byte(name)); err == nil {
			return level, true
		}
		// Same call as logging.Level: an unrecognised name asks for more output, not for an error.
		return slog.LevelDebug, true
	}
}

// LastLogTime is when the run log stopped being written, or the zero time if it cannot be told.
//
// It is the best available answer to "when did the process actually stop", which the timings
// file cannot give: that file records completions, and the step worth asking about is the one
// with no completion. Scans backwards because the answer is nearly always the last line.
//
// The layout is the file sink's own (internal/logging/file.go). The stderr sink writes a
// time-only stamp with no date, and that deliberately does not parse here — half a timestamp
// would produce a confident wrong answer, and no answer is better.
func LastLogTime(records []LogRecord, runID string) time.Time {
	// Only when the two files describe the SAME run. The log is truncated per run and the timings
	// file is appended across runs, so an old timings file beside a fresh log is the normal state
	// of a data directory — and taking the floor from the wrong run turns "at least 4s" into "at
	// least seven hours", which is worse than the understatement it was meant to fix.
	//
	// `run start` names the run (cmd/frznforge/diagnostics.go), so the check is exact rather than
	// a guess about clocks.
	if runID == "" || !logIsForRun(records, runID) {
		return time.Time{}
	}
	const layout = "2006-01-02T15:04:05.000-07:00"
	for i := len(records) - 1; i >= 0; i-- {
		if t, err := time.Parse(layout, records[i].Time); err == nil {
			return t
		}
	}
	return time.Time{}
}

func logIsForRun(records []LogRecord, runID string) bool {
	for _, r := range records {
		for _, a := range r.Attrs {
			if a.Key == "run" {
				return a.Value == runID
			}
		}
	}
	return false
}
