package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/attempts"
)

func TestAttemptsJSONEvents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("runs bash")
	}
	newEvalOpenAI(t)
	repo := evalRepo(t)
	counter := filepath.Join(t.TempDir(), "count")
	check := `n=$(cat ` + counter + ` 2>/dev/null || echo 0); n=$((n+1)); echo $n > ` + counter + `; [ $n -ge 2 ]`
	out := filepath.Join(t.TempDir(), "run.json")
	code, stdout, stderr := runCLI(t, "--attempts", "3", "-m", "openai/gpt-4o", "--attempt-check", check,
		"-t", "MODE_PASS", "--cwd", repo, "--worktree=js", "-o", out, "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, stdout, stderr)
	}
	var evs []map[string]any
	for _, line := range strings.Split(strings.TrimRight(stdout, "\n"), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("stdout line is not JSON: %q (%v)\n%s", line, err, stdout)
		}
		evs = append(evs, m)
	}
	count := map[string]int{}
	for _, e := range evs {
		ty, _ := e["type"].(string)
		count[ty]++
		if _, ok := e["attempt"]; !ok && ty != "attempts.result" {
			t.Fatalf("event %q has no attempt field: %v", ty, e)
		}
	}
	for ty, want := range map[string]int{"attempt.started": 2, "attempt.check": 2, "attempt.result": 2,
		"attempts.result": 1, "run.started": 2, "result": 2} {
		if count[ty] != want {
			t.Fatalf("%d %q events, want %d\n%s", count[ty], ty, want, stdout)
		}
	}
	final := evs[len(evs)-1]
	if final["type"] != "attempts.result" {
		t.Fatalf("last event %v", final)
	}
	b, err := os.ReadFile(attempts.ReportPath(out))
	if err != nil {
		t.Fatal(err)
	}
	var rep attempts.Report
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Winner == nil || final["winner"].(float64) != float64(*rep.Winner) || final["stop_reason"] != rep.StopReason ||
		final["winner_branch"] != rep.WinnerBranch {
		t.Fatalf("attempts.result %v does not match report %s", final, b)
	}
	// Child result events keep their shape, plus the attempt tag.
	for _, e := range evs {
		if e["type"] == "result" && e["attempt"].(float64) == 2 {
			if e["status"] != "Submitted" || e["submission"] != "done" {
				t.Fatalf("child result event lost fields: %v", e)
			}
		}
	}
	if !strings.Contains(stderr, "winner: attempt 2") {
		t.Fatalf("stderr: %s", stderr)
	}
}
