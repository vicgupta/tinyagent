//go:build !linux && !darwin && !windows

package main

import "os"

// isTerminal reports whether f is an interactive terminal.
// Without a termios check here it never claims a terminal, so plain
// tinyagent keeps its usage error.
func isTerminal(f *os.File) bool { return false }
