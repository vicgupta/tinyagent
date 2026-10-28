package attempts

import (
	"context"
	"errors"
	"io"
	"testing"
)

func TestRunAllPicksLowestPassingAttempt(t *testing.T) {
	skipNoBash(t)
	f := &fakeAgent{t: t, statuses: []string{"Submitted", "Submitted", "Submitted"}}
	rep, code := Run(context.Background(), Options{
		Models: []string{"a", "b", "c"}, BaseName: "x", RunAll: true,
		Checks: []Check{{Command: "test $(cat n) -ge 2"}}, Stderr: io.Discard, RunAgent: f.run,
	})
	if code != 0 || len(f.calls) != 3 || rep.Winner == nil || *rep.Winner != 2 || rep.StopReason != StopCompleted {
		t.Fatalf("code=%d calls=%d rep=%+v", code, len(f.calls), rep)
	}
	if rep.StopOnPass || rep.Keep != KeepAll || !rep.Attempts[2].Passed {
		t.Fatalf("rep=%+v", rep)
	}
}

func TestRunAllWithoutPassIsExhausted(t *testing.T) {
	skipNoBash(t)
	f := &fakeAgent{t: t, statuses: []string{"Submitted", "Submitted"}}
	rep, code := Run(context.Background(), Options{
		Models: []string{"a", "b"}, BaseName: "x", RunAll: true,
		Checks: []Check{{Command: "false"}}, Stderr: io.Discard, RunAgent: f.run,
	})
	if code != 1 || rep.Winner != nil || rep.StopReason != StopExhausted || len(f.calls) != 2 {
		t.Fatalf("code=%d rep=%+v", code, rep)
	}
}

func TestKeepCleanup(t *testing.T) {
	skipNoBash(t)
	for _, c := range []struct {
		keep  string
		check string
		want  []int
	}{
		{KeepAll, "test $(cat n) -ge 2", nil},
		{"", "test $(cat n) -ge 2", nil},
		{KeepNone, "test $(cat n) -ge 2", []int{1, 3}},
		{KeepFailed, "test $(cat n) -ge 2", []int{3}},
		{KeepNone, "false", nil}, // no winner: nothing removed
	} {
		f := &fakeAgent{t: t, statuses: []string{"Submitted", "Submitted", "Submitted"}}
		var removed []int
		rep, _ := Run(context.Background(), Options{
			Models: []string{"a", "b", "c"}, BaseName: "x", RunAll: true, Keep: c.keep,
			Checks: []Check{{Command: c.check}}, Stderr: io.Discard, RunAgent: f.run,
			Remove: func(a Attempt) error { removed = append(removed, a.Attempt); return nil },
		})
		if len(removed) != len(c.want) {
			t.Fatalf("keep %q check %q: removed %v, want %v", c.keep, c.check, removed, c.want)
		}
		for i, n := range c.want {
			if removed[i] != n || !rep.Attempts[n-1].Removed {
				t.Fatalf("keep %q: removed %v, want %v (rep %+v)", c.keep, removed, c.want, rep.Attempts)
			}
		}
		if rep.Winner != nil && rep.Attempts[*rep.Winner-1].Removed {
			t.Fatalf("keep %q removed the winner", c.keep)
		}
	}
}

func TestKeepCleanupFailureLeavesAttempt(t *testing.T) {
	skipNoBash(t)
	f := &fakeAgent{t: t, statuses: []string{"Submitted", "Submitted"}}
	rep, code := Run(context.Background(), Options{
		Models: []string{"a", "b"}, BaseName: "x", Keep: KeepNone,
		Checks: []Check{{Command: "test $(cat n) -ge 2"}}, Stderr: io.Discard, RunAgent: f.run,
		Remove: func(Attempt) error { return errors.New("boom") },
	})
	if code != 0 || rep.Attempts[0].Removed {
		t.Fatalf("code=%d rep=%+v", code, rep)
	}
}
