//go:build !windows

package main

import (
	"bufio"
	"context"
	"os"
)

// readTerminalLine reads one line from the controlling terminal (/dev/tty),
// not stdin, so -t - keeps working. It returns ctx's error when the run is
// interrupted or its time limit fires mid-prompt.
func readTerminalLine(ctx context.Context) (string, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := bufio.NewReader(f).ReadString('\n')
		ch <- result{line, err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-ch:
		return r.line, r.err
	}
}
