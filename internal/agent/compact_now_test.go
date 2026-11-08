package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// threeTurnHistory is a short conversation long enough for /compact
// (reason "requested") to summarize older turns: system, task, then
// three assistant replies with user follow-ups.
func threeTurnHistory() *trajectory.Trajectory {
	return &trajectory.Trajectory{
		Steps: 3,
		Messages: []model.Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "the task"},
			{Role: "assistant", Content: "step 1"},
			{Role: "user", Content: "two"},
			{Role: "assistant", Content: "step 2"},
			{Role: "user", Content: "three"},
			{Role: "assistant", Content: "step 3"},
		},
	}
}

func TestCompactNowRunsImmediately(t *testing.T) {
	f := &fakeModel{summary: "## Task\nthe task\n## Next steps\ncontinue"}
	a, stderr := compactAgent(t, f, 30000)
	tr := threeTurnHistory()
	orig := append([]model.Message(nil), tr.Messages...)
	var ev []map[string]any
	a.OnEvent = func(kind string, fields map[string]any) {
		if kind == "compaction" {
			ev = append(ev, fields)
		}
	}

	if err := a.CompactNow(context.Background(), tr); err != nil {
		t.Fatal(err)
	}
	if f.summaryRequests() != 1 {
		t.Fatalf("summary requests = %d, want 1 (compaction must run now, not later)", f.summaryRequests())
	}
	if !reflect.DeepEqual(tr.Messages, orig) {
		t.Error("stored history changed; compaction must be append-only")
	}
	if len(tr.Compactions) != 1 {
		t.Fatalf("compactions = %+v", tr.Compactions)
	}
	c := tr.Compactions[0]
	if c.Reason != "requested" || c.Stage != "summarize" || c.TokensBefore <= 0 || c.TokensAfter <= 0 || len(c.Summarized) == 0 {
		t.Errorf("compaction = %+v", c)
	}
	if !strings.Contains(stderr.String(), "compacted context:") || !strings.Contains(stderr.String(), "→") {
		t.Errorf("stderr = %q", stderr.String())
	}
	if len(ev) != 1 || ev[0]["reason"] != "requested" || ev[0]["tokens_before"] == nil || ev[0]["tokens_after"] == nil {
		t.Errorf("compaction event = %v", ev)
	}
}

func TestCompactNowNothingToCompact(t *testing.T) {
	f := &fakeModel{summary: "should not be called"}
	a, stderr := compactAgent(t, f, 30000)
	tr := &trajectory.Trajectory{
		Steps: 1,
		Messages: []model.Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "the task"},
			{Role: "assistant", Content: "done"},
		},
	}
	orig := append([]model.Message(nil), tr.Messages...)
	if err := a.CompactNow(context.Background(), tr); err != nil {
		t.Fatal(err)
	}
	if f.summaryRequests() != 0 || len(tr.Compactions) != 0 {
		t.Errorf("summary requests = %d, compactions = %+v", f.summaryRequests(), tr.Compactions)
	}
	if !reflect.DeepEqual(tr.Messages, orig) {
		t.Error("stored history changed")
	}
	if !strings.Contains(stderr.String(), "nothing to compact yet") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestCompactNowErrorLeavesHistory(t *testing.T) {
	fail := &failingSummary{err: errors.New("model down")}
	a, _ := compactAgent(t, &fakeModel{}, 30000)
	a.Model = fail
	tr := threeTurnHistory()
	orig := append([]model.Message(nil), tr.Messages...)
	comps := append([]trajectory.Compaction(nil), tr.Compactions...)

	err := a.CompactNow(context.Background(), tr)
	if err == nil || !strings.Contains(err.Error(), "model down") {
		t.Fatalf("error = %v", err)
	}
	if !reflect.DeepEqual(tr.Messages, orig) {
		t.Error("stored messages changed after a failed compaction")
	}
	if !reflect.DeepEqual(tr.Compactions, comps) {
		t.Errorf("compactions changed after a failed compaction: %+v", tr.Compactions)
	}
}

func TestCompactNowNoCompact(t *testing.T) {
	a, _ := compactAgent(t, &fakeModel{}, 30000)
	a.NoCompact = true
	tr := threeTurnHistory()
	if err := a.CompactNow(context.Background(), tr); err == nil || !strings.Contains(err.Error(), "--no-compact") {
		t.Fatalf("error = %v", err)
	}
	if len(tr.Compactions) != 0 {
		t.Errorf("compactions = %+v", tr.Compactions)
	}
}

type failingSummary struct{ err error }

func (f *failingSummary) Query(context.Context, []model.Message, []model.ToolSpec) (model.Message, model.Cost, error) {
	return model.Message{}, model.Cost{}, f.err
}
