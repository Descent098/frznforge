package timings

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
)

// Parse reads a timings file.
//
// It is written to survive the file it is reading, because the run this file describes best is
// the one that was killed:
//
//   - A final line with no newline is a record that was being written when the process died. It
//     is dropped, not reported: an incomplete line is expected, not corrupt.
//   - A line that does not parse is skipped and the rest of the file is still read. One bad
//     line — a disk that wrote garbage, a version that wrote something else — must not cost the
//     other ten thousand.
//   - Lines are read with a bufio.Reader rather than a Scanner on purpose. Scanner gives up on
//     a line longer than its buffer and silently ends the file there; a long error string in one
//     record would then hide every record after it.
//
// The returned error is I/O only. Skipped lines are reported at debug level into the run log,
// which is where someone already looking at a broken file will be.
func Parse(r io.Reader) ([]Record, error) {
	var (
		out     []Record
		skipped int
		br      = bufio.NewReader(r)
	)
	for {
		line, err := br.ReadBytes('\n')
		// err == nil is the test for "this line was complete": on the last, torn line ReadBytes
		// hands back what it read together with io.EOF, and that partial record is not a record.
		if err == nil && len(bytes.TrimSpace(line)) > 0 {
			var rec Record
			if json.Unmarshal(line, &rec) != nil {
				skipped++
				continue
			}
			out = append(out, rec)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				// Anything still in `line` here had no newline: a torn tail. Drop it.
				if skipped > 0 {
					slog.Debug("timings: skipped unparseable lines", "lines", skipped)
				}
				return out, nil
			}
			return out, err
		}
	}
}

// ParseFile reads the timings file at path.
//
// A file that does not exist is not an error: "no build has run yet" and "the last build wrote
// nothing" are the same empty answer to a viewer, and both are ordinary.
func ParseFile(path string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	return Parse(f)
}

// ParseDir reads the timings file belonging to an ingest output directory.
func ParseDir(outDir string) ([]Record, error) { return ParseFile(Path(outDir)) }
