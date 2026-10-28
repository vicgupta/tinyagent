package main

import (
	"reflect"
	"testing"

	"github.com/vicgupta/tinyagent/internal/attempts"
)

func TestAddAttemptLink(t *testing.T) {
	started := map[string]any{}
	t.Setenv(attempts.EnvAttemptsID, "")
	addAttemptLink(started)
	if _, ok := started["attempt"]; ok {
		t.Fatal("attempt field without a parent")
	}
	t.Setenv(attempts.EnvAttemptsID, "grp")
	t.Setenv(attempts.EnvAttempt, "2")
	t.Setenv(attempts.EnvAttempts, "3")
	addAttemptLink(started)
	want := map[string]any{"attempts_id": "grp", "index": 2, "count": 3}
	if !reflect.DeepEqual(started["attempt"], want) {
		t.Fatalf("attempt = %v", started["attempt"])
	}
}

func TestAttemptsLinkChildArgs(t *testing.T) {
	l := &attemptsLink{}
	if got := l.childArgs([]string{"-t", "x"}); !reflect.DeepEqual(got, []string{"-t", "x"}) {
		t.Fatalf("log db off: %v", got)
	}
	l.dbPath = "/r/.tinyagent/tinyagent.db"
	if got := l.childArgs([]string{"-t", "x"}); !reflect.DeepEqual(got, []string{"--log-db=/r/.tinyagent/tinyagent.db", "-t", "x"}) {
		t.Fatalf("log db on: %v", got)
	}
	if l.traceParent() != "" {
		t.Fatal("trace parent without otel")
	}
}
