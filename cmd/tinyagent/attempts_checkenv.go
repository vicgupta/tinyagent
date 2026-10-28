package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/vicgupta/tinyagent/internal/attempts"
	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/tools"
)

// attemptCheckPlace names where --attempts checks run for cfg (after the
// command-line overrides): "container", "sandbox", "command_env" or "host".
func attemptCheckPlace(cfg config.AgentConfig) string {
	switch {
	case cfg.Environment == "container":
		return "container"
	case cfg.Sandbox:
		return "sandbox"
	case cfg.CommandEnv.Configured():
		return "command_env"
	}
	return "host"
}

// attemptCheckEnv returns the attempts.Options.CheckEnv that runs an
// attempt's checks the way the attempt's own commands ran: in a fresh
// container from the configured image with the attempt's worktree (and the
// additional directories) mounted, under a sandbox rooted at the worktree,
// or with the command_env policy. Setting the environment up never falls
// back to the host: an error fails that attempt's checks. It returns nil
// when commands run on the host unconfined, so checks keep tinyagent's
// environment.
func attemptCheckEnv(fs *flag.FlagSet, o options, cfg config.AgentConfig, stderr io.Writer) func(context.Context, string) (attempts.CheckRunner, func(), error) {
	place := attemptCheckPlace(cfg)
	if place == "host" {
		return nil
	}
	return func(ctx context.Context, root string) (attempts.CheckRunner, func(), error) {
		extra, err := additionalDirs(fs, o, cfg, root, false)
		if err != nil {
			return nil, nil, err
		}
		switch place {
		case "container":
			c, err := tools.StartContainer(ctx, cfg.Container.Runtime, cfg.Container.Image, cfg.Container.RunArgs, root, extra)
			if err != nil {
				return nil, nil, err
			}
			return c, func() {
				if err := c.Close(); err != nil {
					fmt.Fprintln(stderr, "warning:", err)
				}
			}, nil
		case "sandbox":
			var ce *tools.CommandEnv
			if cfg.CommandEnv.Configured() {
				ce = tools.NewCommandEnv(cfg.CommandEnv, os.Environ())
			}
			writable := append([]string(nil), cfg.SandboxWritablePaths...)
			for _, d := range extra {
				if d.Mode == tools.DirWrite {
					writable = append(writable, d.Path)
				}
			}
			sb, err := tools.NewSandbox(root, writable, cfg.SandboxNetwork, cfg.SandboxAllowedDomains)
			if err != nil {
				return nil, nil, err
			}
			return tools.Local{Sandbox: sb, CommandEnv: ce}, sb.Cleanup, nil
		default:
			return tools.Local{CommandEnv: tools.NewCommandEnv(cfg.CommandEnv, os.Environ())}, func() {}, nil
		}
	}
}
