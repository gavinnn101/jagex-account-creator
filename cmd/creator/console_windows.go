//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// colorTerminal reports whether f is a console that shows ANSI colors. It
// turns on VT processing when the console has it off.
// Limitation: in a classic conhost window, VT processing stays on after the run exits.
func colorTerminal(f *os.File) bool { // coverage-ignore: needs a console
	h := windows.Handle(f.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return false
	}
	if mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
		return true
	}
	return windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}
