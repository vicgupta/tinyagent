// Command tinyagent is a minimal coding agent: an LLM with file and bash tools.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
	"github.com/vicgupta/tinyagent/internal/update"
)

// version is set at build time with -ldflags "-X main.version=v1.2.3".
var version string

func main() {
	os.Exit(run())
}

func run() int {
	var (
		task, modelName, configPath, cwd, output string
		stepLimit, timeLimit                     int
		costLimit                                float64
		verbose, noStream, showVersion, doUpdate bool
		noAgentsMD                               bool
	)
	fs := flag.NewFlagSet("tinyagent", flag.ContinueOnError)
	fs.StringVar(&task, "task", "", "task/prompt (required); - reads it from stdin")
	fs.StringVar(&task, "t", "", "shorthand for --task")
	fs.StringVar(&modelName, "model", defaultModel(), "provider/model, e.g. openai/gpt-4o")
	fs.StringVar(&modelName, "m", defaultModel(), "shorthand for --model")
	fs.StringVar(&configPath, "config", "", "yaml config path (default embedded)")
	fs.StringVar(&configPath, "c", "", "shorthand for --config")
	fs.StringVar(&cwd, "cwd", ".", "working directory")
	fs.IntVar(&stepLimit, "step-limit", 0, "max model calls, 0 = unlimited")
	fs.Float64Var(&costLimit, "cost-limit", 0, "max USD, 0 = unlimited")
	fs.IntVar(&timeLimit, "time-limit", 0, "max wall-clock seconds, 0 = unlimited; overrides wall_time_seconds")
	fs.StringVar(&output, "output", "", "trajectory JSON path")
	fs.StringVar(&output, "o", "", "shorthand for --output")
	fs.BoolVar(&verbose, "verbose", false, "print tool outputs")
	fs.BoolVar(&verbose, "v", false, "shorthand for --verbose")
	fs.BoolVar(&noStream, "no-stream", false, "wait for each full response instead of streaming it")
	fs.BoolVar(&noAgentsMD, "no-agents-md", false, "don't add <cwd>/AGENTS.md to the system prompt")
	fs.BoolVar(&showVersion, "version", false, "print the version and exit")
	fs.BoolVar(&doUpdate, "update", false, "replace this binary with the latest GitHub release and exit")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return 2
	}
	if timeLimit < 0 {
		fmt.Fprintln(os.Stderr, "error: --time-limit must be >= 0")
		return 2
	}
	if showVersion {
		fmt.Println("tinyagent", currentVersion())
		return 0
	}
	if doUpdate {
		u, err := update.New(currentVersion())
		if err != nil {
			return fail(err)
		}
		msg, err := u.Run(context.Background())
		if err != nil {
			return fail(err)
		}
		fmt.Println(msg)
		return 0
	}
	if task == "" {
		fmt.Fprintln(os.Stderr, "error: --task is required")
		fs.Usage()
		return 2
	}
	task, err := readTask(task, os.Stdin)
	if err != nil {
		return fail(err)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return fail(err)
	}
	applyFlagOverrides(fs, &cfg, stepLimit, costLimit, timeLimit)
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
		code := fail(err)
		printHint(os.Stderr, modelName, err.Error())
		return code
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
		Config:       cfg,
		Out:          os.Stdout,
		Err:          os.Stderr,
		Verbose:      verbose,
		Streamed:     !noStream,
		SkipAgentsMD: noAgentsMD,
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
	reportFailure(os.Stderr, modelName, t)
	return 1
}

// reportFailure prints how an unsuccessful run ended and, for an error that
// looks like a setup problem, a hint on fixing it.
func reportFailure(w io.Writer, modelName string, t *trajectory.Trajectory) {
	fmt.Fprintf(w, "%s after %d steps", t.ExitStatus, t.Steps)
	if t.ExitStatus != agent.StatusError {
		fmt.Fprintln(w)
		return
	}
	fmt.Fprintf(w, ": %s\n", t.Submission)
	printHint(w, modelName, t.Submission)
}

func printHint(w io.Writer, modelName, errMsg string) {
	if h := model.Hint(modelName, errMsg); h != "" {
		fmt.Fprintln(w, "hint:", h)
	}
}

// applyFlagOverrides copies limit flags into cfg, but only the ones given
// explicitly, so unset flags leave the config file's values alone.
func applyFlagOverrides(fs *flag.FlagSet, cfg *config.AgentConfig, stepLimit int, costLimit float64, timeLimit int) {
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "step-limit":
			cfg.StepLimit = stepLimit
		case "cost-limit":
			cfg.CostLimit = costLimit
		case "time-limit":
			cfg.WallTimeSec = timeLimit
		}
	})
}

// currentVersion prefers the -ldflags stamp, then the module version that
// `go install pkg@vX` records, then "dev".
func currentVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && strings.HasPrefix(bi.Main.Version, "v") {
		return bi.Main.Version
	}
	return "dev"
}

// readTask returns task, or the trimmed contents of stdin when task is "-".
func readTask(task string, stdin io.Reader) (string, error) {
	if task != "-" {
		return task, nil
	}
	b, err := io.ReadAll(stdin)
	if err != nil {
		return "", fmt.Errorf("reading task from stdin: %w", err)
	}
	if task = strings.TrimSpace(string(b)); task == "" {
		return "", errors.New("--task - got an empty task on stdin")
	}
	return task, nil
}

func defaultModel() string {
	if v := os.Getenv("AGENT_MODEL"); v != "" {
		return v
	}
	return "openai/gpt-4o"
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, "error:", err)
	return 1
}
