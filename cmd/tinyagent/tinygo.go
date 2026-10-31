package main

// TinyGo builds Go plugin source into WebAssembly. It is an external,
// separately downloaded dependency (the TinyGo project's release), not
// part of TinyAgent: nothing of it is embedded in this binary or committed
// to the repository. See internal/tinygo.

import (
	"context"
	"fmt"
	"io"

	"github.com/vicgupta/tinyagent/internal/tinygo"
	"github.com/vicgupta/tinyagent/internal/tools"
)

// newTinygoManager is replaced in tests.
var newTinygoManager = tinygo.Default

// runTinygo handles --tinygo-install, --tinygo-update and --tinygo-status.
func runTinygo(o *options, stdout, stderr io.Writer) int {
	override := ""
	if _, cfg, err := loadConfig(o, o.cwd, io.Discard); err == nil {
		override = cfg.TinyGo
	} else if o.configPath != "" {
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}
	m, err := newTinygoManager(override)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	ctx := context.Background()
	switch {
	case o.tinygoStatus:
		s := m.Status()
		fmt.Fprintf(stdout, "pinned: TinyGo %s (%s), sha256 %s\n", s.Pinned, s.Platform, orNone(s.Checksum))
		if s.Installed {
			fmt.Fprintf(stdout, "installed: TinyGo %s at %s (checksum OK)\n", s.Pinned, m.Dir())
		} else {
			fmt.Fprintf(stdout, "installed: no (run tinyagent tinygo install)\n")
		}
		for _, v := range s.Others {
			fmt.Fprintf(stdout, "other cached version: %s (tinyagent tinygo update removes it)\n", v)
		}
		if s.Override != "" {
			fmt.Fprintf(stdout, "override: %s\n", s.Override)
		}
		fmt.Fprintf(stdout, "using: %s\n", orNone(s.Path))
		fmt.Fprintln(stdout, "TinyGo is a separate project downloaded by tinyagent, not part of it.")
		return 0
	case o.tinygoUpdate:
		bin, already, removed, err := m.Update(ctx)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
		for _, r := range removed {
			fmt.Fprintf(stdout, "removed old TinyGo %s\n", r)
		}
		if already {
			fmt.Fprintf(stdout, "TinyGo %s is up to date at %s\n", m.Version, bin)
		} else {
			fmt.Fprintf(stdout, "updated to TinyGo %s at %s (checksum OK)\n", m.Version, bin)
		}
		return 0
	default:
		bin, already, err := m.Install(ctx)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
		if already {
			fmt.Fprintf(stdout, "TinyGo %s is already installed at %s (checksum OK)\n", m.Version, bin)
		} else {
			fmt.Fprintf(stdout, "installed TinyGo %s at %s (checksum OK)\n", m.Version, bin)
		}
		return 0
	}
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// sandboxRunner runs a command under the OS sandbox: writes only inside
// writable, network denied. It fails when the host can't enforce that.
func sandboxRunner(ctx context.Context, writable, workdir string, argv, env []string, out io.Writer) error {
	sb, err := tools.NewSandbox(writable, nil, false, nil)
	if err != nil {
		return fmt.Errorf("building plugin source needs the OS sandbox: %w", err)
	}
	defer sb.Cleanup()
	cmd := sb.Command(ctx, argv)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = workdir, env, out, out
	return cmd.Run()
}

// goModuleCompiler builds Go plugin source with TinyGo (resolving the
// override, the cache or a first-use download) under the OS sandbox.
func goModuleCompiler(override string, w io.Writer) func(context.Context, map[string][]byte) ([]byte, error) {
	return func(ctx context.Context, files map[string][]byte) ([]byte, error) {
		m, err := newTinygoManager(override)
		if err != nil {
			return nil, err
		}
		bin, from, err := m.Resolve(ctx, true)
		if err != nil {
			return nil, err
		}
		if from == "download" {
			fmt.Fprintf(w, "downloaded TinyGo %s (a separate project, not part of tinyagent) to %s\n", m.Version, m.Dir())
		}
		return tinygo.Build(ctx, bin, files, sandboxRunner)
	}
}
