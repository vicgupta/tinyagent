package main

import (
	"strings"
	"testing"
)

func TestLargeMessageWarning(t *testing.T) {
	// 1,000 characters ≈ 250 tokens.
	small := strings.Repeat("x", 1000)
	if note := largeMessageWarning(small, 10_000); note != "" {
		t.Errorf("small message warned: %s", note)
	}
	if note := largeMessageWarning(small, 0); note != "" {
		t.Errorf("unknown window warned: %s", note)
	}
	if note := largeMessageWarning("", 100); note != "" {
		t.Errorf("empty message warned: %s", note)
	}

	// Exactly 50% of a 2,000-token window: 1,000 tokens ≈ 4,000 characters.
	half := strings.Repeat("x", 4000)
	note := largeMessageWarning(half, 2000)
	if note == "" || !strings.Contains(note, "~1,000 tokens") || !strings.Contains(note, "50%") || !strings.Contains(note, "2,000-token") {
		t.Errorf("half-window warning: %q", note)
	}

	// A 1 MiB paste against gpt-4o's 128,000-token window.
	huge := strings.Repeat("x", 1<<20)
	note = largeMessageWarning(huge, 128_000)
	if note == "" || !strings.Contains(note, "meets or exceeds") || !strings.Contains(note, "128,000-token") {
		t.Errorf("over-window warning: %q", note)
	}
	est := ((1 << 20) + 3) / 4
	if !strings.Contains(note, formatInt(est)) {
		t.Errorf("over-window warning missing estimate %s: %q", formatInt(est), note)
	}
}

func TestSessionContextWindow(t *testing.T) {
	if sessionContextWindow(nil) != 0 {
		t.Error("nil session")
	}
	s := &session{modelName: "openai/gpt-4o"}
	if got := sessionContextWindow(s); got != 128_000 {
		t.Errorf("gpt-4o window = %d", got)
	}
	s.cfg.ContextWindow = 4096
	if got := sessionContextWindow(s); got != 4096 {
		t.Errorf("config override = %d", got)
	}
}
