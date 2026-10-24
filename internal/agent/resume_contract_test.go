package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

func unfinishedRun(t *testing.T, steps int) (*Agent, *trajectory.Trajectory) {
	t.Helper()
	replies := make([]model.Message, steps)
	for i := range replies {
		replies[i] = model.Message{Role: "assistant", ToolCalls: []model.ToolCall{
			call("same", "write_file", `{"path":"edited","content":"historical"}`),
			call("read", "read_file", `{"path":"edited"}`),
		}}
	}
	a := newAgent(t, &scripted{replies: replies})
	a.Config.StepLimit = steps
	tr := a.Run(context.Background(), "finish the work")
	if tr.ExitStatus != StatusStepLimit {
		t.Fatalf("setup: %s", tr.ExitStatus)
	}
	return a, tr
}

type resumeUsageScript struct{ scripted }

func (s *resumeUsageScript) Query(ctx context.Context, msgs []model.Message, specs []model.ToolSpec) (model.Message, model.Cost, error) {
	msg, cost, err := s.scripted.Query(ctx, msgs, specs)
	cost.CacheReadTokens, cost.CacheWriteTokens = 5, 3
	return msg, cost, err
}

func TestResumeCheckpointContextAndAccounting(t *testing.T) {
	a, tr := unfinishedRun(t, 20)
	before := append([]model.Message(nil), tr.Messages...)
	tr.CacheReadTokens, tr.CacheWriteTokens = 9, 7
	baseCost := tr.Cost
	path := filepath.Join(a.Tools.Root, "edited")
	if err := os.WriteFile(path, []byte("inspected workspace"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &resumeUsageScript{scripted{replies: []model.Message{{Role: "assistant", Content: "done"}}}}
	a.Model, a.Resume, a.Config.StepLimit = m, tr, 40
	saves := 0
	a.OnStep = func(c *trajectory.Trajectory) error {
		saves++
		if saves == 1 && len(m.seen) != 0 {
			t.Fatal("queried before initial persistence")
		}
		return nil
	}
	got := a.Run(context.Background(), "")
	if len(m.seen) != 1 {
		t.Fatalf("queries: %d", len(m.seen))
	}
	msgs := m.seen[0]
	if len(msgs) != len(before)+1 || !reflect.DeepEqual(msgs[:len(before)], before) {
		t.Fatal("saved history not sent intact")
	}
	notice := msgs[len(msgs)-1]
	if notice.Role != "user" || !strings.Contains(notice.Content, "workspace") || !strings.Contains(notice.Content, "side effects") {
		t.Fatalf("missing recovery notice: %+v", notice)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "inspected workspace" {
		t.Fatal("historical tool executed again")
	}
	if got.Steps != 21 || got.InputTokens != 2100 || got.OutputTokens != 420 || got.Cost != baseCost+0.01 || got.CacheReadTokens != 14 || got.CacheWriteTokens != 10 {
		t.Fatalf("accounting changed: %+v", got)
	}
	if got.ExitStatus != StatusCompleted || saves != 2 {
		t.Fatalf("status %s, saves %d", got.ExitStatus, saves)
	}
}

func TestResumeExhaustedCeilingsAndCancellation(t *testing.T) {
	for _, which := range []string{"step", "cost", "cancelled"} {
		t.Run(which, func(t *testing.T) {
			a, tr := unfinishedRun(t, 1)
			m := &scripted{}
			a.Model, a.Resume = m, tr
			a.Config.StepLimit = 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := StatusStepLimit
			switch which {
			case "step":
				a.Config.StepLimit = 1
			case "cost":
				a.Config.CostLimit, want = tr.Cost, StatusCostLimit
			case "cancelled":
				cancel()
				want = StatusInterrupted
			}
			got := a.Run(ctx, "")
			if len(m.seen) != 0 || got.ExitStatus != want {
				t.Fatalf("queries %d, status %s", len(m.seen), got.ExitStatus)
			}
		})
	}
}

func TestResumeSaveFailureStopsWork(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run(string(rune('0'+failAt)), func(t *testing.T) {
			a, tr := unfinishedRun(t, 1)
			m := &scripted{replies: []model.Message{
				{Role: "assistant", ToolCalls: []model.ToolCall{call("new", "read_file", `{"path":"edited"}`)}},
				{Role: "assistant", ToolCalls: []model.ToolCall{call("forbidden", "write_file", `{"path":"must-not-exist","content":"bad"}`)}},
			}}
			a.Model, a.Resume, a.Config.StepLimit = m, tr, 5
			out := filepath.Join(t.TempDir(), "out.json")
			saves := 0
			a.OnStep = func(c *trajectory.Trajectory) error {
				saves++
				if saves == failAt {
					return errors.New("injected disk failure")
				}
				return c.Save(out)
			}
			got := a.Run(context.Background(), "")
			if got.ExitStatus != StatusError || len(m.seen) != failAt-1 || saves != failAt {
				t.Fatalf("status %s, queries %d, saves %d", got.ExitStatus, len(m.seen), saves)
			}
			if _, err := os.Stat(filepath.Join(a.Tools.Root, "must-not-exist")); !os.IsNotExist(err) {
				t.Fatal("continued after save failure")
			}
			if failAt == 2 {
				prev, err := trajectory.Load(out)
				if err != nil || prev.Steps != 1 {
					t.Fatalf("last valid checkpoint lost: %v", err)
				}
			}
		})
	}
}

type resumeDeadlineModel struct{ started time.Time }

func (m *resumeDeadlineModel) Query(ctx context.Context, _ []model.Message, _ []model.ToolSpec) (model.Message, model.Cost, error) {
	m.started = time.Now()
	<-ctx.Done()
	return model.Message{}, model.Cost{}, ctx.Err()
}

func TestResumeStartsANewWallTimeBudget(t *testing.T) {
	a, tr := unfinishedRun(t, 1)
	m := &resumeDeadlineModel{}
	a.Model, a.Resume = m, tr
	a.Config.StepLimit, a.Config.WallTimeSec = 2, 1
	start := time.Now()
	got := a.Run(context.Background(), "")
	if m.started.IsZero() || got.ExitStatus != StatusTimeLimit || time.Since(start) < 900*time.Millisecond {
		t.Fatalf("new wall-time budget was not used: started=%v, status=%s", m.started, got.ExitStatus)
	}
}

func TestResumeSummaryUsageRetainsCacheAccounting(t *testing.T) {
	a, tr := unfinishedRun(t, 1)
	a.Model = &resumeUsageScript{scripted{replies: []model.Message{{Role: "assistant", Content: "summary"}}}}
	tr.CacheReadTokens, tr.CacheWriteTokens = 9, 7
	if _, err := a.summarize(context.Background(), tr, tr.Task, tr.Messages[2:]); err != nil {
		t.Fatal(err)
	}
	if tr.InputTokens != 200 || tr.OutputTokens != 40 || tr.CacheReadTokens != 14 || tr.CacheWriteTokens != 10 {
		t.Fatalf("summary usage not accumulated: %+v", tr)
	}
}

func TestResumeFinalSaveFailureIsAnError(t *testing.T) {
	a, tr := unfinishedRun(t, 1)
	m := &scripted{replies: []model.Message{{Role: "assistant", Content: "done"}}}
	a.Model, a.Resume, a.Config.StepLimit = m, tr, 2
	path := filepath.Join(t.TempDir(), "continued.json")
	saves := 0
	a.OnStep = func(c *trajectory.Trajectory) error {
		saves++
		if saves == 2 {
			return errors.New("injected final save failure")
		}
		return c.Save(path)
	}
	got := a.Run(context.Background(), "")
	if got.ExitStatus != StatusError || saves != 2 || len(m.seen) != 1 {
		t.Fatalf("status %s, saves %d, requests %d", got.ExitStatus, saves, len(m.seen))
	}
	if last, err := trajectory.Load(path); err != nil || last.Steps != 1 {
		t.Fatalf("last checkpoint was lost: %v", err)
	}
}
