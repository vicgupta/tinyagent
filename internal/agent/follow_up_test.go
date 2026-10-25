package agent

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// finishedRun runs one tool step and then ends with final, returning a
// finished trajectory (Submitted with the marker, or Completed without).
func finishedRun(t *testing.T, final model.Message) (*Agent, *trajectory.Trajectory) {
	t.Helper()
	a := newAgent(t, &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("w", "write_file", `{"path":"edited","content":"historical"}`)}},
		final,
	}})
	tr := a.Run(context.Background(), "review the code")
	if !trajectory.Finished(tr.ExitStatus) {
		t.Fatalf("setup: %s %s", tr.ExitStatus, tr.Submission)
	}
	return a, tr
}

// roundTrip saves tr and loads it back through the checkpoint loader.
func roundTrip(t *testing.T, tr *trajectory.Trajectory) *trajectory.Trajectory {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cp.json")
	if err := tr.Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := trajectory.Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return got
}

func TestFollowUpFinishedRun(t *testing.T) { // AC1, AC2, AC8, AC11
	for _, tc := range []struct {
		name   string
		final  model.Message
		status string
	}{
		{"submitted", model.Message{Role: "assistant", Content: "found two races\n" + SubmitMarker}, StatusSubmitted},
		{"completed", model.Message{Role: "assistant", Content: "found two races"}, StatusCompleted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, tr := finishedRun(t, tc.final)
			tr = roundTrip(t, tr)
			before := append([]model.Message(nil), tr.Messages...)
			prevSubmission, prevCost := tr.Submission, tr.Cost
			// The workspace changed after the run; the historical write must not replay.
			path := filepath.Join(a.Tools.Root, "edited")
			if err := os.WriteFile(path, []byte("changed since"), 0o600); err != nil {
				t.Fatal(err)
			}
			m := &scripted{replies: []model.Message{{Role: "assistant", Content: "fixed\n" + SubmitMarker}}}
			a.Model, a.Resume, a.FollowUp = m, tr, "fix the races you found"
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
				t.Fatalf("queries: %d (%s %s)", len(m.seen), got.ExitStatus, got.Submission)
			}
			sent := m.seen[0]
			if len(sent) != len(before)+1 || !reflect.DeepEqual(sent[:len(before)], before) {
				t.Fatal("finished history not sent intact")
			}
			last := sent[len(sent)-1]
			if last.Role != "user" || !strings.HasPrefix(last.Content, trajectory.FollowUpNotice) || !strings.HasSuffix(last.Content, "fix the races you found") {
				t.Fatalf("follow-up message: %+v", last)
			}
			if b, _ := os.ReadFile(path); string(b) != "changed since" {
				t.Fatal("historical tool executed again")
			}
			r := got.Resume
			if r == nil || !r.FollowUp || r.ExitStatus != tc.status || r.Submission != prevSubmission || r.Steps != 2 || r.Message != len(before) {
				t.Fatalf("provenance: %+v", r)
			}
			if got.ExitStatus != StatusSubmitted || got.Submission != "fixed" || got.Task != "review the code" {
				t.Fatalf("result: %s %q task %q", got.ExitStatus, got.Submission, got.Task)
			}
			if got.Steps != 3 || got.InputTokens != 300 || got.Cost <= prevCost {
				t.Fatalf("accounting: %+v", got)
			}
			if err := roundTrip(t, got).Validate(); err != nil {
				t.Fatalf("follow-up checkpoint invalid: %v", err)
			}
		})
	}
}

func TestFollowUpRequiresMatchingMode(t *testing.T) { // AC4, AC5
	a, tr := finishedRun(t, model.Message{Role: "assistant", Content: "done"})
	m := &scripted{}
	a.Model, a.Resume = m, tr
	if got := a.Run(context.Background(), ""); got.ExitStatus != StatusError || !strings.Contains(got.Submission, "follow-up") || len(m.seen) != 0 {
		t.Fatalf("finished without follow-up: %s %q", got.ExitStatus, got.Submission)
	}
	b, un := unfinishedRun(t, 1)
	b.Model, b.Resume, b.FollowUp = m, un, "more"
	if got := b.Run(context.Background(), ""); got.ExitStatus != StatusError || !strings.Contains(got.Submission, "finished checkpoint") || len(m.seen) != 0 {
		t.Fatalf("unfinished with follow-up: %s %q", got.ExitStatus, got.Submission)
	}
}

func TestFollowUpChainsAndResumes(t *testing.T) { // AC10, AC11
	a, tr := finishedRun(t, model.Message{Role: "assistant", Content: "one\n" + SubmitMarker})
	a.Model, a.Resume, a.FollowUp = &scripted{replies: []model.Message{{Role: "assistant", Content: "two\n" + SubmitMarker}}}, roundTrip(t, tr), "second"
	first := a.Run(context.Background(), "")
	if first.ExitStatus != StatusSubmitted {
		t.Fatalf("first follow-up: %s %s", first.ExitStatus, first.Submission)
	}
	a.Model, a.Resume, a.FollowUp = &scripted{replies: []model.Message{{Role: "assistant", Content: "three"}}}, roundTrip(t, first), "third"
	second := a.Run(context.Background(), "")
	if second.ExitStatus != StatusCompleted || second.Resume == nil || second.Resume.Previous == nil || !second.Resume.FollowUp || !second.Resume.Previous.FollowUp || second.Resume.Previous.Previous != nil {
		t.Fatalf("chain: %s %+v", second.ExitStatus, second.Resume)
	}
	if err := roundTrip(t, second).Validate(); err != nil {
		t.Fatal(err)
	}

	// An exhausted cumulative step ceiling makes no request.
	m := &scripted{}
	a.Model, a.Resume, a.FollowUp = m, roundTrip(t, second), "fourth"
	a.Config.StepLimit = second.Steps
	limited := a.Run(context.Background(), "")
	if limited.ExitStatus != StatusStepLimit || len(m.seen) != 0 {
		t.Fatalf("ceiling: %s, %d queries", limited.ExitStatus, len(m.seen))
	}
	// A follow-up stopped by a limit continues with plain resume.
	m = &scripted{replies: []model.Message{{Role: "assistant", Content: "five"}}}
	a.Model, a.Resume, a.FollowUp = m, roundTrip(t, limited), ""
	a.Config.StepLimit = 0
	resumed := a.Run(context.Background(), "")
	if resumed.ExitStatus != StatusCompleted || len(m.seen) != 1 || m.seen[0][len(m.seen[0])-1].Content != trajectory.ContinuationNotice {
		t.Fatalf("resume after follow-up: %s, %d queries", resumed.ExitStatus, len(m.seen))
	}
	if r := resumed.Resume; r == nil || r.FollowUp || r.Previous == nil || !r.Previous.FollowUp {
		t.Fatalf("resume provenance: %+v", r)
	}
	if err := roundTrip(t, resumed).Validate(); err != nil {
		t.Fatal(err)
	}
}

// A compacted finished run continues from its rebuilt compacted view plus
// the follow-up message.
func TestFollowUpCompactedRun(t *testing.T) { // AC12
	full := &fakeModel{summary: "## Task\nread big.txt", replies: []reply{{msg: reads(1, 1)}, {msg: reads(2, 3)}, {msg: reads(3, 1), input: 23000}, {msg: reads(4, 1)}, {msg: submit}}}
	a, _ := compactAgent(t, full, 30000)
	tr := a.Run(context.Background(), "the task")
	if tr.ExitStatus != StatusSubmitted || len(tr.Compactions) == 0 {
		t.Fatalf("setup: %s, %d compactions", tr.ExitStatus, len(tr.Compactions))
	}
	want := append(append([]model.Message(nil), full.agentRequests()[4].msgs...), submit)
	next := &fakeModel{summary: full.summary, replies: []reply{{msg: submit}}}
	r, _ := compactAgent(t, next, 30000)
	r.Tools.Root, r.Resume, r.FollowUp = a.Tools.Root, roundTrip(t, tr), "now add tests"
	if got := r.Run(context.Background(), ""); got.ExitStatus != StatusSubmitted {
		t.Fatalf("follow-up: %s %s", got.ExitStatus, got.Submission)
	}
	sent := next.agentRequests()[0].msgs
	if len(sent) != len(want)+1 || !reflect.DeepEqual(sent[:len(want)], want) || !strings.HasPrefix(sent[len(want)].Content, trajectory.FollowUpNotice) {
		t.Fatalf("follow-up view differs: got %d msgs, want %d + follow-up", len(sent), len(want))
	}
	if next.summaryRequests() != 0 {
		t.Fatal("follow-up re-summarized")
	}
}
