//go:build !windows

package main

import (
	"os"

	"golang.org/x/term"
)

// colorTerminal reports whether f is a terminal.
func colorTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}
