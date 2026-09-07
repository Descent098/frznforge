package main

import (
	"os"
	"testing"
)

// The console plumbing itself cannot be exercised by `go test`, which runs with no terminal
// attached. What CAN be checked is the half that matters when there is none: the calls resolve,
// they refuse rather than panic, and the fallbacks are sane. A viewer that panics on the way to
// deciding it cannot draw is worse than one that never draws.

func TestRawModeRefusesWhereThereIsNoConsole(t *testing.T) {
	// Under `go test` stdin is not a terminal, so this is the degrade path — and reaching it
	// proves the platform entry points (four kernel32 procs on Windows, stty elsewhere) resolve.
	if restore, err := rawMode(os.Stdin, os.Stdout); err == nil {
		// A machine that somehow gives the test binary a real console must still be put back.
		restore()
		t.Skip("this run has a console attached; the refusal path was not exercised")
	}
	if _, err := rawMode(nil, nil); err == nil {
		t.Error("nil files must be refused, not dereferenced")
	}
}

func TestSizerAlwaysAnswers(t *testing.T) {
	size, stop := newSizer(os.Stdout)
	defer stop()
	w, h := size()
	if w < 20 || h < 6 {
		t.Errorf("size = %dx%d; a screen of unknown size must fall back to something drawable", w, h)
	}
	// Twice, because the Unix sizer caches and the Windows one asks again each frame.
	if w2, h2 := size(); w2 != w || h2 != h {
		t.Errorf("size changed between calls with no resize: %dx%d then %dx%d", w, h, w2, h2)
	}
	stop()
}

func TestIsCharDeviceSaysNoForAFile(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "probe")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isCharDevice(f) {
		t.Error("a regular file is not a console")
	}
	if isCharDevice(nil) {
		t.Error("a nil file is not a console")
	}
}

func TestStyleCodesAreEscapeSequencesOrNothing(t *testing.T) {
	for _, s := range []Style{StylePlain, StyleDim, StyleHead, StyleAccent, StyleWarn, StyleError} {
		code := styleCode(s)
		if code == "" {
			continue // StylePlain, which relies on the reset already written
		}
		if code[0] != 0x1b {
			t.Errorf("style %v = %q, which is not an escape sequence", s, code)
		}
	}
}

func TestMoveToIsOneBased(t *testing.T) {
	if got := moveTo(1, 1); got != "\x1b[1;1H" {
		t.Errorf("moveTo(1,1) = %q", got)
	}
	if got := moveTo(24, 80); got != "\x1b[24;80H" {
		t.Errorf("moveTo(24,80) = %q", got)
	}
}
