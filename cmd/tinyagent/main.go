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

// flagOutput receives flag parse errors and the usage shown with them;
// helpOutput receives --help. Tests replace both.
var (
	flagOutput io.Writer = os.Stderr
	helpOutput io.Writer = os.Stdout
)

// options holds the parsed command line.
type options struct {
	task, modelName, configPath, cwd, output string
	stepLimit, timeLimit                     int
	costLimit                                float64
	verbose, noStream, noAgentsMD            bool
	showHelp, showVersion, doUpdate          bool
}

// newFlagSet defines the command-line flags, storing their values in o. Each
// long name also works with a single dash (-task), as with any Go flag.
func newFlagSet(o *options) *flag.FlagSet {
	fs := flag.NewFlagSet("tinyagent", flag.ContinueOnError)
	fs.SetOutput(flagOutput)
	fs.Usage = func() { printUsage(fs.Output(), fs) }
	fs.BoolVar(&o.showHelp, "help", false, "show this help and exit")
	fs.BoolVar(&o.showHelp, "h", false, "shorthand for --help")
	fs.StringVar(&o.task, "task", "", `task/prompt (required); "-" reads it from stdin`)
	fs.StringVar(&o.task, "t", "", "shorthand for --task")
	fs.StringVar(&o.modelName, "model", defaultModel(), "provider/model")
	fs.StringVar(&o.modelName, "m", defaultModel(), "shorthand for --model")
	fs.StringVar(&o.configPath, "config", "", "YAML config (default: built-in)")
	fs.StringVar(&o.configPath, "c", "", "shorthand for --config")
	fs.StringVar(&o.cwd, "cwd", ".", "working directory")
	fs.IntVar(&o.stepLimit, "step-limit", 0, "max model calls, 0 = unlimited")
	fs.Float64Var(&o.costLimit, "cost-limit", 0, "max USD, 0 = unlimited")
	fs.IntVar(&o.timeLimit, "time-limit", 0, "max wall-clock seconds, 0 = unlimited")
	fs.StringVar(&o.output, "output", "", "write trajectory JSON")
	fs.StringVar(&o.output, "o", "", "shorthand for --output")
	fs.BoolVar(&o.verbose, "verbose", false, "print tool outputs")
	fs.BoolVar(&o.verbose, "v", false, "shorthand for --verbose")
	fs.BoolVar(&o.noStream, "no-stream", false, "wait for full responses instead of streaming")
	fs.BoolVar(&o.noAgentsMD, "no-agents-md", false, "don't add <cwd>/AGENTS.md to the system prompt")
	fs.BoolVar(&o.showVersion, "version", false, "print the version and exit")
	fs.BoolVar(&o.showVersion, "V", false, "shorthand for --version")
	fs.BoolVar(&o.doUpdate, "update", false, "update to the latest release and exit")
	return fs
}

func main() {
	os.Exit(run())
}

func run() int {
	var o options
	fs := newFlagSet(&o)
	if err := fs.Parse(os.Args[1:]); err != nil {
		return 2
	}
	if o.showHelp {
		printUsage(helpOutput, fs)
		return 0
	}
	if o.timeLimit < 0 {
		fmt.Fprintln(os.Stderr, "error: --time-limit must be >= 0")
		return 2
	}
	if o.showVersion {
		fmt.Println("tinyagent", currentVersion())
		return 0
	}
	if o.doUpdate {
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
	if o.task == "" {
		fmt.Fprintln(os.Stderr, "error: --task is required")
		fs.Usage()
		return 2
	}
	task, err := readTask(o.task, os.Stdin)
	if err != nil {
		return fail(err)
	}

	cfg, err := config.Load(o.configPath)
	if err != nil {
		return fail(err)
	}
	applyFlagOverrides(fs, &cfg, o.stepLimit, o.costLimit, o.timeLimit)
	model.PriceOverride = [2]float64{cfg.InputPricePerMTok, cfg.OutputPricePerMTok}

	root, err := filepath.Abs(o.cwd)
	if err != nil {
		return fail(err)
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return fail(fmt.Errorf("--cwd %s is not a directory", o.cwd))
	}

	var onText func(string)
	if !o.noStream {
		onText = func(s string) { os.Stdout.WriteString(s) }
	}
	m, err := model.New(o.modelName, onText)
	if err != nil {
		code := fail(err)
		printHint(os.Stderr, o.modelName, err.Error())
		return code
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	a := &agent.Agent{
		Model:     m,
		ModelName: o.modelName,
		Tools: &tools.Executor{
			Root:           root,
			BashTimeout:    time.Duration(cfg.BashTimeoutSec) * time.Second,
			MaxOutputChars: cfg.MaxOutputChars,
		},
		Config:       cfg,
		Out:          os.Stdout,
		Err:          os.Stderr,
		Verbose:      o.verbose,
		Streamed:     !o.noStream,
		SkipAgentsMD: o.noAgentsMD,
	}
	if o.output != "" {
		a.OnStep = func(t *trajectory.Trajectory) {
			if err := t.Save(o.output); err != nil {
				fmt.Fprintln(os.Stderr, "warning: saving trajectory:", err)
			}
		}
	}

	t := a.Run(ctx, task)
	if t.ExitStatus == agent.StatusSubmitted || t.ExitStatus == agent.StatusCompleted {
		return 0
	}
	reportFailure(os.Stderr, o.modelName, t)
	return 1
}

// usageGroups lays out --help: each flag's shorthand, long name and value
// placeholder, by group. Help text and defaults come from the FlagSet.
var usageGroups = []struct {
	name  string
	flags [][3]string
}{
	{"", [][3]string{{"h", "help", ""}}},
	{"Task", [][3]string{{"t", "task", "string"}, {"m", "model", "string"}, {"c", "config", "path"}, {"", "cwd", "dir"}}},
	{"Limits", [][3]string{{"", "step-limit", "n"}, {"", "cost-limit", "usd"}, {"", "time-limit", "sec"}}},
	{"Output", [][3]string{{"o", "output", "path"}, {"v", "verbose", ""}, {"", "no-stream", ""}, {"", "no-agents-md", ""}}},
	{"Other", [][3]string{{"V", "version", ""}, {"", "update", ""}}},
}

func printUsage(w io.Writer, fs *flag.FlagSet) {
	left := func(f [3]string) string {
		s := "      --" + f[1]
		if f[0] != "" {
			s = "  -" + f[0] + ", --" + f[1]
		}
		if f[2] != "" {
			s += " " + f[2]
		}
		return s
	}
	width := 0
	for _, g := range usageGroups {
		for _, f := range g.flags {
			width = max(width, len(left(f)))
		}
	}
	fmt.Fprint(w, "Usage: tinyagent -t \"<prompt>\" [flags]    (or --task \"<prompt>\")\n       echo \"<prompt>\" | tinyagent -t -\n\n")
	for _, g := range usageGroups {
		if g.name != "" {
			fmt.Fprintf(w, "%s:\n", g.name)
		}
		for _, f := range g.flags {
			fl := fs.Lookup(f[1])
			help := fl.Usage
			if d := fl.DefValue; d != "" && d != "0" && d != "false" {
				help += fmt.Sprintf(" (default %q)", d)
			}
			fmt.Fprintf(w, "%-*s%s\n", width+4, left(f), help)
		}
	}
	fmt.Fprint(w, "\nExamples:\n"+
		"  tinyagent -t \"fix the failing test\" -m anthropic/claude-sonnet-4\n"+
		"  cat task.md | tinyagent -t - --step-limit 30 -o run.json\n")
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
