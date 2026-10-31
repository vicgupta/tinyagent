//go:build linux || darwin

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// makeRaw puts the terminal f in raw mode (no echo, no line buffering, no
// signals from Ctrl-C) and returns the function that restores it.
func makeRaw(f *os.File) (func(), error) {
	fd := int(f.Fd())
	t, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return nil, err
	}
	old := *t
	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	t.Cflag &^= unix.CSIZE | unix.PARENB
	t.Cflag |= unix.CS8
	t.Cc[unix.VMIN] = 1
	t.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, ioctlSetTermios, t); err != nil {
		return nil, err
	}
	return func() { unix.IoctlSetTermios(fd, ioctlSetTermios, &old) }, nil
}
