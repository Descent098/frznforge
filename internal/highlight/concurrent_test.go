package highlight

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestHighlightIsSafeUnderConcurrency is the regression test for a real defect found by building
// the site with repositories and pages rendering in parallel: chroma emitted spurious Error
// tokens around single characters, at random positions, in long lines of large files.
//
// Highlighting is a pure function of (code, language) — the same input must give the same output
// whether one goroutine calls it or thirty-two do. Anything else means the site's colouring
// depends on scheduling, which is both wrong and invisible: the page still renders, one letter
// just quietly turns red.
func TestHighlightIsSafeUnderConcurrency(t *testing.T) {
	// Several real files of DIFFERENT languages, which is what the build actually does: one
	// worker per page, each page a different file. Highlighting the SAME input concurrently was
	// never the failing shape — the corruption needs distinct texts sharing a lexer.
	type sample struct{ path, lang, name string }
	samples := []sample{
		{filepath.Join("..", "..", "docs", "dev", "data-model.md"), "Markdown", "data-model.md"},
		{filepath.Join("..", "..", "docs", "dev", "build-steps.md"), "Markdown", "build-steps.md"},
		{filepath.Join("..", "..", "web", "css", "global.css"), "CSS", "global.css"},
		{filepath.Join("..", "..", "web", "js", "listing.js"), "JavaScript", "listing.js"},
		{filepath.Join("..", "..", "src", "lib", "routes.ts"), "TypeScript", "routes.ts"},
		{filepath.Join("..", "highlight", "highlight.go"), "Go", "highlight.go"},
	}
	type job struct {
		src, lang, name, want string
	}
	var jobs []job
	for _, s := range samples {
		raw, err := os.ReadFile(s.path)
		if err != nil {
			continue
		}
		src := string(raw)
		jobs = append(jobs, job{src: src, lang: s.lang, name: s.name, want: Highlight(src, s.lang, s.name, "")})
	}
	if len(jobs) < 3 {
		t.Skip("not enough sample files on this machine")
	}

	const rounds = 8
	var wg sync.WaitGroup
	bad := make([]string, len(jobs)*rounds)
	for r := 0; r < rounds; r++ {
		for j := range jobs {
			wg.Add(1)
			go func(idx, j int) {
				defer wg.Done()
				if got := Highlight(jobs[j].src, jobs[j].lang, jobs[j].name, ""); got != jobs[j].want {
					bad[idx] = jobs[j].name + " " + firstDifference(jobs[j].want, got)
				}
			}(r*len(jobs)+j, j)
		}
	}
	wg.Wait()

	failures := 0
	var sample1 string
	for _, b := range bad {
		if b != "" {
			failures++
			if sample1 == "" {
				sample1 = b
			}
		}
	}
	if failures > 0 {
		t.Fatalf("%d of %d concurrent highlights differed from the serial result\nfirst difference: %s",
			failures, len(jobs)*rounds, sample1)
	}
}

func readSelf(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("no sample file at %s: %v", path, err)
	}
	return string(raw)
}

func firstDifference(want, got string) string {
	i := 0
	for i < len(want) && i < len(got) && want[i] == got[i] {
		i++
	}
	lo := i - 60
	if lo < 0 {
		lo = 0
	}
	clip := func(s string) string {
		hi := i + 60
		if hi > len(s) {
			hi = len(s)
		}
		if lo > len(s) {
			return "(end)"
		}
		return s[lo:hi]
	}
	return "at byte " + itoa(i) + "\n  serial:   …" + clip(want) + "\n  parallel: …" + clip(got)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}
