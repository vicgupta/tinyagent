package main

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/eventlog"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

const askUserReply = `{"role":"assistant","content":"","tool_calls":[{"id":"q1","type":"function","function":{"name":"ask_user","arguments":"{\"question\":\"Per key or per IP?\",\"options\":[\"per key\",\"per IP\"]}"}}]}`

// askServer answers each chat request with the next raw assistant message
// (the last one repeats), recording the request messages.
func askServer(t *testing.T, replies ...string) *[][]model.Message {
	t.Helper()
	seen := &[][]model.Message{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Messages []model.Message }
		json.NewDecoder(r.Body).Decode(&body)
		*seen = append(*seen, body.Messages)
		reply := replies[min(len(*seen), len(replies))-1]
		io.WriteString(w, `{"choices":[{"message":`+reply+`,"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":20}}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_API_KEY", "fake")
	t.Setenv("AGENT_MODEL", "")
	return seen
}

// askSetup returns a canonical root with a .git and an empty config.
func askSetup(t *testing.T) (root, cfg string) {
	t.Helper()
	root, err := canonicalRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	os.Mkdir(filepath.Join(root, ".git"), 0o755)
	cfg = filepath.Join(root, "none.yaml")
	os.WriteFile(cfg, nil, 0o644)
	return root, cfg
}

// askRun runs tinyagent, returning the exit code, stdout and stderr.
func askRun(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	stderr = captureStderr(t, func() {
		stdout = captureStdout(t, func() { code, _, _ = runWith(t, append([]string{"--no-stream", "-m", "openai/gpt-4o"}, args...)...) })
	})
	return code, stdout, stderr
}

func TestCLIAskUserNeedsInput(t *testing.T) { // AC2, AC4
	root, cfg := askSetup(t)
	seen := askServer(t, askUserReply, `{"role":"assistant","content":"rate limited per key"}`)
	out := filepath.Join(root, "run.json")
	code, stdout, stderr := askRun(t, "--ask-user", "-t", "add rate limiting", "--cwd", root, "-c", cfg, "-o", out)
	if code != exitNeedsInput || len(*seen) != 1 {
		t.Fatalf("exit %d, %d requests: %s", code, len(*seen), stderr)
	}
	if strings.TrimSpace(stdout) != "Per key or per IP?" {
		t.Errorf("stdout = %q", stdout)
	}
	next := filepath.Join(root, "run-2.json")
	for _, want := range []string{
		"NeedsInput after 1 steps", ": Per key or per IP?", "options: per key | per IP",
		"answer with: tinyagent --resume " + shellQuote(out) + " --cwd " + shellQuote(root) + ` -t "<answer>" -o ` + shellQuote(next),
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	tr, err := trajectory.Load(out)
	if err != nil || tr.ExitStatus != agent.StatusNeedsInput || tr.PendingQuestion == nil || len(tr.PendingCalls) != 1 {
		t.Fatalf("checkpoint: %v %+v", err, tr)
	}

	code, _, stderr = askRun(t, "--resume", out, "--cwd", root, "-c", cfg, "-o", next, "-t", "per key")
	if code != 0 || len(*seen) != 2 {
		t.Fatalf("answer exit %d: %s", code, stderr)
	}
	msgs := (*seen)[1]
	last, prev := msgs[len(msgs)-1], msgs[len(msgs)-2]
	if last.Role != "tool" || last.ToolCallID != "q1" || !strings.HasSuffix(last.Content, "per key") || prev.Role != "assistant" || len(prev.ToolCalls) != 1 {
		t.Fatalf("answer not sent as the ask_user result: %+v / %+v", prev, last)
	}
	got, err := trajectory.Load(next)
	if err != nil || got.ExitStatus != agent.StatusCompleted || got.Resume == nil || !got.Resume.Answer || got.QuestionsAsked != 1 {
		t.Fatalf("answered run: %v %+v", err, got)
	}
}

func TestCLIAskUserJSONAndLogDB(t *testing.T) { // AC3
	root, cfg := askSetup(t)
	askServer(t, askUserReply)
	db := filepath.Join(root, "log.db")
	code, stdout, stderr := askRun(t, "--ask-user", "--json", "--log-db="+db, "-t", "x", "--cwd", root, "-c", cfg, "-o", filepath.Join(root, "run.json"))
	if code != exitNeedsInput {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	results := jsonEvents(t, stdout, "result")
	if len(results) != 1 {
		t.Fatalf("result events: %v", results)
	}
	r := results[0]
	q, _ := r["question"].(map[string]any)
	if r["status"] != "NeedsInput" || r["submission"] != "Per key or per IP?" || q == nil || q["tool_call_id"] != "q1" || q["question"] != "Per key or per IP?" {
		t.Fatalf("result event: %v", r)
	}
	if opts, _ := q["options"].([]any); len(opts) != 2 || opts[0] != "per key" {
		t.Fatalf("options: %v", q["options"])
	}
	sdb, err := sql.Open("sqlite", eventlog.DSN(db))
	if err != nil {
		t.Fatal(err)
	}
	defer sdb.Close()
	var status, payload string
	if err := sdb.QueryRow(`SELECT status FROM runs`).Scan(&status); err != nil || status != "NeedsInput" {
		t.Fatalf("runs.status = %q, %v", status, err)
	}
	if err := sdb.QueryRow(`SELECT payload FROM events WHERE type = 'result'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, "Per key") || !strings.Contains(payload, "question_len") {
		t.Fatalf("metadata level stored the question body: %s", payload)
	}
}

func TestCLIAskUserUsageErrors(t *testing.T) { // AC5, AC9, AC12
	root, cfg := askSetup(t)
	seen := askServer(t, askUserReply)
	out := filepath.Join(root, "run.json")
	if code, _, stderr := askRun(t, "--ask-user", "-t", "x", "--cwd", root, "-c", cfg, "-o", out); code != exitNeedsInput {
		t.Fatalf("setup exit %d: %s", code, stderr)
	}
	schema := filepath.Join(root, "s.json")
	os.WriteFile(schema, []byte(cliSchema), 0o644)
	next := filepath.Join(root, "next.json")
	img := filepath.Join(root, "a.png")
	os.WriteFile(img, []byte("\x89PNG\r\n\x1a\n"), 0o644)
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"schema", []string{"--ask-user", "--output-schema", schema, "-t", "x", "--cwd", root, "-c", cfg, "-o", next}, "--output-schema"},
		{"no output", []string{"--ask-user", "-t", "x", "--cwd", root, "-c", cfg}, "requires -o"},
		{"no answer", []string{"--resume", out, "--cwd", root, "-c", cfg, "-o", next}, "Per key or per IP?"},
		{"empty answer", []string{"--resume", out, "--cwd", root, "-c", cfg, "-o", next, "-t", " "}, "Per key or per IP?"},
		{"image", []string{"--resume", out, "--cwd", root, "-c", cfg, "-o", next, "-t", "per key", "--image", img}, "--image"},
		{"setting", []string{"--resume", out, "--cwd", root, "-c", cfg, "-o", next, "-t", "per key", "--ask-user=false"}, "ask_user setting"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(*seen)
			code, _, stderr := askRun(t, tc.args...)
			if code != 2 || len(*seen) != before || !strings.Contains(stderr, tc.want) {
				t.Fatalf("exit %d, %d requests, stderr: %s", code, len(*seen)-before, stderr)
			}
			if _, err := os.Stat(next); !os.IsNotExist(err) {
				t.Fatal("output written")
			}
		})
	}
}

func TestCLIAskUserConfigAndCheck(t *testing.T) {
	root, _ := askSetup(t)
	cfg := filepath.Join(root, "ask.yaml")
	os.WriteFile(cfg, []byte("ask_user:\n  enabled: true\n  max_questions: 5\n"), 0o644)
	t.Setenv("OPENAI_API_KEY", "fake")
	t.Setenv("AGENT_MODEL", "")
	code, stdout, stderr := askRun(t, "--check", "--cwd", root, "-c", cfg)
	if code != 0 || !strings.Contains(stdout, "ask_user:  on, at most 5 questions per run (from config)") {
		t.Fatalf("exit %d: %s%s", code, stdout, stderr)
	}
	// Enabled by config, a run without -o is refused.
	if code, _, stderr := askRun(t, "-t", "x", "--cwd", root, "-c", cfg); code != 2 || !strings.Contains(stderr, "requires -o") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	os.WriteFile(cfg, []byte("ask_user:\n  max_questions: 0\n"), 0o644)
	if code, _, stderr := askRun(t, "--check", "--cwd", root, "-c", cfg); code != 2 || !strings.Contains(stderr, "ask_user.max_questions") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}
