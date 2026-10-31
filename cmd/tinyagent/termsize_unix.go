//go:build linux || darwin

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// terminalWidth returns f's column count, or 0 when f isn't a terminal.
func terminalWidth(f *os.File) int {
	ws, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	if err != nil {
		return 0
	}
	return int(ws.Col)
}

// enableEscapes is a no-op: Unix terminals take ANSI escapes as they are.
func enableEscapes(*os.File) bool { return true }
