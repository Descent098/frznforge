package highlight

import (
	"testing"

	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/yuin/goldmark"
)

// TestDependenciesResolve is a smoke check that the two chosen dependencies build and work on
// the toolchain this repo pins. Both were an owner decision; this is where "it fetched" turns
// into "it runs".
func TestDependenciesResolve(t *testing.T) {
	if n := len(lexers.Names(false)); n < 100 {
		t.Errorf("chroma resolved only %d lexers", n)
	} else {
		t.Logf("chroma lexers: %d", n)
	}
	if goldmark.New() == nil {
		t.Error("goldmark did not construct")
	}
}
