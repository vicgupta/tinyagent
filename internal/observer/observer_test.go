package observer

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/harness"
	"github.com/vicgupta/tinyagent/internal/ratchet"
)

func failed(stage, output string) *harness.Result {
	return &harness.Result{
		Workflow: "build", Status: harness.StatusBlocked, FailedStage: stage, Reason: "check failed",
		Attempts: []harness.Attempt{{Skill: stage, Check: harness.Check{Method: "command", Argv: []string{"go", "test", "./..."}, ExitCode: 1, Output: output}}},
	}
}

var now = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

func TestSignatureNormalizes(t *testing.T) {
	a := failed("generate-code", "ok  pkg 0.12s\n--- FAIL: TestX (0.03s)\n    /tmp/go-build123/x_test.go:12: got 3 want 4\n")
	b := failed("generate-code", "ok  pkg 4.70s\n--- FAIL: TestX (1.20s)\n    /var/folders/ab/x_test.go:99: got 7 want 8\n")
	if Signature(a) != Signature(b) {
		t.Errorf("same failure, different signatures: %s vs %s", Signature(a), Signature(b))
	}
	if c := failed("plan-tasks", a.Attempts[0].Check.Output); Signature(c) == Signature(a) {
		t.Error("different stage, same signature")
	}
	d := failed("generate-code", "--- FAIL: TestOther (0.03s)\n")
	if Signature(d) == Signature(a) {
		t.Error("different failing test, same signature")
	}
}

func TestProposeRedactsAndCaps(t *testing.T) {
	r := failed("generate-code", "error: token sk-secret leaked\n"+strings.Repeat("x", MaxExcerpt+100))
	p := Propose(r, "/proj", func(s string) string { return strings.ReplaceAll(s, "sk-secret", "[REDACTED]") }, now)
	if strings.Contains(p.Excerpt, "sk-secret") || !strings.Contains(p.Excerpt, "[REDACTED]") {
		t.Errorf("excerpt not redacted: %.80q", p.Excerpt)
	}
	if len(p.Excerpt) > MaxExcerpt+20 {
		t.Errorf("excerpt not capped: %d bytes", len(p.Excerpt))
	}
	if !strings.HasPrefix(p.Command, "tinyagent ratchet add '") || !strings.Contains(p.Command, "--incident '") || !strings.Contains(p.Command, "go test ./...") {
		t.Errorf("command = %s", p.Command)
	}
	if !strings.Contains(p.Incident, tag(p.Signature)) || p.Stage != "generate-code" {
		t.Errorf("proposal = %+v", p)
	}
	rule := ratchet.Rule{ID: "r1", Rule: p.Rule, Incident: p.Incident, Created: p.Created, Expires: "2027-01-01"}
	if err := rule.Validate(); err != nil {
		t.Errorf("proposed rule invalid: %v", err)
	}
}

func TestObserveLedgerAndRecurrence(t *testing.T) {
	root := t.TempDir()
	res := failed("generate-code", "--- FAIL: TestX (0.03s)\n")
	ob, err := Observe(res, root, nil, now)
	if err != nil || !ob.Observed || ob.Proposal == nil || ob.Recurrence || ob.Entry.Count != 1 {
		t.Fatalf("first: %+v %v", ob, err)
	}
	ob2, err := Observe(failed("generate-code", "--- FAIL: TestX (9.99s)\n"), root, nil, now.Add(time.Hour))
	if err != nil || !ob2.Recurrence || ob2.Proposal != nil || ob2.Entry.Count != 2 || ob2.RuleID != "" {
		t.Fatalf("second: %+v %v", ob2, err)
	}
	b, _ := os.ReadFile(ProposalsPath(root))
	if n := strings.Count(string(b), "\n"); n != 1 {
		t.Errorf("proposals lines = %d, want 1", n)
	}
	// An applied rule is found on the next recurrence.
	if _, err := ratchet.Add(ratchet.Path(root), ratchet.Rule{Rule: ob.Proposal.Rule, Incident: ob.Proposal.Incident}, now); err != nil {
		t.Fatal(err)
	}
	ob3, _ := Observe(res, root, nil, now)
	if ob3.RuleID != "r1" || ob3.Entry.Count != 3 {
		t.Errorf("third: %+v", ob3)
	}
}

func TestObserveSkipsPassedAndInterrupted(t *testing.T) {
	root := t.TempDir()
	for _, st := range []string{harness.StatusPassed, harness.StatusInterrupted} {
		ob, err := Observe(&harness.Result{Status: st, FailedStage: "x"}, root, nil, now)
		if err != nil || ob.Observed {
			t.Errorf("%s: %+v %v", st, ob, err)
		}
	}
	if _, err := os.Stat(Dir(root)); !os.IsNotExist(err) {
		t.Errorf("observer dir created: %v", err)
	}
}

func TestCorruptLedgerStartsFresh(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(Dir(root), 0o700)
	os.WriteFile(LedgerPath(root), []byte("{not json\n"), 0o600)
	ob, err := Observe(failed("s", "error x\n"), root, nil, now)
	if err != nil || len(ob.Warnings) != 1 || ob.Recurrence || ob.Proposal == nil {
		t.Errorf("%+v %v", ob, err)
	}
}
