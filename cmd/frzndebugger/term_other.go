//go:build !windows

package main

// Raw mode and window size everywhere that is not Windows, through stty.
//
// The alternative was raw termios ioctls, and it was rejected: without golang.org/x/sys the
// request numbers have to be written out per GOOS and per GOARCH by hand (TCGETS is 0x5401 on
// most of Linux but not all of it, TIOCGETA is a different number again on the BSDs), and the
// failure mode of getting one wrong is a terminal left in a mode the user's shell cannot undo.
// stty is specified by POSIX, is on every machine that has a terminal at all, and when it is
// missing it fails cleanly — which the caller turns into the plain listing instead of a
// half-broken screen. The cost is two process spawns per session, which is nothing next to
// getting the restore wrong once.
//
// Windows is this project's development platform, so this file is the fallback path and is
// written to be obviously correct rather than clever.

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// stty runs stty against the terminal on fd, returning its stdout.
//
// Stdin is the terminal itself rather than the controlling one: a session whose stdin has been
// redirected is not one this program will enter raw mode for anyway, and passing the file
// through is what makes the saved settings belong to the right device.
func stty(tty *os.File, args ...string) (string, error) {
	cmd := exec.Command("stty", args...)
	cmd.Stdin = tty
	out, err := cmd.Output()
	return string(out), err
}

func rawMode(in, out *os.File) (func(), error) {
	if !isCharDevice(in) || !isCharDevice(out) {
		return nil, errors.New("stdin and stdout are not both a terminal")
	}
	saved, err := stty(in, "-g")
	if err != nil {
		return nil, errors.New("stty is not available to put the terminal into raw mode: " + err.Error())
	}
	saved = strings.TrimSpace(saved)
	if saved == "" {
		return nil, errors.New("stty reported no terminal settings to restore")
	}
	// -echo alongside raw: `stty raw` alone still echoes on some systems, and a screen that
	// prints every j and k the user presses is unusable.
	//
	// intr undef makes Ctrl-C arrive as the byte 0x03 rather than as a signal, matching Windows,
	// so the loop quits through its own exit path and restores the terminal itself.
	if _, err := stty(in, "raw", "-echo", "intr", "undef"); err != nil {
		return nil, errors.New("stty could not put the terminal into raw mode: " + err.Error())
	}
	var once sync.Once
	return func() { once.Do(func() { _, _ = stty(in, saved) }) }, nil
}

// newSizer caches the size and refreshes it on SIGWINCH.
//
// Cached because asking costs a process spawn here, and asking once per frame would mean one
// fork per keypress. SIGWINCH is the signal a terminal sends when it is resized, so the cache is
// only ever stale between the resize and the signal being delivered.
func newSizer(out *os.File) (func() (int, int), func()) {
	var (
		mu   sync.Mutex
		cols = defaultCols
		rows = defaultRows
	)
	refresh := func() {
		text, err := stty(out, "size")
		if err != nil {
			return
		}
		fields := strings.Fields(text)
		if len(fields) != 2 {
			return
		}
		r, rerr := strconv.Atoi(fields[0])
		c, cerr := strconv.Atoi(fields[1])
		if rerr != nil || cerr != nil || c < 20 || r < 6 {
			return
		}
		mu.Lock()
		cols, rows = c, r
		mu.Unlock()
	}
	refresh()

	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-winch:
				refresh()
			case <-done:
				return
			}
		}
	}()

	size := func() (int, int) {
		mu.Lock()
		defer mu.Unlock()
		return cols, rows
	}
	stop := func() {
		signal.Stop(winch)
		close(done)
	}
	return size, stop
}
