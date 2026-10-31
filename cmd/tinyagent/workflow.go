package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/vicgupta/tinyagent/internal/eventlog"
	"github.com/vicgupta/tinyagent/internal/harness"
)

// workflowConflicts are flags that make no sense with --workflow: stages
// run unattended in --cwd, one fresh run each.
var workflowConflicts = []string{"resume", "rewind", "to", "dry-run", "transcript", "review", "review-base",
	"review-commit", "worktree", "acp", "check", "eval", "image", "ask-user", "output-schema", "plan-file",
	"list-profiles", "init-config", "update", "version", "completion", "skills-validate"}

// workflowOwn are flags --workflow consumes; every other flag set on the
// command line is forwarded to each stage's run.
var workflowOwn = []string{"workflow", "workflow-name", "t", "task", "o", "output", "cwd", "json"}

// runWorkflowAgent runs one stage; tests replace it with a fake.
var runWorkflowAgent = func() (harness.RunAgent, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return harness.ExecAgent(exe), nil
}

func runWorkflow(o *options, fs *flag.FlagSet, args []string, stdout, stderr io.Writer) int {
	for _, f := range workflowConflicts {
		if fs.Lookup(f) != nil && flagWasSet(fs, f) {
			fmt.Fprintf(stderr, "error: --workflow can't be combined with -%s: workflow stages run unattended, one fresh run each\n", dashName(f))
			return 2
		}
	}
	if o.task == "" {
		fmt.Fprintln(stderr, "error: --workflow needs a task (-t)")
		return 2
	}
	w, err := harness.Load(o.workflow)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}
	task, err := readTask(o.task, os.Stdin)
	if err != nil {
		return fail(err)
	}
	route, err := w.Route(task, o.workflowName)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}
	cwd, err := filepath.Abs(o.cwd)
	if err == nil {
		var fi os.FileInfo
		if fi, err = os.Stat(cwd); err == nil && !fi.IsDir() {
			err = fmt.Errorf("%s is not a directory", cwd)
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, "error: --cwd:", err)
		return 2
	}
	runAgent, err := runWorkflowAgent()
	if err != nil {
		return fail(err)
	}
	dir, keep := "", o.output != ""
	if keep {
		dir = o.output + ".stages"
		err = os.MkdirAll(dir, 0o700)
	} else {
		dir, err = os.MkdirTemp("", "tinyagent-workflow-")
	}
	if err != nil {
		return fail(err)
	}
	if !keep {
		defer os.RemoveAll(dir)
	}
	var jl *eventlog.JSONL
	if o.jsonOutput {
		jl = eventlog.NewJSONL(stdout)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintf(stderr, "workflow %s (%s, %s): %d stage(s)\n", route.Name, route.Kind, route.How, len(route.Pipeline.Stages))
	res := harness.Run(ctx, harness.Options{
		Route: route, Task: task, Cwd: cwd, ExtraArgs: forwardArgs(fs, args, workflowOwn),
		Dir: dir, RunAgent: runAgent, Progress: stderr,
		Emit: func(kind string, f map[string]any) {
			if jl != nil {
				jl.Emit(kind, f)
			}
		},
	})
	if !keep {
		for i := range res.Attempts {
			res.Attempts[i].Trajectory, res.Attempts[i].Log = "", ""
		}
	}
	if o.output != "" {
		b, _ := json.MarshalIndent(res, "", "  ")
		if err := os.WriteFile(o.output, append(b, '\n'), 0o600); err != nil {
			fmt.Fprintln(stderr, "error: write --output:", err)
			return 1
		}
	}
	if jl != nil && jl.Err() != nil && !errors.Is(jl.Err(), io.ErrClosedPipe) {
		fmt.Fprintln(stderr, "error: write events:", jl.Err())
	}
	if res.Status != harness.StatusPassed {
		fmt.Fprintf(stderr, "workflow %s %s at stage %s: %s\n", route.Name, res.Status, res.FailedStage, res.Reason)
		if res.Status == harness.StatusBlocked {
			fmt.Fprintln(stderr, "blocked: no more attempts will run; fix the task or its acceptance criteria, then re-run the same tinyagent --workflow command")
		}
	} else if !o.jsonOutput {
		fmt.Fprintln(stdout, res.Output)
	}
	return res.ExitCode()
}
