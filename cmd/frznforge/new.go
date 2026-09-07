package main

// `frznforge new <dir>` — scaffold the files a site owner authors.
//
// Everything that decides anything lives in internal/scaffold, arguments included: the whole
// command is testable there without a process. This file is only the wiring that supplies a
// working directory and a place to print.

import "frznforge/internal/scaffold"

func newCmd(argv []string, io *Io) error {
	return scaffold.Command(argv, io.Cwd, io.log)
}
