package main

import (
	"reflect"
	"testing"
)

// run.started reports the after-taint command policy under
// lockdown.commands.
func TestLockdownRunStartedCommandPolicy(t *testing.T) {
	dir, _ := lockdownEnv(t)
	var code int
	stdout, stderr := captureStdoutStderr(t, func() {
		code, _, _ = runWith(t, "-t", "task", "-m", "openai/gpt-4o", "--no-stream", "--json", "--cwd", dir, "--lockdown")
	})
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	ev := lockdownEvents(t, stdout)
	info, _ := ev["run.started"][0]["lockdown"].(map[string]any)
	want := map[string]any{"policy": "refused"}
	if got := info["commands"]; !reflect.DeepEqual(got, want) {
		t.Errorf("run.started lockdown.commands = %v, want %v", got, want)
	}
}
