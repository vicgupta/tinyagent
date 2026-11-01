//go:build !linux && !darwin && !windows

package main

import "os"

// terminalWidth is unknown here; chat falls back to $COLUMNS or 80.
func terminalWidth(*os.File) int { return 0 }

// enableEscapes assumes the terminal takes ANSI escapes.
func enableEscapes(*os.File) bool { return true }
