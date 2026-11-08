package selftune

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/harness"
)

// Workspace copy bounds.
const (
	MaxWorkspaceFiles = 50000
	MaxWorkspaceBytes = 512 << 20
)

// ValidateOptions configure Validate.
type ValidateOptions struct {
	Root     string
	ID       string
	Settings config.SelfTuning
	// RunAgent runs one workflow stage (harness.ExecAgent in the CLI, a
	// fake in tests).
	RunAgent harness.RunAgent
	// TempDir holds the throwaway workspaces; "" is the OS temp dir.
	TempDir  string
	Progress io.Writer
	Now      time.Time
}

type valTask struct {
	ev      Evidence
	control bool
}

// Validate reruns the candidate's failed tasks, then its control tasks,
// each once without the change (baseline) and once with it (candidate),
// every run in a fresh copy of --cwd outside the project, within the
// validation_runs and validation_cost_usd bounds. A task that passed at
// baseline and fails with the change is a regression and rejects the
// candidate; it is accepted only with no regression and at least one
// failed task fixed on fresh reruns.
func Validate(ctx context.Context, o ValidateOptions) (*Candidate, error) {
	if !o.Settings.Enabled {
		return nil, ErrOff
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	c, err := Load(o.Root, o.ID)
	if err != nil {
		return nil, err
	}
	switch c.Status {
	case StatusApplied:
		return nil, fmt.Errorf("candidate %s is applied; revert it before validating again", c.ID)
	case StatusReverted:
		return nil, fmt.Errorf("candidate %s was reverted; propose again to get a fresh candidate", c.ID)
	}
	tp, err := resolveTarget(o.Root, c.Target)
	if err != nil {
		return nil, err
	}
	cur, err := readRegular(tp, harness.MaxSkillBytes)
	if err != nil {
		return nil, err
	}
	if sha(string(cur)) != c.BaseSHA256 {
		return nil, fmt.Errorf("%s changed since candidate %s was proposed; propose again", c.Target, c.ID)
	}
	var tasks []valTask
	seen := map[string]bool{}
	for _, e := range c.Evidence {
		if k := sha(e.Task); !seen[k] {
			seen[k] = true
			tasks = append(tasks, valTask{ev: e})
		}
	}
	for _, e := range c.Controls {
		if k := sha(e.Task); !seen[k] {
			seen[k] = true
			tasks = append(tasks, valTask{ev: e, control: true})
		}
	}
	limitRuns, limitCost := o.Settings.EffValidationRuns(), o.Settings.EffValidationCostUSD()
	v := &Validation{At: stamp(o.Now)}
	incomplete := ""
	for _, t := range tasks {
		if v.Runs+2 > limitRuns {
			incomplete = fmt.Sprintf("self_tuning.validation_runs %d reached before every task ran", limitRuns)
			break
		}
		var pair [2]Outcome
		for i, variant := range []string{"baseline", "candidate"} {
			if v.CostUSD >= limitCost {
				incomplete = fmt.Sprintf("self_tuning.validation_cost_usd $%.2f reached", limitCost)
				break
			}
			if ctx.Err() != nil {
				incomplete = "interrupted"
				break
			}
			out, err := validateOne(ctx, o, c, t, variant)
			if err != nil {
				return nil, err
			}
			v.Runs++
			v.CostUSD += out.CostUSD
			v.Outcomes = append(v.Outcomes, out)
			pair[i] = out
			if o.Progress != nil {
				fmt.Fprintf(o.Progress, "tune: %s %s task %s (%s): %s\n", c.ID, variant, out.Task, out.Source, out.Status)
			}
		}
		if incomplete != "" {
			break
		}
		base, cand := pair[0].Status == harness.StatusPassed, pair[1].Status == harness.StatusPassed
		switch {
		case base && !cand:
			v.Regressions++
		case !base && cand && !t.control:
			v.Improvements++
		}
	}
	switch {
	case v.Regressions > 0:
		v.Verdict, v.Reason = VerdictRegression, fmt.Sprintf("%d task(s) that passed without the change failed with it", v.Regressions)
	case incomplete != "":
		v.Verdict, v.Reason = VerdictIncomplete, incomplete
	case v.Improvements == 0:
		v.Verdict, v.Reason = VerdictNoImprovement, "no failed task passed with the change on a fresh rerun (or the failure did not reproduce at baseline)"
	default:
		v.Verdict, v.Reason = VerdictAccepted, fmt.Sprintf("%d failed task(s) passed with the change, no regressions", v.Improvements)
	}
	c.Validation = v
	c.Status = StatusRejected
	if v.Verdict == VerdictAccepted {
		c.Status = StatusValidated
	}
	c.log(o.Now, "validated", v.Verdict+": "+v.Reason)
	if err := Save(o.Root, c); err != nil {
		return nil, err
	}
	return c, nil
}

// validateOne runs one task in a fresh workspace copy, with the change for
// the candidate variant. The copy is removed afterwards.
func validateOne(ctx context.Context, o ValidateOptions, c *Candidate, t valTask, variant string) (Outcome, error) {
	out := Outcome{Variant: variant, Task: sha(t.ev.Task)[:12], Source: t.ev.Path, Control: t.control}
	ws, err := os.MkdirTemp(o.TempDir, "tinyagent-tune-")
	if err != nil {
		return out, err
	}
	defer os.RemoveAll(ws)
	ws, err = filepath.EvalSymlinks(ws)
	if err != nil {
		return out, err
	}
	tree := filepath.Join(ws, "work")
	if err := CopyTree(o.Root, tree); err != nil {
		return out, fmt.Errorf("tune: copy --cwd into a workspace: %w", err)
	}
	if variant == "candidate" {
		tp, err := resolveTarget(tree, c.Target)
		if err != nil {
			return out, err
		}
		if err := writeAtomic(tp, []byte(c.After), 0o644); err != nil {
			return out, err
		}
	}
	w, _, err := loadWorkflow(tree, c.WorkflowFile)
	if err != nil {
		out.Status, out.Reason = "error", clean(err.Error(), MaxReason, nil)
		return out, nil
	}
	route, err := w.Route(t.ev.Task, c.Workflow)
	if err != nil {
		out.Status, out.Reason = "error", clean(err.Error(), MaxReason, nil)
		return out, nil
	}
	dir := filepath.Join(ws, "runs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return out, err
	}
	res := harness.Run(ctx, harness.Options{Route: route, Task: t.ev.Task, Cwd: tree, Dir: dir, RunAgent: o.RunAgent})
	out.Status, out.FailedStage, out.CostUSD = res.Status, res.FailedStage, res.CostUSD
	out.Reason = clean(res.Reason, MaxReason, nil)
	return out, nil
}

// CopyTree copies the project at src into dst for an isolated run: regular
// files and directories only. .git and tinyagent's own state under
// .tinyagent are left out (except the project rules file), and symlinks
// are skipped so nothing in the copy points back into the project or
// outside it. The copy is bounded by MaxWorkspaceFiles and
// MaxWorkspaceBytes.
func CopyTree(src, dst string) error {
	files, size := 0, int64(0)
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		slash := filepath.ToSlash(rel)
		if d.IsDir() {
			if slash == ".git" || (slash == ".tinyagent") {
				if slash == ".tinyagent" {
					return copyRules(p, filepath.Join(dst, rel))
				}
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		if !d.Type().IsRegular() {
			return nil // symlinks, sockets, devices
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		files++
		size += fi.Size()
		if files > MaxWorkspaceFiles || size > MaxWorkspaceBytes {
			return fmt.Errorf("the project is larger than the workspace limit (%d files, %d MB)", MaxWorkspaceFiles, MaxWorkspaceBytes>>20)
		}
		return copyFile(p, filepath.Join(dst, rel), fi.Mode().Perm())
	})
}

// copyRules copies .tinyagent/ratchet.toml, the only tinyagent state a
// fresh run reads, and skips the rest of .tinyagent.
func copyRules(srcDir, dstDir string) error {
	p := filepath.Join(srcDir, "ratchet.toml")
	fi, err := os.Lstat(p)
	if err == nil && fi.Mode().IsRegular() {
		if err := os.MkdirAll(dstDir, 0o755); err != nil {
			return err
		}
		if err := copyFile(p, filepath.Join(dstDir, "ratchet.toml"), fi.Mode().Perm()); err != nil {
			return err
		}
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return filepath.SkipDir
}

func copyFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode|0o200)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// summary is a one-line validation result.
func (v *Validation) summary() string {
	if v == nil {
		return "not validated"
	}
	return fmt.Sprintf("%s (%d runs, $%.4f, %d improved, %d regressed)", v.Verdict, v.Runs, v.CostUSD, v.Improvements, v.Regressions)
}

// Summary is c's one-line status.
func (c *Candidate) Summary() string {
	s := strings.Join([]string{c.ID, c.Status, c.Kind, c.Stage, c.Target}, "  ")
	if c.Validation != nil {
		s += "  " + c.Validation.summary()
	}
	return s
}
