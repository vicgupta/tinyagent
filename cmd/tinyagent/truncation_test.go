package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMaxOutputTokensFlag(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.yaml")
	os.WriteFile(empty, nil, 0o644)
	for _, c := range []struct {
		name string
		args []string
		want any
	}{
		{"unset", nil, nil},
		{"flag", []string{"--max-output-tokens", "321"}, float64(321)},
	} {
		t.Run(c.name, func(t *testing.T) {
			var bodies []map[string]any
			srv := fakeOpenAIBodies(t, &bodies)
			t.Setenv("OPENAI_BASE_URL", srv.URL)
			t.Setenv("OPENAI_API_KEY", "fake")
			args := append([]string{"-t", "x", "--no-stream", "--cwd", dir, "-m", "openai/gpt-4o", "-c", empty}, c.args...)
			var code int
			stderr := captureStderr(t, func() { code, _, _ = runWith(t, args...) })
			if code != 0 || len(bodies) != 1 {
				t.Fatalf("exit %d, %d requests; stderr:\n%s", code, len(bodies), stderr)
			}
			if got := bodies[0]["max_completion_tokens"]; got != c.want {
				t.Errorf("max_completion_tokens = %v, want %v", got, c.want)
			}
		})
	}
}

func TestMaxOutputTokensRejected(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "fake")
	for _, args := range [][]string{
		{"--max-output-tokens", "-5", "-m", "openai/gpt-4o"},
		{"--max-output-tokens", "abc", "-m", "openai/gpt-4o"},
		{"--max-output-tokens", "100", "-m", "codex/gpt-5"},
	} {
		var code int
		stderr := captureStderr(t, func() {
			code, _, _ = runWith(t, append([]string{"-t", "x", "--no-stream", "--cwd", t.TempDir()}, args...)...)
		})
		if code != 2 {
			t.Errorf("%v: exit %d, want 2; stderr %q", args, code, stderr)
		}
	}
}
