package attempts

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func TestLinkEnvRoundTrip(t *testing.T) {
	base := []string{"PATH=/bin", EnvAttempt + "=9", "TRACEPARENT=old", "TRACESTATE=x=1"}
	env := LinkEnv(base, Link{ID: "abc", Index: 2, Count: 3, TraceParent: "00-t-s-01"})
	got := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if _, dup := got[k]; dup {
			t.Fatalf("duplicate %s in %v", k, env)
		}
		got[k] = v
	}
	if got["PATH"] != "/bin" || got["TRACEPARENT"] != "00-t-s-01" || got[EnvAttempt] != "2" {
		t.Fatalf("env = %v", env)
	}
	if _, ok := got["TRACESTATE"]; ok {
		t.Fatalf("stale TRACESTATE kept: %v", env)
	}
	l, ok := LinkFromEnv(func(k string) string { return got[k] })
	if !ok || l.ID != "abc" || l.Index != 2 || l.Count != 3 {
		t.Fatalf("LinkFromEnv = %+v %v", l, ok)
	}
	if _, ok := LinkFromEnv(func(string) string { return "" }); ok {
		t.Fatal("empty env parsed as a link")
	}
	// Without a trace parent the inherited one is left alone.
	env = LinkEnv(base, Link{ID: "abc", Index: 1, Count: 1})
	if !strings.Contains(strings.Join(env, "\n"), "TRACEPARENT=old") {
		t.Fatalf("inherited TRACEPARENT dropped: %v", env)
	}
}

func TestNewIDUnique(t *testing.T) {
	a, b := NewID(), NewID()
	if len(a) != 32 || a == b {
		t.Fatalf("ids %q %q", a, b)
	}
}

func TestExecAgentSetsLinkEnv(t *testing.T) {
	ctx := withLink(context.Background(), Link{ID: "grp", Index: 3, Count: 4})
	var out bytes.Buffer
	if err := ExecAgent("/bin/sh")(ctx, []string{"-c", "echo $" + EnvAttemptsID + ":$" + EnvAttempt + ":$" + EnvAttempts}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "grp:3:4" {
		t.Fatalf("child saw %q", got)
	}
}

func TestRunLinksEachAttempt(t *testing.T) {
	var seen []Link
	o := Options{Models: []string{"a", "b"}, BaseName: "w", AttemptsID: "grp", TraceParent: "00-x-y-01", RunAll: true,
		Stderr: io.Discard, Stdout: io.Discard, Redact: func(s string) string { return s },
		RunAgent: func(ctx context.Context, args []string, out, errw io.Writer) error {
			l, _ := LinkFromContext(ctx)
			seen = append(seen, l)
			return nil
		}}
	rep, _ := Run(context.Background(), o)
	if rep.AttemptsID != "grp" {
		t.Fatalf("report attempts_id = %q", rep.AttemptsID)
	}
	if len(seen) == 0 || seen[0] != (Link{ID: "grp", Index: 1, Count: 2, TraceParent: "00-x-y-01"}) {
		t.Fatalf("links = %+v", seen)
	}
}
