package attempts

import (
	"context"
	"io"
	"strings"
	"testing"
)

// Each fake attempt reports 10 input + 5 output tokens.
func TestTokenBudgetPassedOnAndExhausted(t *testing.T) {
	skipNoBash(t)
	f := &fakeAgent{t: t, statuses: []string{"Submitted", "Submitted", "Submitted"}}
	rep, code := Run(context.Background(), Options{
		Models: []string{"a", "b", "c"}, BaseName: "x", TokenLimit: 25,
		Checks: []Check{{Command: "false"}},
		Stderr: io.Discard, RunAgent: f.run,
	})
	if code != 1 || rep.StopReason != StopBudget || len(f.calls) != 2 {
		t.Fatalf("code=%d calls=%d rep=%+v", code, len(f.calls), rep)
	}
	if got := strings.Join(f.calls[0], " "); !strings.Contains(got, "--token-limit 25") {
		t.Fatalf("first attempt should get the whole budget: %s", got)
	}
	if got := strings.Join(f.calls[1], " "); !strings.Contains(got, "--token-limit 10") {
		t.Fatalf("second attempt should get the remaining budget: %s", got)
	}
}

func TestTokenLimitExceededStopsAttempts(t *testing.T) {
	skipNoBash(t)
	f := &fakeAgent{t: t, statuses: []string{"TokenLimitExceeded", "Submitted"}}
	rep, _ := Run(context.Background(), Options{
		Models: []string{"a", "b"}, BaseName: "x", TokenLimit: 1000,
		Checks: []Check{{Command: "true"}},
		Stderr: io.Discard, RunAgent: f.run,
	})
	if rep.StopReason != StopBudget || len(f.calls) != 1 {
		t.Fatalf("calls=%d rep=%+v", len(f.calls), rep)
	}
}

func TestNoTokenLimitNotForwarded(t *testing.T) {
	skipNoBash(t)
	f := &fakeAgent{t: t, statuses: []string{"Submitted"}}
	Run(context.Background(), Options{
		Models: []string{"a"}, BaseName: "x",
		Checks: []Check{{Command: "true"}}, Stderr: io.Discard, RunAgent: f.run,
	})
	if got := strings.Join(f.calls[0], " "); strings.Contains(got, "--token-limit") {
		t.Fatalf("unset token limit forwarded: %s", got)
	}
}
