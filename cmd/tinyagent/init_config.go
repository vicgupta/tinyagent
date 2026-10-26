package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/vicgupta/tinyagent/internal/config"
)

const starterConfig = `# tinyagent user config. Flags override these values. Full reference: README "Config".
# model: openai/gpt-4o        # same <provider>/<model> syntax as -m
# step_limit: 50
# cost_limit: 2.0
# wall_time_seconds: 1800
# read_only: false
`

func runInitConfig(path string) int {
	if path == "" {
		var err error
		path, err = config.DefaultPath()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error: config path:", err)
			return 2
		}
	}
	if err := writeStarterConfig(path); err != nil {
		fmt.Fprintf(os.Stderr, "error: init config %s: %v\n", path, err)
		return 2
	}
	fmt.Println(path)
	return 0
}

func writeStarterConfig(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	// Exclusive creation also refuses symlinks and avoids an overwrite race.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.WriteString(starterConfig)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}
