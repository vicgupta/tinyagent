package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/model"
)

// verifyAgent returns an agent with the verify-on-stop guard on, counting
// `true` and `false` (and their arguments) as verification commands, and
// a pointer to the verify events it emits.
func verifyAgent(t *testing.T, m model.Model) (*Agent, *[]string) {
	t.Helper()
	a := newAgent(t, m)
	a.Config.VerifyOnStop = config.VerifyOnStop{Enabled: true, Commands: []string{"true", "false"}, MaxReprompts: 2}
	var events []string
	a.OnEvent = func(kind string, _ map[string]any) {
		if strings.HasPrefix(kind, "verify.") {
			events = append(events, kind)
		}
	}
	return a, &events
}

func edit() model.Message {
	return model.Message{Role: "assistant", ToolCalls: []model.ToolCall{call("w", "write_file", `{"path":"f.txt","content":"hi"}`)}}
}

func bashMsg(id, cmd string) model.Message {
	return model.Message{Role: "assistant", ToolCalls: []model.ToolCall{call(id, "bash", `{"command":"`+cmd+`"}`)}}
}

func verifyNotices(msgs []model.Message) int {
	n := 0
	for _, m := range msgs {
		if m.Role == "user" && strings.HasPrefix(m.Content, "The run was not accepted as finished") {
			n++
		}
	}
	return n
}

func TestVerifyOnStopRepromptsThenStops(t *testing.T) {
	m := &scripted{replies: []model.Message{
		edit(),
		{Role: "assistant", Content: "Done."},
		bashMsg("v", "true ./..."),
		{Role: "assistant", Content: "Done, tests pass."},
	}}
	a, events := verifyAgent(t, m)
	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusCompleted || tr.Submission != "Done, tests pass." {
		t.Fatalf("trajectory = %+v", tr)
	}
	if tr.Unverified || tr.VerifyPending || tr.VerifyReprompts != 1 {
		t.Errorf("unverified=%v pending=%v reprompts=%d", tr.Unverified, tr.VerifyPending, tr.VerifyReprompts)
	}
	if n := verifyNotices(tr.Messages); n != 1 {
		t.Errorf("verify notices = %d, want 1", n)
	}
	if !strings.Contains(m.seen[2][len(m.seen[2])-1].Content, "`true`") {
		t.Errorf("notice does not name the verify commands: %q", m.seen[2][len(m.seen[2])-1].Content)
	}
	if len(*events) != 1 || (*events)[0] != "verify.reprompt" {
		t.Errorf("events = %v", *events)
	}
}

func TestVerifyOnStopSubmitMarker(t *testing.T) {
	m := &scripted{replies: []model.Message{
		edit(),
		{Role: "assistant", Content: "Done.\n" + SubmitMarker},
		bashMsg("v", "true"),
		{Role: "assistant", Content: "Verified.\n" + SubmitMarker},
	}}
	a, _ := verifyAgent(t, m)
	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusSubmitted || tr.Submission != "Verified." || tr.Unverified || tr.VerifyReprompts != 1 {
		t.Fatalf("trajectory = %+v", tr)
	}
}

func TestVerifyOnStopNoChanges(t *testing.T) {
	m := &scripted{replies: []model.Message{
		model.Message{Role: "assistant", ToolCalls: []model.ToolCall{call("r", "read_file", `{"path":"missing.txt"}`)}},
		{Role: "assistant", Content: "Nothing to change."},
	}}
	a, events := verifyAgent(t, m)
	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusCompleted || tr.Unverified || tr.VerifyReprompts != 0 || len(*events) != 0 {
		t.Fatalf("trajectory = %+v, events = %v", tr, *events)
	}
	if verifyNotices(tr.Messages) != 0 {
		t.Error("guard re-prompted a run without changes")
	}
}

func TestVerifyOnStopFailureShownToModel(t *testing.T) {
	m := &scripted{replies: []model.Message{
		edit(),
		bashMsg("v1", "false"),
		{Role: "assistant", Content: "Done."},
		bashMsg("v2", "true"),
		{Role: "assistant", Content: "Fixed."},
	}}
	a, _ := verifyAgent(t, m)
	tr := a.Run(context.Background(), "task")
	// The failed verify run's result reached the model before it answered.
	sent := m.seen[2]
	last := sent[len(sent)-1]
	if last.Role != "tool" || !strings.HasPrefix(last.Content, "<returncode>1</returncode>") {
		t.Errorf("failed verify result not shown: %+v", last)
	}
	if tr.ExitStatus != StatusCompleted || tr.Submission != "Fixed." || tr.VerifyReprompts != 1 || tr.Unverified {
		t.Fatalf("trajectory = %+v", tr)
	}
}

func TestVerifyOnStopCapMarksUnverified(t *testing.T) {
	m := &scripted{replies: []model.Message{
		edit(),
		{Role: "assistant", Content: "Done."},
		{Role: "assistant", Content: "Done, really."},
		{Role: "assistant", Content: "Still done."},
	}}
	a, events := verifyAgent(t, m)
	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusCompleted || tr.Submission != "Still done." {
		t.Fatalf("trajectory = %+v", tr)
	}
	if !tr.Unverified || tr.VerifyReprompts != 2 || verifyNotices(tr.Messages) != 2 {
		t.Errorf("unverified=%v reprompts=%d notices=%d", tr.Unverified, tr.VerifyReprompts, verifyNotices(tr.Messages))
	}
	want := []string{"verify.reprompt", "verify.reprompt", "verify.unverified"}
	if strings.Join(*events, ",") != strings.Join(want, ",") {
		t.Errorf("events = %v, want %v", *events, want)
	}
}

func TestVerifyOnStopOff(t *testing.T) {
	m := &scripted{replies: []model.Message{
		edit(),
		{Role: "assistant", Content: "Done."},
	}}
	a, events := verifyAgent(t, m)
	a.Config.VerifyOnStop.Enabled = false
	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusCompleted || tr.Unverified || tr.VerifyPending || tr.VerifyReprompts != 0 || len(*events) != 0 {
		t.Fatalf("trajectory = %+v, events = %v", tr, *events)
	}
}

func TestVerifyOnStopBashRefused(t *testing.T) {
	m := &scripted{replies: []model.Message{
		edit(),
		bashMsg("v", "true"),
		{Role: "assistant", Content: "Done."},
	}}
	a, events := verifyAgent(t, m)
	withPolicy(t, a, config.Permissions{Rules: []config.PermissionRule{
		{Tool: "bash", Command: "*", Decision: "deny"},
	}})
	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusCompleted || !tr.Unverified || tr.VerifyReprompts != 0 {
		t.Fatalf("trajectory = %+v", tr)
	}
	if len(*events) != 1 || (*events)[0] != "verify.unverified" {
		t.Errorf("events = %v", *events)
	}
}

func TestVerifyOnStopOtherBashCountsAsChange(t *testing.T) {
	m := &scripted{replies: []model.Message{
		bashMsg("b", "echo hi > f.txt"),
		{Role: "assistant", Content: "Done."},
		bashMsg("v", "true"),
		{Role: "assistant", Content: "Verified."},
	}}
	a, _ := verifyAgent(t, m)
	tr := a.Run(context.Background(), "task")
	if tr.VerifyReprompts != 1 || tr.Unverified || tr.Submission != "Verified." {
		t.Fatalf("trajectory = %+v", tr)
	}
}

func TestIsVerifyCommand(t *testing.T) {
	cfg := config.VerifyOnStop{Commands: []string{"make check"}}
	for cmd, want := range map[string]bool{
		"make check":         true,
		"make check -j4":     true,
		"make checkout":      false,
		"go test ./...":      false,
		"":                   false,
		"echo make check":    false,
		"  make check  ":     false, // callers trim; untrimmed input does not match
		"make check && true": true,
	} {
		if got := isVerifyCommand(cfg, cmd); got != want {
			t.Errorf("isVerifyCommand(%q) = %v, want %v", cmd, got, want)
		}
	}
	def := config.VerifyOnStop{}
	for cmd, want := range map[string]bool{
		"go test ./...":              true,
		"cd sub && go test ./...":    true,
		"cargo test":                 true,
		"npm run test":               true,
		"pytest -q":                  true,
		"echo go test":               false,
		"cat go.mod":                 false,
		"git commit -m 'go test it'": false,
	} {
		if got := isVerifyCommand(def, cmd); got != want {
			t.Errorf("default isVerifyCommand(%q) = %v, want %v", cmd, got, want)
		}
	}
	pat := config.VerifyOnStop{Patterns: []string{`^\./ci\.sh\b`}}
	if !isVerifyCommand(pat, "./ci.sh fast") || isVerifyCommand(pat, "go test ./...") {
		t.Error("configured patterns should replace the defaults")
	}
}
