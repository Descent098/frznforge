//go:build !windows

package build

import "os/exec"

// shellName is the interpreter a postprocess command runs through everywhere but Windows.
const shellName = "sh"

// shellCommand hands the line to `sh -c`, which takes it as one argument and does its own
// quoting — no raw command line needed, and none of the escaping the Windows half has to work
// around.
func shellCommand(line string) *exec.Cmd {
	return exec.Command(shellName, "-c", line)
}
