package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/model"
)

// dirtyWorktreeWarning is printed when --worktree starts from HEAD while the
// main checkout has uncommitted changes.
const dirtyWorktreeWarning = "warning: the main checkout has uncommitted changes; the worktree starts from HEAD without them"

// checkAttemptModels validates every attempt's model before the first
// attempt runs, the way a single run checks its own model: a configured
// provider's variables must be set, a built-in provider's API key variable
// must be set, and the client must build (unknown provider, missing model
// name, missing Codex login). No request is made. Each distinct spec is
// checked once; the error names the first attempt that uses it.
func checkAttemptModels(models []string, cfg config.AgentConfig, openaiAPI string) error {
	registerProviders(cfg)
	seen := map[string]bool{}
	for i, spec := range models {
		if seen[spec] {
			continue
		}
		seen[spec] = true
		wrap := func(err error) error {
			return fmt.Errorf("attempt %d model %q: %w", i+1, spec, err)
		}
		if spec == "" {
			// An empty -m leaves the child on its own default model.
			spec = resolveModel(flag.NewFlagSet("", flag.ContinueOnError), "", cfg.Model)
		}
		if err := model.CheckProviderEnv(spec); err != nil {
			return wrap(err)
		}
		if env := model.MissingKeyEnv(spec); env != "" {
			return wrap(fmt.Errorf("%s is not set", env))
		}
		if _, err := model.NewWith(spec, nil, model.Options{OpenAIAPI: model.OpenAITransport(spec, openaiAPI)}); err != nil {
			return wrap(err)
		}
	}
	return nil
}

// lineDropper writes to w everything except whole lines equal to line
// (which ends in "\n"). Bytes that could still begin such a line are held
// back until they can be decided; Flush writes whatever is held.
type lineDropper struct {
	w       io.Writer
	line    string
	atStart bool
	held    []byte
}

func (d *lineDropper) Write(p []byte) (int, error) {
	var out bytes.Buffer
	for _, b := range p {
		if len(d.held) == 0 && !d.atStart {
			out.WriteByte(b)
			d.atStart = b == '\n'
			continue
		}
		d.held = append(d.held, b)
		if strings.HasPrefix(d.line, string(d.held)) {
			if len(d.held) == len(d.line) {
				d.held = d.held[:0]
				d.atStart = true
			}
			continue
		}
		out.Write(d.held)
		d.held = d.held[:0]
		d.atStart = b == '\n'
	}
	if out.Len() > 0 {
		if _, err := d.w.Write(out.Bytes()); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// Flush writes any held-back bytes.
func (d *lineDropper) Flush() error {
	if len(d.held) == 0 {
		return nil
	}
	_, err := d.w.Write(d.held)
	d.held = d.held[:0]
	return err
}
