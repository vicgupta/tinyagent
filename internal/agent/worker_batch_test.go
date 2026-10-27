package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/model"
)

// taskModel is a concurrency-safe fake for workers: each worker's replies
// come from the script whose key its task contains. before, when set, runs
// before every reply and may block or fail it.
type taskModel struct {
	mu      sync.Mutex
	scripts map[string][]model.Message
	before  func(ctx context.Context, key string) error
	cost    float64
}

func (m *taskModel) Query(ctx context.Context, msgs []model.Message, _ []model.ToolSpec) (model.Message, model.Cost, error) {
	key := ""
	m.mu.Lock()
	for k := range m.scripts {
		for _, msg := range msgs {
			if msg.Role == "user" && strings.Contains(msg.Content, k) {
				key = k
			}
		}
	}
	m.mu.Unlock()
	if m.before != nil {
		if err := m.before(ctx, key); err != nil {
			return model.Message{}, model.Cost{}, err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.scripts[key]
	if len(s) == 0 {
		return model.Message{}, model.Cost{}, errors.New("out of replies for " + key)
	}
	r := s[0]
	if len(s) > 1 {
		m.scripts[key] = s[1:]
	}
	c := m.cost
	if c == 0 {
		c = 0.01
	}
	return r, model.Cost{USD: c, InputTokens: 10, OutputTokens: 5}, nil
}

func writeAndSubmit(file string) []model.Message {
	return []model.Message{
		assistantCalls(call("c-"+file, "write_file", `{"path":"`+file+`","content":"x\n"}`)),
		{Role: "assistant", Content: "wrote " + file + submitMarker},
	}
}

func batchAgent(t *testing.T, parent []model.Message, tm *taskModel, max int) *Agent {
	t.Helper()
	a, _, _ := workerAgent(t, parent, nil)
	a.Workers.Model = tm
	a.Workers.MaxConcurrent = max
	return a
}

func TestWorkersRunConcurrently(t *testing.T) {
	var arrived sync.WaitGroup
	arrived.Add(2)
	var once sync.Map
	tm := &taskModel{scripts: map[string][]model.Message{"task-A": writeAndSubmit("a2.txt"), "task-B": writeAndSubmit("b2.txt")}}
	both := make(chan struct{})
	go func() { arrived.Wait(); close(both) }()
	tm.before = func(ctx context.Context, key string) error {
		if _, seen := once.LoadOrStore(key, true); seen {
			return nil
		}
		arrived.Done()
		select {
		case <-both:
			return nil
		case <-time.After(10 * time.Second):
			return errors.New("the other worker never ran at the same time")
		}
	}
	a := batchAgent(t, []model.Message{
		assistantCalls(workerCallMsg("p1", "task-A"), workerCallMsg("p2", "task-B")),
		assistantCalls(applyCallMsg("p3", "w1"), applyCallMsg("p4", "w2")),
		{Role: "assistant", Content: "done" + submitMarker},
	}, tm, 2)
	var mu sync.Mutex
	var kinds []string
	a.OnEvent = func(kind string, _ map[string]any) { mu.Lock(); kinds = append(kinds, kind); mu.Unlock() }
	tr := a.Run(context.Background(), "go")
	if tr.ExitStatus != StatusSubmitted {
		t.Fatalf("exit = %s %s", tr.ExitStatus, tr.Submission)
	}
	if len(tr.Subagents) != 2 || tr.Subagents[0].ID != "w1" || tr.Subagents[0].Task != "task-A" || tr.Subagents[1].ID != "w2" || tr.Subagents[1].Task != "task-B" {
		t.Fatalf("subagents = %+v", tr.Subagents)
	}
	res := toolResults(tr.Messages)
	if len(res) != 4 || !strings.Contains(res[0], "worker w1: Submitted") || !strings.Contains(res[1], "worker w2: Submitted") {
		t.Fatalf("results = %q", res)
	}
	if !strings.Contains(res[2], "applied worker w1") || !strings.Contains(res[3], "applied worker w2") {
		t.Errorf("apply results = %q", res[2:])
	}
	for _, s := range tr.Subagents {
		if s.Commit == "" || !s.Applied {
			t.Errorf("worker %s: %+v", s.ID, s)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(strings.Join(kinds, ","), "workers.batch") {
		t.Errorf("no workers.batch event in %v", kinds)
	}
}

func TestWorkersHonorMaxConcurrent(t *testing.T) {
	var running, peak atomic.Int32
	tm := &taskModel{scripts: map[string][]model.Message{
		"task-A": writeAndSubmit("a.out"), "task-B": writeAndSubmit("b.out"), "task-C": writeAndSubmit("c.out"),
	}}
	tm.before = func(ctx context.Context, key string) error {
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		running.Add(-1)
		return nil
	}
	a := batchAgent(t, []model.Message{
		assistantCalls(workerCallMsg("p1", "task-A"), workerCallMsg("p2", "task-B"), workerCallMsg("p3", "task-C")),
		{Role: "assistant", Content: "done" + submitMarker},
	}, tm, 2)
	tr := a.Run(context.Background(), "go")
	if tr.ExitStatus != StatusSubmitted || len(tr.Subagents) != 3 {
		t.Fatalf("exit = %s, %d workers", tr.ExitStatus, len(tr.Subagents))
	}
	if p := peak.Load(); p > 2 {
		t.Errorf("peak concurrent model calls = %d, cap is 2", p)
	}
	for i, s := range tr.Subagents {
		if s.Status != StatusSubmitted || s.Commit == "" {
			t.Errorf("worker %d: %+v", i, s)
		}
	}
}

func TestWorkersSharedCostLimitCancelsAll(t *testing.T) {
	loop := assistantCalls(call("c", "write_file", `{"path":"spin.txt","content":"x\n"}`))
	tm := &taskModel{scripts: map[string][]model.Message{"task-spend": {loop}, "task-wait": {loop}}}
	tm.before = func(ctx context.Context, key string) error {
		if key == "task-wait" {
			// Only cancellation ends this worker.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(20 * time.Second):
				return errors.New("never cancelled")
			}
		}
		time.Sleep(5 * time.Millisecond)
		return nil
	}
	a := batchAgent(t, []model.Message{
		assistantCalls(workerCallMsg("p1", "task-spend"), workerCallMsg("p2", "task-wait")),
		{Role: "assistant", Content: "done" + submitMarker},
	}, tm, 2)
	a.Config.CostLimit = 0.05 // the parent's first call spends 0.01
	a.Workers.MaxSteps = 1000
	start := time.Now()
	tr := a.Run(context.Background(), "go")
	if time.Since(start) > 15*time.Second {
		t.Fatal("workers were not cancelled")
	}
	if tr.ExitStatus != StatusCostLimit {
		t.Fatalf("exit = %s %s", tr.ExitStatus, tr.Submission)
	}
	if len(tr.Subagents) != 2 {
		t.Fatalf("subagents = %d", len(tr.Subagents))
	}
	for _, s := range tr.Subagents {
		if s.Status != StatusCostLimit {
			t.Errorf("worker %s status = %s", s.ID, s.Status)
		}
	}
	if res := toolResults(tr.Messages); len(res) != 2 {
		t.Errorf("results = %q", res)
	}
}

func TestWorkersInterruptCancelsAll(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var waiting atomic.Int32
	tm := &taskModel{scripts: map[string][]model.Message{"task-A": writeAndSubmit("a"), "task-B": writeAndSubmit("b")}}
	tm.before = func(c context.Context, key string) error {
		if waiting.Add(1) == 2 {
			cancel()
		}
		<-c.Done()
		return c.Err()
	}
	a := batchAgent(t, []model.Message{
		assistantCalls(workerCallMsg("p1", "task-A"), workerCallMsg("p2", "task-B")),
		{Role: "assistant", Content: "done" + submitMarker},
	}, tm, 4)
	tr := a.Run(ctx, "go")
	if tr.ExitStatus != StatusInterrupted {
		t.Fatalf("exit = %s %s", tr.ExitStatus, tr.Submission)
	}
	if len(tr.Subagents) != 2 {
		t.Fatalf("subagents = %d", len(tr.Subagents))
	}
}

func TestWorkersOneAtATimeByDefault(t *testing.T) {
	var running, peak atomic.Int32
	tm := &taskModel{scripts: map[string][]model.Message{"task-A": writeAndSubmit("a"), "task-B": writeAndSubmit("b")}}
	tm.before = func(ctx context.Context, key string) error {
		if n := running.Add(1); n > peak.Load() {
			peak.Store(n)
		}
		time.Sleep(10 * time.Millisecond)
		running.Add(-1)
		return nil
	}
	a := batchAgent(t, []model.Message{
		assistantCalls(workerCallMsg("p1", "task-A"), workerCallMsg("p2", "task-B")),
		{Role: "assistant", Content: "done" + submitMarker},
	}, tm, 0)
	var kinds []string
	a.OnEvent = func(kind string, _ map[string]any) { kinds = append(kinds, kind) }
	tr := a.Run(context.Background(), "go")
	if tr.ExitStatus != StatusSubmitted || len(tr.Subagents) != 2 {
		t.Fatalf("exit = %s, %d workers", tr.ExitStatus, len(tr.Subagents))
	}
	if peak.Load() != 1 {
		t.Errorf("peak = %d, want 1", peak.Load())
	}
	if strings.Contains(strings.Join(kinds, ","), "workers.batch") {
		t.Error("batched with max_concurrent 0")
	}
}
