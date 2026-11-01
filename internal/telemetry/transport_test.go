package telemetry

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTransport replays scripted attempts and records every request.
type fakeTransport struct {
	mu       sync.Mutex
	attempts []Attempt // returned in order; once exhausted, success
	bodies   [][]byte
	ctypes   []string
	times    []time.Time
}

func (f *fakeTransport) Post(ctx context.Context, body []byte, ctype string) Attempt {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bodies = append(f.bodies, body)
	f.ctypes = append(f.ctypes, ctype)
	f.times = append(f.times, time.Now())
	if len(f.attempts) == 0 {
		return Attempt{}
	}
	a := f.attempts[0]
	f.attempts = f.attempts[1:]
	return a
}

func (f *fakeTransport) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bodies)
}

func fakeSettings(protocol string, timeout time.Duration) Settings {
	return Settings{Endpoint: "http://unused.invalid/v1/traces", Protocol: protocol, Timeout: timeout,
		ServiceName: "tinyagent", MaxAttributeBytes: DefaultMaxAttributeBytes}
}

func TestFakeTransportRetriesTransient(t *testing.T) {
	f := &fakeTransport{attempts: []Attempt{
		{Err: errors.New("HTTP 503"), Retry: true},
		{Err: errors.New("connection reset"), Retry: true},
	}}
	e, _ := New(Options{Settings: fakeSettings("http/json", 5*time.Second), Model: "m", Transport: f})
	e.Emit("result", map[string]any{"status": "Submitted"})
	if w := e.Close(); w != "" {
		t.Fatalf("warning after two transient failures then success: %s", w)
	}
	if f.calls() != 3 {
		t.Fatalf("%d attempts, want 3", f.calls())
	}
	for i, ct := range f.ctypes {
		if ct != "application/json" {
			t.Errorf("attempt %d content type %q", i, ct)
		}
		if string(f.bodies[i]) != string(f.bodies[0]) {
			t.Errorf("attempt %d body differs from the first", i)
		}
	}
}

func TestFakeTransportPermanentFailureNotRetried(t *testing.T) {
	f := &fakeTransport{attempts: []Attempt{{Err: errors.New("HTTP 400 Bad Request")}}}
	e, _ := New(Options{Settings: fakeSettings("http/protobuf", 5*time.Second), Model: "m", Transport: f})
	e.Emit("result", map[string]any{"status": "Submitted"})
	w := e.Close()
	if f.calls() != 1 {
		t.Errorf("%d attempts, want 1", f.calls())
	}
	if !strings.Contains(w, "HTTP 400") || !strings.Contains(w, "1 of 1 spans not delivered") {
		t.Errorf("warning = %q", w)
	}
	if f.ctypes[0] != "application/x-protobuf" {
		t.Errorf("content type %q", f.ctypes[0])
	}
}

func TestFakeTransportHonorsRetryAfter(t *testing.T) {
	f := &fakeTransport{attempts: []Attempt{{Err: errors.New("HTTP 429"), Retry: true, Wait: 400 * time.Millisecond}}}
	e, _ := New(Options{Settings: fakeSettings("http/json", 5*time.Second), Model: "m", Transport: f})
	e.Emit("result", map[string]any{"status": "Submitted"})
	if w := e.Close(); w != "" {
		t.Fatal(w)
	}
	if f.calls() != 2 {
		t.Fatalf("%d attempts, want 2", f.calls())
	}
	if gap := f.times[1].Sub(f.times[0]); gap < 350*time.Millisecond {
		t.Errorf("retried after %s, want the 400ms Retry-After", gap)
	}
}

func TestFakeTransportGivesUpAtTimeout(t *testing.T) {
	// Always transient: retried with back-off until the export timeout,
	// then reported once.
	f := &fakeTransport{}
	for i := 0; i < 100; i++ {
		f.attempts = append(f.attempts, Attempt{Err: errors.New("connection refused"), Retry: true})
	}
	s := fakeSettings("http/json", 600*time.Millisecond)
	e, _ := New(Options{Settings: s, Model: "m", Transport: f})
	e.Emit("result", map[string]any{"status": "Submitted"})
	start := time.Now()
	w := e.Close()
	if d := time.Since(start); d > s.Timeout+time.Second {
		t.Errorf("Close took %s", d)
	}
	if n := f.calls(); n < 2 {
		t.Errorf("%d attempts, want retries before the timeout", n)
	}
	if !strings.Contains(w, "connection refused") || strings.Count(w, "otel:") != 1 {
		t.Errorf("warning = %q", w)
	}
}

func TestRetryAfter(t *testing.T) {
	if d := retryAfter("3"); d != 3*time.Second {
		t.Errorf("seconds: %s", d)
	}
	if d := retryAfter(""); d != 0 {
		t.Errorf("empty: %s", d)
	}
	if d := retryAfter("soon"); d != 0 {
		t.Errorf("garbage: %s", d)
	}
	future := time.Now().Add(10 * time.Second).UTC().Format(http1123)
	if d := retryAfter(future); d <= 0 || d > 11*time.Second {
		t.Errorf("date: %s", d)
	}
}

const http1123 = "Mon, 02 Jan 2006 15:04:05 GMT"
