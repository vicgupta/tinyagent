package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// The "append, don't edit" invariant (#380): once a message has been sent to
// the model, every later request repeats it byte for byte, in the same
// position, so provider prompt caches keep hitting and the model's earlier
// reasoning stays valid. New context (tool results, notices, resume and
// follow-up messages, compaction summaries) is only ever appended. The one
// deliberate exception is compaction itself, which replaces the history it
// compacts; between compactions the invariant holds again. See
// docs/long-runs.md#append-only-history-and-prompt-caching.

// hashMsgs hashes msgs as JSON, so equal hashes mean byte-identical messages
// as far as any provider serialization can tell.
func hashMsgs(t *testing.T, msgs []model.Message) string {
	t.Helper()
	b, err := json.Marshal(msgs)
	if err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func hashTools(t *testing.T, specs []model.ToolSpec) string {
	t.Helper()
	b, err := json.Marshal(specs)
	if err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// checkPrefixStable fails unless request k+1 starts with every message of
// request k, byte for byte, for each k where skip(k+1) is false. The tool
// definitions must be identical in every request.
func checkPrefixStable(t *testing.T, reqs []request, skip func(int) bool) {
	t.Helper()
	if len(reqs) < 2 {
		t.Fatalf("only %d requests", len(reqs))
	}
	tools := hashTools(t, reqs[0].tools)
	for k := 1; k < len(reqs); k++ {
		if got := hashTools(t, reqs[k].tools); got != tools {
			t.Errorf("request %d: tool definitions changed", k)
		}
		if skip != nil && skip(k) {
			continue
		}
		prev, cur := reqs[k-1].msgs, reqs[k].msgs
		if len(cur) <= len(prev) {
			t.Errorf("request %d: %d messages, previous had %d; history must only grow", k, len(cur), len(prev))
			continue
		}
		if hashMsgs(t, cur[:len(prev)]) != hashMsgs(t, prev) {
			for i := range prev {
				if hashMsgs(t, prev[i:i+1]) != hashMsgs(t, cur[i:i+1]) {
					t.Errorf("request %d rewrote already-sent message %d (%s):\n was %.200q\n now %.200q", k, i, prev[i].Role, prev[i].Content, cur[i].Content)
					break
				}
			}
		}
	}
}

// checkSystemStable fails unless every request sends the same system prompt
// bytes, containing want.
func checkSystemStable(t *testing.T, reqs []request, want string) {
	t.Helper()
	sys := reqs[0].msgs[0]
	if sys.Role != "system" || !strings.Contains(sys.Content, want) {
		t.Fatalf("system prompt lacks %q: %.300q", want, sys.Content)
	}
	for k, r := range reqs {
		if r.msgs[0].Content != sys.Content || r.msgs[0].Role != "system" {
			t.Errorf("request %d: system prompt bytes changed", k)
		}
	}
}

const appendOnlyRules = "Always run go test before submitting."

func appendOnlyAgent(t *testing.T, f *fakeModel, window int) *Agent {
	t.Helper()
	a, _ := compactAgent(t, f, window)
	if err := os.WriteFile(filepath.Join(a.Tools.Root, "AGENTS.md"), []byte(appendOnlyRules), 0o644); err != nil {
		t.Fatal(err)
	}
	return a
}

// Ordinary steps (successful, failing, denied and unknown tool calls, and a
// plain-text turn that gets the submit reminder) only append: the request
// prefix hashes the same before and after each step, the system prompt and
// AGENTS.md bytes never change, and the model's replies are stored and resent
// exactly as returned.
func TestRequestPrefixHashStableAcrossSteps(t *testing.T) {
	f := &fakeModel{replies: []reply{
		{msg: reads(1, 2)},
		{msg: model.Message{Role: "assistant", Content: "edit", ToolCalls: []model.ToolCall{call("w", "write_file", `{"path":"out.txt","content":"hi"}`)}}},
		{msg: model.Message{Role: "assistant", ToolCalls: []model.ToolCall{call("e", "read_file", `{"path":"missing.txt"}`)}}},
		{msg: model.Message{Role: "assistant", ToolCalls: []model.ToolCall{call("u", "no_such_tool", `{}`)}}},
		{msg: model.Message{Role: "assistant", ToolCalls: []model.ToolCall{call("m", "read_file", `{not json`)}}},
		{msg: submit},
	}}
	a := appendOnlyAgent(t, f, 0)
	tr := a.Run(context.Background(), "the task")
	if tr.ExitStatus != StatusSubmitted {
		t.Fatalf("status = %s: %s", tr.ExitStatus, tr.Submission)
	}
	reqs := f.agentRequests()
	if len(reqs) != 6 {
		t.Fatalf("%d requests, want 6", len(reqs))
	}
	checkPrefixStable(t, reqs, nil)
	checkSystemStable(t, reqs, appendOnlyRules)

	// Each reply is resent verbatim right after the request that produced it.
	script := []model.Message{reads(1, 2)}
	for k := 0; k+1 < len(reqs); k++ {
		got := reqs[k+1].msgs[len(reqs[k].msgs)]
		if got.Role != "assistant" {
			t.Fatalf("request %d: message after the previous prefix is %s, want the reply", k+1, got.Role)
		}
		if k == 0 && hashMsgs(t, []model.Message{got}) != hashMsgs(t, script) {
			t.Errorf("reply 0 was rewritten before being resent: %+v", got)
		}
	}
	// The trajectory holds exactly what was sent, plus the final reply.
	last := reqs[len(reqs)-1].msgs
	if hashMsgs(t, tr.Messages[:len(last)]) != hashMsgs(t, last) {
		t.Error("trajectory messages differ from what was sent")
	}
}

// Compaction never edits the stored history and never touches the system
// prompt or task: it appends a summary (or elides old outputs) in the model's
// view only. Between compactions every step is append-only again, including
// the summary message, which is resent byte for byte.
func TestCompactionKeepsPrefixStableBetweenCompactions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		window  int
		replies []reply
		stage   string
	}{
		{"elide", 20000, []reply{
			{msg: reads(1, 1)}, {msg: reads(2, 1)}, {msg: reads(3, 1)},
			{msg: reads(4, 1)}, {msg: reads(5, 1)}, {msg: reads(6, 1), input: 15000},
			{msg: reads(7, 1)}, {msg: reads(8, 1)}, {msg: submit},
		}, "elide"},
		{"summarize", 30000, []reply{
			{msg: reads(1, 1)}, {msg: reads(2, 3)}, {msg: reads(3, 1), input: 23000},
			{msg: reads(4, 1)}, {msg: reads(5, 1)}, {msg: submit},
		}, "summarize"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeModel{summary: "## Task\nread big.txt", replies: tc.replies}
			a := appendOnlyAgent(t, f, tc.window)
			sent := map[int]string{} // trajectory index -> hash when first sent
			a.OnStep = func(tr *trajectory.Trajectory) error {
				for i := range tr.Messages {
					if _, ok := sent[i]; !ok {
						sent[i] = hashMsgs(t, tr.Messages[i:i+1])
					}
				}
				return nil
			}
			tr := a.Run(context.Background(), "the task")
			if tr.ExitStatus != StatusSubmitted || len(tr.Compactions) != 1 || tr.Compactions[0].Stage != tc.stage {
				t.Fatalf("status %s, compactions %+v", tr.ExitStatus, tr.Compactions)
			}
			reqs := f.agentRequests()
			c := tr.Compactions[0]
			// The request right after the compaction is the only one allowed
			// to differ from its predecessor's prefix.
			checkPrefixStable(t, reqs, func(k int) bool { return k == c.Step })
			checkSystemStable(t, reqs, appendOnlyRules)
			for k, r := range reqs {
				if hashMsgs(t, r.msgs[:2]) != hashMsgs(t, tr.Messages[:2]) {
					t.Errorf("request %d: system prompt or task changed", k)
				}
			}
			// Stored history is never rewritten by compaction.
			for i := range tr.Messages {
				if h, ok := sent[i]; ok && h != hashMsgs(t, tr.Messages[i:i+1]) {
					t.Errorf("trajectory message %d was edited after it was recorded", i)
				}
			}
			after := reqs[c.Step].msgs
			if len(after) != len(c.View) {
				t.Fatalf("request after compaction has %d messages, view records %d", len(after), len(c.View))
			}
			elided := map[int]bool{}
			for _, ix := range c.Elided {
				elided[ix] = true
			}
			for j, ix := range c.View {
				switch {
				case ix == -1:
					if j != 2 || !strings.HasPrefix(after[j].Content, summaryHeader) {
						t.Errorf("summary at %d, want appended right after the task: %.80q", j, after[j].Content)
					}
				case elided[ix]:
					if !strings.HasPrefix(after[j].Content, elidedPrefix) {
						t.Errorf("message %d recorded as elided but sent as %.80q", ix, after[j].Content)
					}
				default:
					if hashMsgs(t, after[j:j+1]) != hashMsgs(t, tr.Messages[ix:ix+1]) {
						t.Errorf("kept message %d not sent verbatim after compaction", ix)
					}
				}
			}
		})
	}
}

// Resume and follow-up append a notice to the checkpoint's history: the first
// request after resuming starts with every message the interrupted run would
// have sent next, byte for byte, under the same system prompt.
func TestResumeAppendsWithoutRewritingHistory(t *testing.T) {
	script := []reply{{msg: reads(1, 1)}, {msg: reads(2, 2)}, {msg: reads(3, 1)}}
	cut := &fakeModel{replies: script}
	a := appendOnlyAgent(t, cut, 0)
	a.Config.StepLimit = 2
	saves := snapshots(t, a)
	first := a.Run(context.Background(), "the task")
	if first.ExitStatus != StatusStepLimit {
		t.Fatalf("setup: %s", first.ExitStatus)
	}
	cp := loadCheckpoint(t, (*saves)[len(*saves)-1])
	want := hashMsgs(t, first.Messages)
	if hashMsgs(t, cp.Messages) != want {
		t.Fatal("checkpoint round trip changed the history")
	}

	resumed := &fakeModel{replies: []reply{{msg: reads(3, 1)}, {msg: submit}}}
	r, _ := compactAgent(t, resumed, 0)
	r.Tools.Root, r.Resume = a.Tools.Root, cp
	got := r.Run(context.Background(), "")
	if got.ExitStatus != StatusSubmitted {
		t.Fatalf("resume: %s %s", got.ExitStatus, got.Submission)
	}
	reqs := resumed.agentRequests()
	sent := reqs[0].msgs
	if len(sent) != len(first.Messages)+1 || hashMsgs(t, sent[:len(first.Messages)]) != want {
		t.Fatalf("resume rewrote the history (%d messages sent, %d recorded)", len(sent), len(first.Messages))
	}
	if n := sent[len(sent)-1]; n.Role != "user" || !strings.HasPrefix(n.Content, trajectory.ContinuationNotice) {
		t.Errorf("resume notice not appended: %+v", n)
	}
	if sent[0].Content != cut.agentRequests()[0].msgs[0].Content {
		t.Error("resume changed the system prompt bytes")
	}
	checkPrefixStable(t, append(cut.agentRequests(), reqs...), nil)

	// A follow-up on the finished run appends again.
	done := roundTrip(t, got)
	follow := &fakeModel{replies: []reply{{msg: submit}}}
	fa, _ := compactAgent(t, follow, 0)
	fa.Tools.Root, fa.Resume, fa.FollowUp = a.Tools.Root, done, "also check the README"
	if res := fa.Run(context.Background(), ""); res.ExitStatus != StatusSubmitted {
		t.Fatalf("follow-up: %s %s", res.ExitStatus, res.Submission)
	}
	fsent := follow.agentRequests()[0].msgs
	if len(fsent) != len(got.Messages)+1 || hashMsgs(t, fsent[:len(got.Messages)]) != hashMsgs(t, got.Messages) {
		t.Fatal("follow-up rewrote the finished history")
	}
}

// With --plan the [Current plan] block is pinned to the task message. Steps
// that leave the plan alone keep the whole prefix stable; a plan update
// changes only the text after the task, never the system prompt or the task
// text itself. This is the documented exception to the invariant.
func TestPlanBlockOnlyChangesOnPlanUpdate(t *testing.T) {
	plan := `{"items":[{"id":"a","text":"read big.txt","status":"in_progress"}]}`
	done := `{"items":[{"id":"a","text":"read big.txt","status":"done"}]}`
	f := &fakeModel{replies: []reply{
		{msg: planCall("p1", plan)}, {msg: reads(1, 1)}, {msg: reads(2, 1)},
		{msg: planCall("p2", done)}, {msg: reads(3, 1)}, {msg: submit},
	}}
	a := appendOnlyAgent(t, f, 0)
	a.Tools.Plan = true
	tr := a.Run(context.Background(), "the task")
	if tr.ExitStatus != StatusSubmitted {
		t.Fatalf("status = %s: %s", tr.ExitStatus, tr.Submission)
	}
	reqs := f.agentRequests()
	// Requests 1 and 4 follow the two update_plan calls.
	updated := func(k int) bool { return k == 1 || k == 4 }
	checkPrefixStable(t, reqs, updated)
	checkSystemStable(t, reqs, appendOnlyRules)
	for k := 1; k < len(reqs); k++ {
		if !updated(k) {
			continue
		}
		prev, cur := reqs[k-1].msgs, reqs[k].msgs
		if !strings.HasPrefix(cur[1].Content, "the task") || !strings.HasPrefix(prev[1].Content, "the task") {
			t.Errorf("request %d: task text changed", k)
		}
		if hashMsgs(t, cur[2:len(prev)]) != hashMsgs(t, prev[2:]) {
			t.Errorf("request %d: a plan update rewrote messages after the task", k)
		}
	}
}
