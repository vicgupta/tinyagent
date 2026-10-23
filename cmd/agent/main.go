// Command agent is a minimal coding agent: an LLM with file and bash tools.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

func main() {
	os.Exit(run())
}

func run() int {
	var (
		task, modelName, configPath, cwd, output string
		stepLimit                                int
		costLimit                                float64
		verbose, noStream                        bool
	)
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	fs.StringVar(&task, "task", "", "task/prompt (required)")
	fs.StringVar(&task, "t", "", "shorthand for --task")
	fs.StringVar(&modelName, "model", defaultModel(), "provider/model, e.g. openai/gpt-4o")
	fs.StringVar(&modelName, "m", defaultModel(), "shorthand for --model")
	fs.StringVar(&configPath, "config", "", "yaml config path (default embedded)")
	fs.StringVar(&configPath, "c", "", "shorthand for --config")
	fs.StringVar(&cwd, "cwd", ".", "working directory")
	fs.IntVar(&stepLimit, "step-limit", 0, "max model calls, 0 = unlimited")
	fs.Float64Var(&costLimit, "cost-limit", 0, "max USD, 0 = unlimited")
	fs.StringVar(&output, "output", "", "trajectory JSON path")
	fs.StringVar(&output, "o", "", "shorthand for --output")
	fs.BoolVar(&verbose, "verbose", false, "print tool outputs")
	fs.BoolVar(&verbose, "v", false, "shorthand for --verbose")
	fs.BoolVar(&noStream, "no-stream", false, "wait for each full response instead of streaming it")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return 2
	}
	if task == "" {
		fmt.Fprintln(os.Stderr, "error: --task is required")
		fs.Usage()
		return 2
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return fail(err)
	}
	// Flags override the config file only when given explicitly.
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "step-limit":
			cfg.StepLimit = stepLimit
		case "cost-limit":
			cfg.CostLimit = costLimit
		}
	})
	model.PriceOverride = [2]float64{cfg.InputPricePerMTok, cfg.OutputPricePerMTok}

	root, err := filepath.Abs(cwd)
	if err != nil {
		return fail(err)
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return fail(fmt.Errorf("--cwd %s is not a directory", cwd))
	}

	var onText func(string)
	if !noStream {
		onText = func(s string) { os.Stdout.WriteString(s) }
	}
	m, err := model.New(modelName, onText)
	if err != nil {
		return fail(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	a := &agent.Agent{
		Model:     m,
		ModelName: modelName,
		Tools: &tools.Executor{
			Root:           root,
			BashTimeout:    time.Duration(cfg.BashTimeoutSec) * time.Second,
			MaxOutputChars: cfg.MaxOutputChars,
		},
		Config:   cfg,
		Out:      os.Stdout,
		Verbose:  verbose,
		Streamed: !noStream,
	}
	if output != "" {
		a.OnStep = func(t *trajectory.Trajectory) {
			if err := t.Save(output); err != nil {
				fmt.Fprintln(os.Stderr, "warning: saving trajectory:", err)
			}
		}
	}

	t := a.Run(ctx, task)
	if t.ExitStatus == agent.StatusSubmitted || t.ExitStatus == agent.StatusCompleted {
		return 0
	}
	fmt.Fprintf(os.Stderr, "%s after %d steps", t.ExitStatus, t.Steps)
	if t.ExitStatus == agent.StatusError {
		fmt.Fprintf(os.Stderr, ": %s", t.Submission)
	}
	fmt.Fprintln(os.Stderr)
	return 1
}

func defaultModel() string {
	for _, k := range []string{"AGENT_MODEL", "MSWEA_MODEL_NAME"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return "openai/gpt-4o"
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, "error:", err)
	return 1
}
