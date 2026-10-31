//go:build !linux && !darwin

package main

import (
	"errors"
	"os"
)

// makeRaw is not supported here: chat reads cooked lines (no in-place
// editing or history recall).
func makeRaw(*os.File) (func(), error) {
	return nil, errors.New("raw terminal mode is not supported on this platform")
}
