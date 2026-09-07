package main

// The terminal, by hand.
//
// No dependency: the project has two, both pure Go and both about rendering a site, and a
// developer's log viewer is not the thing that earns a third. So this is ANSI escape sequences
// written out as strings and a key decoder written over raw bytes. Both halves are small because
// the UI deliberately asks for very little — no mouse, no colours beyond the eight everything
// has had since 1979, no repainting cleverness.
//
// The platform-specific half is exactly two functions, in term_windows.go and term_other.go:
// putting the console into raw mode, and asking how big it is.

import (
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

/* ---- output -------------------------------------------------------------- */

const (
	// The alternate screen buffer. Entering it means the user's scrollback is untouched and
	// comes straight back when this exits, which matters because someone running this is in the
	// middle of reading something else.
	ansiAltEnter = "\x1b[?1049h"
	ansiAltExit  = "\x1b[?1049l"

	ansiHideCursor = "\x1b[?25l"
	ansiShowCursor = "\x1b[?25h"
	ansiHome       = "\x1b[H"
	ansiClearBelow = "\x1b[J"
	ansiClearLine  = "\x1b[K"
	ansiReset      = "\x1b[0m"
	ansiSelected   = "\x1b[7m"
)

// styleCode is the escape for a row style.
//
// Eight colours and the two attributes, nothing else: 256-colour and true-colour sequences are
// silently wrong on a terminal that does not have them, and this program has to work on the
// terminal the user is stuck with rather than the one they should have.
func styleCode(s Style) string {
	switch s {
	case StyleDim:
		return "\x1b[2m"
	case StyleHead:
		return "\x1b[1m"
	case StyleAccent:
		return "\x1b[36m"
	case StyleWarn:
		return "\x1b[33m"
	case StyleError:
		return "\x1b[1;31m"
	default:
		return ""
	}
}

// moveTo is the 1-based cursor position escape.
func moveTo(row, col int) string {
	return "\x1b[" + itoa(row) + ";" + itoa(col) + "H"
}

// itoa avoids pulling strconv into the hot path of a redraw for numbers that are always small.
func itoa(n int) string {
	if n <= 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 && i > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

/* ---- input --------------------------------------------------------------- */

// KeyCode names the keys the UI reacts to. Everything else arrives as KeyRune.
type KeyCode int

const (
	KeyRune KeyCode = iota
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyPageUp
	KeyPageDown
	KeyHome
	KeyEnd
	KeyEnter
	KeyBackspace
	KeyEscape
	KeyTab
	// KeyInterrupt is Ctrl-C or Ctrl-D. In raw mode Ctrl-C is a byte rather than a signal, which
	// is the safer arrangement: the loop quits through its own exit path and restores the
	// console itself instead of racing a signal handler that might run after the process is gone.
	KeyInterrupt
	KeyUnknown
)

// Key is one keypress.
type Key struct {
	Code KeyCode
	Rune rune
}

// escapeGap is how long the decoder waits after a lone ESC before deciding it was the Escape key
// rather than the start of a sequence.
//
// A terminal sends the whole sequence in one write, so any real arrow key delivers its next byte
// immediately; the wait only ever costs something when Escape really was pressed, and 50ms of
// nothing happening is below what anyone notices.
const escapeGapMS = 50

// byteSource is a stream of input bytes that closes when the input ends. Reading input on its
// own goroutine is what lets the decoder time out on a lone ESC without a non-blocking read.
type byteSource struct {
	ch   <-chan byte
	done chan struct{}
	// pending holds bytes read ahead and handed back — the byte after an ESC that turned out not
	// to start a sequence. Only the decoding goroutine touches it, so it needs no lock.
	pending []byte
}

func newByteSource(r io.Reader) *byteSource {
	ch := make(chan byte, 4096)
	src := &byteSource{ch: ch, done: make(chan struct{})}
	go func() {
		defer close(ch)
		buf := make([]byte, 512)
		for {
			n, err := r.Read(buf)
			for i := 0; i < n; i++ {
				select {
				case ch <- buf[i]:
				case <-src.done:
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return src
}

// Close releases the reader goroutine. The goroutine cannot be interrupted mid-Read — there is
// no portable way to cancel a blocking console read — so it may sit on one until the process
// ends; what this stops is the leak of a goroutine blocked forever on the channel.
func (s *byteSource) Close() { close(s.done) }

// read takes the next byte, from the pushback buffer first.
func (s *byteSource) read() (byte, bool) {
	if len(s.pending) > 0 {
		b := s.pending[0]
		s.pending = s.pending[1:]
		return b, true
	}
	b, ok := <-s.ch
	return b, ok
}

// unread hands a byte back so the next call to next sees it as its own keypress.
func (s *byteSource) unread(b byte) {
	s.pending = append([]byte{b}, s.pending...)
}

// timer is the escape-gap clock, injected so a test can drive the decoder without waiting.
type timer func(ms int) <-chan struct{}

// next decodes one keypress. ok is false when the input has ended, which is how a piped session
// and a closed terminal both stop the loop rather than spinning.
func (s *byteSource) next(after timer) (Key, bool) {
	b, ok := s.read()
	if !ok {
		return Key{}, false
	}
	switch b {
	case 0x03, 0x04:
		return Key{Code: KeyInterrupt}, true
	case '\r', '\n':
		return Key{Code: KeyEnter}, true
	case '\t':
		return Key{Code: KeyTab}, true
	case 0x7f, 0x08:
		return Key{Code: KeyBackspace}, true
	case 0x1b:
		return s.escape(after), true
	}
	if b < 0x20 {
		return Key{Code: KeyUnknown, Rune: rune(b)}, true
	}
	if b < utf8.RuneSelf {
		return Key{Code: KeyRune, Rune: rune(b)}, true
	}
	return Key{Code: KeyRune, Rune: s.rune(b)}, true
}

// rune completes a multi-byte UTF-8 character. A filter typed with a non-ASCII repository name
// in it has to work; a byte-at-a-time decoder would turn one character into three unknown keys.
func (s *byteSource) rune(first byte) rune {
	buf := []byte{first}
	for len(buf) < utf8.UTFMax {
		if r, _ := utf8.DecodeRune(buf); r != utf8.RuneError {
			return r
		}
		b, ok := s.read()
		if !ok {
			break
		}
		buf = append(buf, b)
	}
	r, _ := utf8.DecodeRune(buf)
	return r
}

// escape decodes what follows an ESC byte.
func (s *byteSource) escape(after timer) Key {
	intro, ok := s.peek(after)
	if !ok {
		return Key{Code: KeyEscape}
	}
	if intro != '[' && intro != 'O' {
		// ESC followed by anything else is Alt+that key on most terminals. Nothing here binds Alt,
		// and consuming the key would lose a keystroke — pressing Escape and then q would quietly
		// eat the q — so the byte goes back and this reports a plain Escape.
		s.unread(intro)
		return Key{Code: KeyEscape}
	}

	// CSI: parameter bytes, then one final byte in 0x40..0x7e. Parameters are read and mostly
	// discarded — "ctrl+arrow" arrives as ESC[1;5A and is treated as the plain arrow, which is
	// what someone pressing it wanted.
	var seq strings.Builder
	for i := 0; i < 16; i++ {
		b, ok := s.read()
		if !ok {
			return Key{Code: KeyEscape}
		}
		if b >= 0x40 && b <= 0x7e {
			return csiKey(seq.String(), b)
		}
		seq.WriteByte(b)
	}
	return Key{Code: KeyUnknown}
}

// peek waits up to the escape gap for the byte after an ESC. Anything already pushed back
// counts as immediate: it was read before the ESC's own arrival and is not the gap's business.
func (s *byteSource) peek(after timer) (byte, bool) {
	if len(s.pending) > 0 {
		return s.read()
	}
	select {
	case b, ok := <-s.ch:
		return b, ok
	case <-after(escapeGapMS):
		return 0, false
	}
}

func csiKey(params string, final byte) Key {
	switch final {
	case 'A':
		return Key{Code: KeyUp}
	case 'B':
		return Key{Code: KeyDown}
	case 'C':
		return Key{Code: KeyRight}
	case 'D':
		return Key{Code: KeyLeft}
	case 'H':
		return Key{Code: KeyHome}
	case 'F':
		return Key{Code: KeyEnd}
	case '~':
		// The numeric forms, which is how a PC keyboard's navigation block arrives.
		switch leadingNumber(params) {
		case 1, 7:
			return Key{Code: KeyHome}
		case 4, 8:
			return Key{Code: KeyEnd}
		case 5:
			return Key{Code: KeyPageUp}
		case 6:
			return Key{Code: KeyPageDown}
		}
	}
	return Key{Code: KeyUnknown}
}

func leadingNumber(params string) int {
	n := 0
	for i := 0; i < len(params); i++ {
		if params[i] < '0' || params[i] > '9' {
			break
		}
		n = n*10 + int(params[i]-'0')
	}
	return n
}

/* ---- is anyone there ----------------------------------------------------- */

// isCharDevice reports whether f is a console rather than a pipe or a file.
//
// Stat's character-device bit rather than a syscall: it answers correctly on Windows consoles,
// Unix ttys and every redirection of both, and it is the same test cmd/frznforge uses.
func isCharDevice(f *os.File) bool {
	if f == nil {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
