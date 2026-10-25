//go:build !windows

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// Kill -9 the CLI during a long bash call, then --resume: the model sees an
// interrupted result for that call and no step, cost, or message is lost.
func TestKill9DuringBashThenResume(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	bin := filepath.Join(t.TempDir(), "tinyagent")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	root, err := canonicalRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	var resumed []model.Message
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Messages []model.Message }
		json.NewDecoder(r.Body).Decode(&body)
		if len(body.Messages) == 2 {
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"running","tool_calls":[{"id":"slow","type":"function","function":{"name":"bash","arguments":"{\"command\":\"sleep 5\"}"}}]}}],"usage":{"prompt_tokens":100,"completion_tokens":10}}`)
			return
		}
		resumed = body.Messages
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"done"}}],"usage":{"prompt_tokens":100,"completion_tokens":10}}`)
	}))
	defer srv.Close()
	env := append(os.Environ(), "OPENAI_BASE_URL="+srv.URL, "OPENAI_API_KEY=test", "XDG_CONFIG_HOME="+t.TempDir(), "HOME="+t.TempDir())
	first := filepath.Join(root, "run.json")
	cmd := exec.Command(bin, "-t", "task", "--cwd", root, "-m", "openai/gpt-4o", "--no-stream", "-o", first)
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		if tr, err := trajectory.Load(first); err == nil && len(tr.PendingCalls) == 1 {
			break
		}
		if time.Now().After(deadline) {
			cmd.Process.Kill()
			t.Fatal("no write-ahead checkpoint")
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // bash is now running
	cmd.Process.Signal(syscall.SIGKILL)
	cmd.Wait()

	out := filepath.Join(root, "continued.json")
	res := exec.Command(bin, "--resume", first, "--cwd", root, "--no-stream", "-o", out, "--json")
	res.Env = env
	stdout, err := res.Output()
	if err != nil {
		t.Fatalf("resume: %v\n%s", err, stdout)
	}
	if len(resumed) != 5 || resumed[2].Content != "running" || resumed[3].ToolCallID != "slow" || !strings.Contains(resumed[3].Content, "[interrupted:") {
		t.Fatalf("resumed request = %+v", resumed)
	}
	for _, ev := range []string{`"type":"resume.interrupted_call"`, `"type":"checkpoint.saved"`} {
		if !strings.Contains(string(stdout), ev) {
			t.Errorf("missing %s in %s", ev, stdout)
		}
	}
	tr, err := trajectory.Load(first)
	if err != nil || tr.Steps != 1 || tr.InputTokens != 100 {
		t.Fatalf("crash checkpoint %+v %v", tr, err)
	}
	b, _ := os.ReadFile(out)
	var final trajectory.Trajectory
	json.Unmarshal(b, &final)
	if final.Steps != 2 || final.InputTokens != 200 || final.ExitStatus != "Completed" {
		t.Fatalf("final steps %d tokens %d status %s", final.Steps, final.InputTokens, final.ExitStatus)
	}
}
