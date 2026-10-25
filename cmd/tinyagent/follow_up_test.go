package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// finishedFixture is resumeFixture turned into a Submitted run.
func finishedFixture(t *testing.T) (*trajectory.Trajectory, string, string, string) {
	t.Helper()
	tr, source, output, cfg := resumeFixture(t)
	tr.Messages = append(tr.Messages, model.Message{Role: "assistant", Content: "found two races\n" + agent.SubmitMarker})
	tr.Steps, tr.ExitStatus, tr.Submission = 2, agent.StatusSubmitted, "found two races"
	if err := tr.Save(source); err != nil {
		t.Fatal(err)
	}
	return tr, source, output, cfg
}

// followUpServer answers each chat request with the next reply, recording the
// request messages.
func followUpServer(t *testing.T, replies ...string) *[][]model.Message {
	t.Helper()
	seen := &[][]model.Message{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Messages []model.Message }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		*seen = append(*seen, body.Messages)
		reply := "done"
		if len(replies) > 0 {
			reply, replies = replies[0], replies[1:]
		}
		b, _ := json.Marshal(reply)
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":` + string(b) + `},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":20}}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("AGENT_MODEL", "")
	return seen
}

func TestCLIFollowUp(t *testing.T) { // AC1, AC3, AC7, AC8, AC13
	for _, via := range []string{"flag", "stdin"} {
		t.Run(via, func(t *testing.T) {
			tr, source, output, cfg := finishedFixture(t)
			before, _ := os.ReadFile(source)
			seen := followUpServer(t, "fixed\n"+agent.SubmitMarker)
			args := []string{"--resume", source, "--cwd", tr.Root, "-c", cfg, "-o", output, "--no-stream", "--json"}
			if via == "flag" {
				args = append(args, "-t", "fix the races you found")
			} else {
				args = append(args, "-t", "-")
				r, w, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				w.WriteString("fix the races you found\n")
				w.Close()
				defer func(f *os.File) { os.Stdin = f }(os.Stdin)
				os.Stdin = r
			}
			var code int
			var stdout string
			stderr := captureStderr(t, func() {
				stdout = captureStdout(t, func() { code, _, _ = runWith(t, args...) })
			})
			if code != 0 || len(*seen) != 1 {
				t.Fatalf("exit %d, %d requests: %s", code, len(*seen), stderr)
			}
			msgs := (*seen)[0]
			if len(msgs) != len(tr.Messages)+1 || msgs[len(msgs)-2].Content != tr.Messages[len(tr.Messages)-1].Content {
				t.Fatalf("history not sent intact: %d messages", len(msgs))
			}
			last := msgs[len(msgs)-1]
			if last.Role != "user" || !strings.HasPrefix(last.Content, trajectory.FollowUpNotice) || !strings.Contains(last.Content, "fix the races you found") {
				t.Fatalf("follow-up message: %+v", last)
			}
			if !strings.Contains(stderr, "following up") || !strings.Contains(stderr, "previous status Submitted after 2 steps") {
				t.Fatalf("missing feedback: %s", stderr)
			}
			ev := runStarted(t, stdout)
			if ev["resumed"] != true || ev["follow_up"] != true {
				t.Fatalf("run.started: %v", ev)
			}
			after, _ := os.ReadFile(source)
			if string(before) != string(after) {
				t.Fatal("source changed")
			}
			if _, err := os.Stat(filepath.Join(tr.Root, "must-not-replay")); !os.IsNotExist(err) {
				t.Fatal("replayed historical write")
			}
			got, err := trajectory.Load(output)
			if err != nil {
				t.Fatal(err)
			}
			if got.ExitStatus != agent.StatusSubmitted || got.Steps != 3 || got.InputTokens != 200 || got.Resume == nil || !got.Resume.FollowUp || got.Resume.ExitStatus != agent.StatusSubmitted || got.Resume.Submission != "found two races" {
				t.Fatalf("result: %s steps %d resume %+v", got.ExitStatus, got.Steps, got.Resume)
			}

			// Chaining: the follow-up's result can itself be followed up.
			chained := filepath.Join(tr.Root, "chained.json")
			stderr = captureStderr(t, func() {
				code, _, _ = runWith(t, "--resume", output, "--cwd", tr.Root, "-c", cfg, "-o", chained, "--no-stream", "-t", "now add tests")
			})
			if code != 0 || len(*seen) != 2 {
				t.Fatalf("chain exit %d: %s", code, stderr)
			}
			c, err := trajectory.Load(chained)
			if err != nil || c.Resume == nil || !c.Resume.FollowUp || c.Resume.Previous == nil || !c.Resume.Previous.FollowUp {
				t.Fatalf("chain provenance: %v %+v", err, c)
			}
		})
	}
}

func TestCLIFollowUpUsageErrors(t *testing.T) { // AC4, AC5, AC11
	for _, which := range []string{"missing", "empty", "blank stdin", "unfinished", "step limit"} {
		t.Run(which, func(t *testing.T) {
			tr, source, output, cfg := finishedFixture(t)
			seen := followUpServer(t)
			args := []string{"--resume", source, "--cwd", tr.Root, "-c", cfg, "-o", output, "--no-stream"}
			want, wantCode := "follow-up task is required", 2
			switch which {
			case "empty":
				args = append(args, "-t", "")
			case "blank stdin":
				args = append(args, "-t", "-")
				r, w, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				w.WriteString("  \n")
				w.Close()
				defer func(f *os.File) { os.Stdin = f }(os.Stdin)
				os.Stdin = r
				want = "task"
			case "unfinished":
				tr, source, output, cfg = resumeFixture(t)
				args = []string{"--resume", source, "--cwd", tr.Root, "-c", cfg, "-o", output, "--no-stream", "-t", "more"}
				want = "mutually exclusive"
			case "step limit":
				args = append(args, "-t", "more", "--step-limit", "2")
				want, wantCode = "", -1
			}
			before, _ := os.ReadFile(source)
			code := 0
			stderr := captureStderr(t, func() { code, _, _ = runWith(t, args...) })
			if len(*seen) != 0 {
				t.Fatalf("made %d requests", len(*seen))
			}
			if wantCode == -1 {
				got, err := trajectory.Load(output)
				if err != nil || got.ExitStatus != agent.StatusStepLimit || got.Steps != 2 {
					t.Fatalf("exit %d, %v %+v: %s", code, err, got, stderr)
				}
			} else if code != wantCode || !strings.Contains(stderr, want) {
				t.Fatalf("exit %d, want %d %q: %s", code, wantCode, want, stderr)
			}
			after, _ := os.ReadFile(source)
			if string(before) != string(after) {
				t.Fatal("source modified")
			}
		})
	}
}
