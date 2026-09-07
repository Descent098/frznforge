//go:build windows

package build

import (
	"os/exec"
	"syscall"
)

// shellName is the interpreter a postprocess command runs through on Windows.
const shellName = "cmd"

// shellCommand hands cmd.exe a RAW command line rather than an argv.
//
// os/exec builds a Windows command line by escaping each argument the way CommandLineToArgvW
// parses it — an embedded `"` becomes `\"`. cmd.exe has never understood that escape, so a hook
// as ordinary as
//
//	echo done >"C:\out dir\marker.txt"
//
// reaches it as `echo done >\"C:\out dir\marker.txt\"` and dies with "The filename, directory
// name, or volume label syntax is incorrect". Any path with a space in it needs those quotes,
// which made this the difference between the hook working on Windows and not.
//
// `/C "<line>"` is cmd.exe's own documented form, and the wrapping quotes matter: with more than
// two quote characters cmd strips the outer pair and runs the rest verbatim, which is exactly
// what a hook wants — including one that starts with a quoted program path
// (`"C:\Program Files\tool.exe" --minify`).
func shellCommand(line string) *exec.Cmd {
	cmd := exec.Command(shellName)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `/C "` + line + `"`}
	return cmd
}
