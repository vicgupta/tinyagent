package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"strings"
)

// Setup hints (issue #293): for a fixed set of known setup failures, the
// error: line is followed by one hint: line naming the next action. API
// key and provider failures get theirs from model.Hint (printHint); this
// file covers invalid config and an unavailable sandbox. Unknown errors get
// no hint: tinyagent never guesses.

// configFileError is an invalid setting in a config file. Its message names
// the file, so an error found after loading (a permission rule, a hook, a
// command tool) points at the file as a load error does.
type configFileError struct {
	path string
	err  error
}

func (e *configFileError) Error() string {
	if strings.Contains(e.err.Error(), e.path) {
		return e.err.Error()
	}
	return "config " + e.path + ": " + strings.TrimPrefix(e.err.Error(), "config: ")
}

func (e *configFileError) Unwrap() error { return e.err }

// inConfig marks err as a setting in the config file at path. With no
// file (built-in defaults) or no error it returns err unchanged.
func inConfig(path string, err error) error {
	if err == nil || path == "" {
		return err
	}
	return &configFileError{path: path, err: err}
}

// sandboxStartError is a sandbox backend that can't start on this host.
type sandboxStartError struct {
	backend string
	err     error
}

func (e *sandboxStartError) Error() string { return e.err.Error() }
func (e *sandboxStartError) Unwrap() error { return e.err }

// sandboxUnavailable marks err as the sandbox backend failing to start.
func sandboxUnavailable(backend string, err error) error {
	if backend == "" {
		backend = "auto"
	}
	return &sandboxStartError{backend: backend, err: err}
}

// configKeyRe matches the setting a config error starts with, such as
// permissions.rules[2] or lsp.servers[0].
var configKeyRe = regexp.MustCompile(`^[a-z_]+(?:\.[a-z_]+|\[\d+\])*`)

// setupHint returns the one-line next action for a known setup failure,
// or "" for any other error.
func setupHint(err error) string {
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	var sb *sandboxStartError
	if errors.As(err, &sb) {
		alt := "--sandbox-backend landlock"
		if sb.backend == "landlock" {
			alt = "--sandbox-backend bwrap"
		}
		return fmt.Sprintf("the %s sandbox backend can't start on this host: fix the cause above, try %s, or run without --sandbox (sandbox: false in the config)", sb.backend, alt)
	}
	var cf *configFileError
	if errors.As(err, &cf) {
		return configHint(cf.path, cf.Error())
	}
	// Errors from loading the file already read "config <path>: <key>: ...".
	msg := err.Error()
	for _, p := range []string{"config ", "parse config "} {
		if rest, ok := strings.CutPrefix(msg, p); ok {
			if path, _, ok := strings.Cut(rest, ": "); ok && path != "" && !strings.ContainsAny(path, " \n") {
				return configHint(path, msg)
			}
		}
	}
	return ""
}

// configHint names the setting to fix in path, taken from msg ("config
// <path>: <key>: ..."), or the file alone when msg names no setting.
func configHint(path, msg string) string {
	rest := msg
	if i := strings.Index(msg, path); i >= 0 {
		rest = strings.TrimPrefix(msg[i+len(path):], ": ")
	}
	if k := configKeyRe.FindString(rest); k != "" && k != "yaml" && (strings.HasPrefix(rest[len(k):], ":") || strings.HasPrefix(rest[len(k):], " (")) {
		return fmt.Sprintf("fix %s in %s, then rerun tinyagent --check", k, path)
	}
	return fmt.Sprintf("fix %s, then rerun tinyagent --check", path)
}

// printSetupHint prints setupHint(err) as a hint: line, if there is one.
func printSetupHint(w io.Writer, err error) {
	if h := setupHint(err); h != "" {
		fmt.Fprintln(w, styleFor(w).Yellow("hint:"), h)
	}
}
