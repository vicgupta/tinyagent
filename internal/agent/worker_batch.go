package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/vicgupta/tinyagent/internal/hooks"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/permissions"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// maxConcurrent is the configured cap on workers running at once.
func (w *Workers) maxConcurrent() int {
	if w == nil || w.MaxConcurrent < 1 {
		return 1
	}
	return w.MaxConcurrent
}

// errWorkersCostLimit cancels a batch whose workers together reached the
// run's cost limit.
var errWorkersCostLimit = errors.New("the workers together reached the cost limit")

// errWorkersTokenLimit cancels a batch whose workers together reached the
// run's token limit.
var errWorkersTokenLimit = errors.New("the workers together reached the token limit")

// workerBatch holds the results of a response's worker calls that ran
// concurrently, for the tool loop to record in call order.
type workerBatch struct {
	results map[string]tools.Result
	// stop is the status the batch leaves for the parent, set once the
	// tool loop reaches stopAt, the last batched call.
	stop, stopAt string
}

// batched returns call's precomputed result, leaving the batch's stop
// status for the parent at its last call; ok is false for any other call.
func (a *Agent) batched(b *workerBatch, call model.ToolCall) (tools.Result, bool) {
	if b == nil {
		return tools.Result{}, false
	}
	res, ok := b.results[call.ID]
	if ok && call.ID == b.stopAt && b.stop != "" {
		a.delegateStop = b.stop
	}
	return res, ok
}

// runWorkerBatch runs a response's well-formed worker calls concurrently,
// at most Workers.MaxConcurrent at a time, when there are two or more and
// the cap allows it; it returns nil otherwise. Permission checks, pre-tool
// hooks, worktree creation, commits and post-tool hooks run one call at a
// time in call order; only the workers' own runs overlap. When the workers
// together reach the run's cost limit, or the run is interrupted or times
// out, every running worker is cancelled.
func (a *Agent) runWorkerBatch(ctx context.Context, t *trajectory.Trajectory, calls []model.ToolCall) *workerBatch {
	if !a.workersOn() || a.Workers == nil || a.Workers.Model == nil || a.Workers.maxConcurrent() < 2 {
		return nil
	}
	var batch []model.ToolCall
	for _, c := range calls {
		if c.Function.Name == tools.WorkerToolName && a.Tools.FormatError(c) == "" {
			batch = append(batch, c)
		}
	}
	if len(batch) < 2 {
		return nil
	}
	b := &workerBatch{results: map[string]tools.Result{}, stopAt: batch[len(batch)-1].ID}

	// Children report through these, so concurrent workers never
	// interleave writes or events.
	var mu sync.Mutex
	parentEvent := a.OnEvent
	if parentEvent != nil {
		a.OnEvent = func(kind string, fields map[string]any) {
			mu.Lock()
			defer mu.Unlock()
			parentEvent(kind, fields)
		}
		defer func() { a.OnEvent = parentEvent }()
	}

	bctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	gate := &costGate{spent: t.Cost, limit: a.Config.CostLimit, tokens: usedTokens(t), tokenLimit: a.Config.TokenLimit, cancel: cancel}

	var jobs []*workerJob
	for _, c := range batch {
		if res, decided := a.permit(ctx, t, c); decided {
			b.results[c.ID] = res
			continue
		}
		if res, denied := a.preWorkerHooks(ctx, t, c); denied {
			b.results[c.ID] = res
			continue
		}
		j, res, ok := a.prepareWorker(t, c, a.nextWorkerID(t, len(jobs)))
		if !ok {
			b.results[c.ID] = res
			if s := a.takeDelegateStop(); s != "" {
				b.stop = s
			}
			continue
		}
		j.child.Model = &gatedModel{Model: j.child.Model, gate: gate}
		j.child.Out = &lockedWriter{w: j.child.Out, mu: &mu}
		j.child.Err = &lockedWriter{w: j.child.Err, mu: &mu}
		if j.child.Tools.CommandLog != nil {
			j.child.Tools.CommandLog = &lockedWriter{w: j.child.Tools.CommandLog, mu: &mu}
		}
		if ap := j.child.PermissionApprover; ap != nil {
			var askMu sync.Mutex
			j.child.PermissionApprover = func(ctx context.Context, req permissions.Request) permissions.Answer {
				askMu.Lock()
				defer askMu.Unlock()
				return ap(ctx, req)
			}
		}
		jobs = append(jobs, j)
	}

	sem := make(chan struct{}, a.Workers.maxConcurrent())
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		go func(j *workerJob) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-bctx.Done():
			}
			j.ct = j.child.Run(bctx, j.task)
			switch j.ct.ExitStatus {
			case StatusInterrupted, StatusTimeLimit, StatusCostLimit, StatusTokenLimit:
				// A shared limit: stop the other workers too.
				cancel(fmt.Errorf("worker %s stopped: %s", j.id, j.ct.ExitStatus))
			}
		}(j)
	}
	wg.Wait()

	costHit := errors.Is(context.Cause(bctx), errWorkersCostLimit)
	tokenHit := errors.Is(context.Cause(bctx), errWorkersTokenLimit)
	for _, j := range jobs {
		if costHit && j.ct.ExitStatus == StatusInterrupted {
			j.ct.ExitStatus = StatusCostLimit
		}
		if tokenHit && j.ct.ExitStatus == StatusInterrupted {
			j.ct.ExitStatus = StatusTokenLimit
		}
		res := a.finishWorker(t, j)
		b.results[j.call.ID] = a.postWorkerHooks(ctx, t, j.call, res)
		if s := a.takeDelegateStop(); s != "" && b.stop == "" {
			b.stop = s
		}
	}
	// The parent's own deadline or interrupt wins over a stop one worker
	// passed on to the others.
	if s, ok := ctxStatus(ctx); ok {
		b.stop = s
	} else if costHit {
		b.stop = StatusCostLimit
	} else if tokenHit {
		b.stop = StatusTokenLimit
	}
	a.event("workers.batch", map[string]any{"workers": len(jobs), "max_concurrent": a.Workers.maxConcurrent(), "stop": b.stop})
	return b
}

// preWorkerHooks runs call's pre_tool hooks as runTool does; denied is
// true with the result when one denies it.
func (a *Agent) preWorkerHooks(ctx context.Context, t *trajectory.Trajectory, call model.ToolCall) (tools.Result, bool) {
	if a.Hooks == nil || !a.Tools.Permits(call.Function.Name) {
		return tools.Result{}, false
	}
	for _, r := range a.Hooks.Pre(ctx, a.workerHookInput(t, call)) {
		a.hookRan(call, r)
		if r.Denied {
			return tools.Result{Text: tools.Truncate(fmt.Sprintf("error: denied by pre_tool hook %s: %s", r.Command, r.Reason), a.Tools.MaxOutputChars), IsError: true}, true
		}
	}
	return tools.Result{}, false
}

// postWorkerHooks runs call's post_tool hooks on res as runTool does.
func (a *Agent) postWorkerHooks(ctx context.Context, t *trajectory.Trajectory, call model.ToolCall, res tools.Result) tools.Result {
	if a.Hooks == nil || !a.Tools.Permits(call.Function.Name) {
		return res
	}
	in := a.workerHookInput(t, call)
	in.OutputFile = res.OutputFile
	var feedback strings.Builder
	for _, r := range a.Hooks.Post(ctx, in, res.Text, res.IsError) {
		a.hookRan(call, r)
		feedback.WriteString(r.Feedback)
	}
	if feedback.Len() > 0 {
		fb, n := a.Tools.Redactor.RedactCount(feedback.String())
		res.Redactions += n
		res.Text = withFeedback(res, fb, a.Tools.MaxOutputChars)
	}
	return res
}

func (a *Agent) workerHookInput(t *trajectory.Trajectory, call model.ToolCall) hooks.Input {
	return hooks.Input{
		ToolName: call.Function.Name, ToolCallID: call.ID, Arguments: eventArguments(call.Function.Arguments),
		Root: a.Tools.Root, ReadOnly: a.Tools.ReadOnly, Step: t.Steps,
		Subagent: a.subagentID, Worker: a.isWorker,
	}
}

// costGate tracks what a batch's parent and workers have spent together
// and cancels the batch once that reaches the run's cost limit.
type costGate struct {
	mu     sync.Mutex
	spent  float64
	limit  float64
	cancel context.CancelCauseFunc
	// tokens and tokenLimit track input plus output tokens the same way.
	tokens     int
	tokenLimit int
}

func (g *costGate) add(c model.Cost) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.spent += c.USD
	g.tokens += c.InputTokens + c.OutputTokens
	if g.limit > 0 && g.spent >= g.limit {
		g.cancel(errWorkersCostLimit)
	}
	if g.tokenLimit > 0 && g.tokens >= g.tokenLimit {
		g.cancel(errWorkersTokenLimit)
	}
}

// gatedModel adds each response's cost to a batch's costGate.
type gatedModel struct {
	model.Model
	gate *costGate
}

func (m *gatedModel) Query(ctx context.Context, msgs []model.Message, specs []model.ToolSpec) (model.Message, model.Cost, error) {
	msg, cost, err := m.Model.Query(ctx, msgs, specs)
	m.gate.add(cost)
	return msg, cost, err
}

// lockedWriter serializes writes to w across a batch's workers.
type lockedWriter struct {
	w  io.Writer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
