//go:build windows

package main

// Raw mode and window size on Windows, through the console API directly.
//
// golang.org/x/sys is not a dependency of this project and is not going to become one for a log
// viewer, so these are four kernel32 entry points reached with syscall.NewLazyDLL. That is safe
// for kernel32 specifically: it is a KnownDll, resolved by the loader from the known-DLL section
// rather than from the process search path, so the usual "a DLL of that name in the working
// directory wins" hijack does not apply.
//
// Every failure here is reported rather than worked around, because the caller's answer to
// "this console cannot do it" is to print the data plainly instead — which is the right answer
// for a redirected stdout, a Windows 8 console with no VT support, and an editor's built-in
// terminal alike.

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"
	"unsafe"
)

var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleMode             = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode             = kernel32.NewProc("SetConsoleMode")
	procGetConsoleScreenBufferInfo = kernel32.NewProc("GetConsoleScreenBufferInfo")
)

// The console mode flags this program touches. Named here rather than imported because syscall
// does not export them and the values are part of the stable Win32 ABI.
const (
	enableProcessedInput       = 0x0001
	enableLineInput            = 0x0002
	enableEchoInput            = 0x0004
	enableVirtualTerminalInput = 0x0200

	enableProcessedOutput           = 0x0001
	enableVirtualTerminalProcessing = 0x0004
)

type coord struct{ X, Y int16 }

type smallRect struct{ Left, Top, Right, Bottom int16 }

type consoleScreenBufferInfo struct {
	Size              coord
	CursorPosition    coord
	Attributes        uint16
	Window            smallRect
	MaximumWindowSize coord
}

func getConsoleMode(h syscall.Handle) (uint32, error) {
	var mode uint32
	r1, _, err := procGetConsoleMode.Call(uintptr(h), uintptr(unsafe.Pointer(&mode)))
	if r1 == 0 {
		return 0, err
	}
	return mode, nil
}

func setConsoleMode(h syscall.Handle, mode uint32) error {
	r1, _, err := procSetConsoleMode.Call(uintptr(h), uintptr(mode))
	if r1 == 0 {
		return err
	}
	return nil
}

// rawMode puts stdin into character-at-a-time mode and stdout into VT mode, returning the
// function that puts both back.
func rawMode(in, out *os.File) (func(), error) {
	if in == nil || out == nil {
		return nil, errors.New("no console attached")
	}
	inH, outH := syscall.Handle(in.Fd()), syscall.Handle(out.Fd())

	inMode, err := getConsoleMode(inH)
	if err != nil {
		return nil, fmt.Errorf("stdin is not a console: %w", err)
	}
	outMode, err := getConsoleMode(outH)
	if err != nil {
		return nil, fmt.Errorf("stdout is not a console: %w", err)
	}

	// Line input and echo off, so a key arrives when it is pressed rather than at Enter.
	// Processed input off as well, which is what turns Ctrl-C into the byte 0x03: the loop then
	// quits through the same path as q and restores the console on its way out, rather than a
	// signal handler racing the restore and sometimes losing.
	newIn := inMode&^(enableLineInput|enableEchoInput|enableProcessedInput) | enableVirtualTerminalInput
	// Virtual terminal processing is what makes the escape sequences in term.go mean anything.
	// It exists from Windows 10 1511; on anything older SetConsoleMode fails and the caller
	// falls back to the plain listing, which is the correct outcome rather than a screen of
	// literal "[2J".
	newOut := outMode | enableProcessedOutput | enableVirtualTerminalProcessing

	if err := setConsoleMode(inH, newIn); err != nil {
		return nil, fmt.Errorf("cannot put stdin into raw mode: %w", err)
	}
	if err := setConsoleMode(outH, newOut); err != nil {
		_ = setConsoleMode(inH, inMode)
		return nil, fmt.Errorf("this console has no virtual terminal support: %w", err)
	}

	var once sync.Once
	return func() {
		// Once, because restore is deferred AND called on the way out of the event loop: a second
		// SetConsoleMode would be harmless today but the guarantee is worth more than the branch.
		once.Do(func() {
			_ = setConsoleMode(inH, inMode)
			_ = setConsoleMode(outH, outMode)
		})
	}, nil
}

// newSizer reports the console size. On Windows the query is a single syscall, so it is answered
// live on every frame and a window resized mid-session is picked up on the next keypress.
func newSizer(out *os.File) (func() (int, int), func()) {
	h := syscall.Handle(out.Fd())
	return func() (int, int) {
		var info consoleScreenBufferInfo
		r1, _, _ := procGetConsoleScreenBufferInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&info)))
		if r1 == 0 {
			return defaultCols, defaultRows
		}
		// The WINDOW, not the buffer: a console's buffer is commonly 9001 rows tall for
		// scrollback, and laying the screen out to that would put the footer somewhere below the
		// bottom of the visible area.
		cols := int(info.Window.Right-info.Window.Left) + 1
		rows := int(info.Window.Bottom-info.Window.Top) + 1
		if cols < 20 || rows < 6 {
			return defaultCols, defaultRows
		}
		return cols, rows
	}, func() {}
}
