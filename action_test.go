package tinyagent_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/vicgupta/tinyagent/internal/agent"
	"gopkg.in/yaml.v3"
)

// The tests below run action.yml's composite steps the way the Actions
// runner does: ${{ }} expressions are resolved into step env:, each step runs
// under `bash --noprofile --norc -eo pipefail` with its own GITHUB_OUTPUT
// file, and the action's outputs are mapped from the step outputs. Model
// requests go to a local stub, so no keys or network are needed.

type actionMeta struct {
	Inputs map[string]struct {
		Default string `yaml:"default"`
	} `yaml:"inputs"`
	Outputs map[string]struct {
		Value string `yaml:"value"`
	} `yaml:"outputs"`
	Runs struct {
		Using string `yaml:"using"`
		Steps []struct {
			ID    string            `yaml:"id"`
			Shell string            `yaml:"shell"`
			Env   map[string]string `yaml:"env"`
			Run   string            `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"runs"`
}

func loadAction(t *testing.T) actionMeta {
	t.Helper()
	data, err := os.ReadFile("action.yml")
	if err != nil {
		t.Fatal(err)
	}
	var a actionMeta
	if err := yaml.Unmarshal(data, &a); err != nil {
		t.Fatal(err)
	}
	return a
}

var expr = regexp.MustCompile(`\$\{\{\s*([^}]*?)\s*\}\}`)

func resolve(t *testing.T, s string, ctx map[string]string) string {
	t.Helper()
	return expr.ReplaceAllStringFunc(s, func(m string) string {
		name := expr.FindStringSubmatch(m)[1]
		if strings.HasPrefix(name, "steps.") {
			return ctx[name] // unset step outputs are empty
		}
		v, ok := ctx[name]
		if !ok {
			t.Fatalf("unknown expression %s", m)
		}
		return v
	})
}

// parseOutputs reads a GITHUB_OUTPUT file as the runner does.
func parseOutputs(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if line == "" {
			continue
		}
		eq, hd := strings.Index(line, "="), strings.Index(line, "<<")
		if eq >= 0 && (hd < 0 || eq < hd) {
			out[line[:eq]] = line[eq+1:]
			continue
		}
		if hd < 0 {
			t.Fatalf("bad output line %q", line)
		}
		name, delim := line[:hd], line[hd+2:]
		var val []string
		for i++; ; i++ {
			if i >= len(lines) {
				t.Fatalf("unterminated output %s", name)
			}
			if lines[i] == delim {
				break
			}
			val = append(val, lines[i])
		}
		out[name] = strings.Join(val, "\n")
	}
	return out
}

type actionRun struct {
	outputs map[string]string
	failed  string // id of the failing step, "" if all passed
	log     string
	summary string
}

type actionEnv struct {
	ref       string            // github.action_ref
	workspace string            // github.workspace and the step directory
	env       map[string]string // job/step env (e.g. OPENAI_BASE_URL)
}

func runAction(t *testing.T, ae actionEnv, inputs map[string]string) actionRun {
	t.Helper()
	a := loadAction(t)
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if ae.workspace == "" {
		ae.workspace = t.TempDir()
	}
	ctx := map[string]string{"github.action_ref": ae.ref, "github.workspace": ae.workspace}
	for name, in := range a.Inputs {
		ctx["inputs."+name] = resolve(t, in.Default, ctx)
	}
	for name, v := range inputs {
		if _, ok := a.Inputs[name]; !ok {
			t.Fatalf("action has no input %s", name)
		}
		ctx["inputs."+name] = v
	}
	tmp := t.TempDir()
	summary := filepath.Join(tmp, "summary.md")
	os.WriteFile(summary, nil, 0o644)
	runnerOS, runnerArch := map[string]string{"linux": "Linux", "darwin": "macOS"}[runtime.GOOS], map[string]string{"amd64": "X64", "arm64": "ARM64"}[runtime.GOARCH]
	base := map[string]string{
		"GITHUB_ACTION_PATH": root, "GITHUB_STEP_SUMMARY": summary, "RUNNER_TEMP": tmp,
		"RUNNER_OS": runnerOS, "RUNNER_ARCH": runnerArch, "XDG_CONFIG_HOME": t.TempDir(),
	}
	for k, v := range ae.env {
		base[k] = v
	}
	var res actionRun
	var log strings.Builder
	for _, step := range a.Runs.Steps {
		output := filepath.Join(tmp, step.ID+".out")
		os.WriteFile(output, nil, 0o644)
		env := os.Environ()
		for k, v := range base {
			env = append(env, k+"="+v)
		}
		for k, v := range step.Env {
			env = append(env, k+"="+resolve(t, v, ctx))
		}
		env = append(env, "GITHUB_OUTPUT="+output)
		cmd := exec.Command("bash", "--noprofile", "--norc", "-eo", "pipefail", "-c", step.Run)
		cmd.Dir, cmd.Env = ae.workspace, env
		b, err := cmd.CombinedOutput()
		log.Write(b)
		for k, v := range parseOutputs(t, output) {
			ctx["steps."+step.ID+".outputs."+k] = v
		}
		if err != nil {
			res.failed = step.ID
			break
		}
	}
	res.log = log.String()
	res.outputs = map[string]string{}
	for name, o := range a.Outputs {
		res.outputs[name] = resolve(t, o.Value, ctx)
	}
	s, _ := os.ReadFile(summary)
	res.summary = string(s)
	return res
}

// stubModel is an OpenAI-compatible Chat Completions endpoint that serves
// one queued reply per request and records the request bodies.
type stubModel struct {
	mu      sync.Mutex
	replies []func(http.ResponseWriter)
	bodies  []string
	url     string
}

func newStubModel(t *testing.T, replies ...func(http.ResponseWriter)) *stubModel {
	t.Helper()
	s := &stubModel{replies: replies}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.bodies = append(s.bodies, string(b))
		if len(s.replies) == 0 {
			http.Error(w, `{"error":{"message":"no more replies"}}`, http.StatusBadRequest)
			return
		}
		reply := s.replies[0]
		s.replies = s.replies[1:]
		reply(w)
	}))
	t.Cleanup(srv.Close)
	s.url = srv.URL
	return s
}

func (s *stubModel) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.bodies...)
}

func (s *stubModel) env() map[string]string {
	return map[string]string{"OPENAI_BASE_URL": s.url, "OPENAI_API_KEY": "sk-stub-secret-4242"}
}

func chatReply(msg map[string]any, finish string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": msg, "finish_reason": finish}},
			"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1},
		})
	}
}

func submitReply(text string) func(http.ResponseWriter) {
	return chatReply(map[string]any{"role": "assistant", "content": text + "\n" + agent.SubmitMarker}, "stop")
}

func buildTinyagent(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("the action supports Linux and macOS runners")
	}
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("needs %s", tool)
		}
	}
	bin := filepath.Join(t.TempDir(), "tinyagent")
	if out, err := exec.Command("go", "build", "-o", bin, "./cmd/tinyagent").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func TestActionNoUntrustedContextInScripts(t *testing.T) {
	a := loadAction(t)
	if a.Runs.Using != "composite" {
		t.Fatalf("runs.using = %q", a.Runs.Using)
	}
	for _, step := range a.Runs.Steps {
		if strings.Contains(step.Run, "${{") {
			t.Errorf("step %s has an expression in its run: script", step.ID)
		}
		if step.Shell != "bash" {
			t.Errorf("step %s shell = %q, want bash", step.ID, step.Shell)
		}
	}
	for _, f := range []string{"action/install.sh", "action/run.sh"} {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if regexp.MustCompile(`set -[a-z]*x|\beval\b`).Match(data) {
			t.Errorf("%s uses set -x or eval", f)
		}
	}
}

func TestActionRun(t *testing.T) {
	bin := buildTinyagent(t)
	pwned := t.TempDir()
	task := "fix it $(touch " + pwned + "/a) `touch " + pwned + "/b` 'single' \"double\" ${HOME}\nsecond line \\ ; | & touch " + pwned + "/c"
	submission := "line one\nstatus=Hacked\nstatus<<EOF\nHacked\nEOF\nghadelimiter_0123\n::add-mask::x\nlast"
	stub := newStubModel(t, submitReply(submission))
	r := runAction(t, actionEnv{env: stub.env()}, map[string]string{
		"tinyagent-path": bin, "task": task, "model": "openai/gpt-4o",
		"args": "--no-stream\n\n--step-limit\n5\n", "job-summary": "true",
	})
	if r.failed != "" {
		t.Fatalf("step %s failed:\n%s", r.failed, r.log)
	}
	if entries, _ := os.ReadDir(pwned); len(entries) != 0 {
		t.Fatalf("the task ran shell commands: %v", entries)
	}
	reqs := stub.requests()
	if len(reqs) != 1 {
		t.Fatalf("%d model requests", len(reqs))
	}
	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	json.Unmarshal([]byte(reqs[0]), &body)
	found := false
	for _, m := range body.Messages {
		if s, ok := m.Content.(string); ok && m.Role == "user" && strings.Contains(s, task) {
			found = true
		}
	}
	if !found {
		t.Errorf("the task did not reach the model verbatim:\n%s", reqs[0])
	}
	want := map[string]string{"status": "Submitted", "submission": submission, "structured-output": "", "steps": "1", "exit-code": "0"}
	for k, v := range want {
		if r.outputs[k] != v {
			t.Errorf("output %s = %q, want %q", k, r.outputs[k], v)
		}
	}
	for _, k := range []string{"trajectory", "events"} {
		if st, err := os.Stat(r.outputs[k]); err != nil || st.Size() == 0 {
			t.Errorf("output %s = %q: %v", k, r.outputs[k], err)
		}
	}
	if !strings.Contains(r.summary, "Submitted") || !strings.Contains(r.summary, "line one") {
		t.Errorf("job summary lacks the transcript:\n%s", r.summary)
	}
	if !strings.Contains(r.log, "::stop-commands::") || strings.Contains(r.log, "sk-stub-secret-4242") {
		t.Errorf("log:\n%s", r.log)
	}
}

func TestActionSchemaAndTaskFile(t *testing.T) {
	bin := buildTinyagent(t)
	ws := t.TempDir()
	os.WriteFile(filepath.Join(ws, "task.md"), []byte("label this issue"), 0o644)
	os.WriteFile(filepath.Join(ws, "s.json"), []byte(`{"type":"object","properties":{"label":{"type":"string","enum":["bug","docs"]}},"required":["label"],"additionalProperties":false}`), 0o644)
	stub := newStubModel(t, chatReply(map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{
		"id": "c1", "type": "function", "function": map[string]any{"name": "submit_result", "arguments": `{"label": "bug"}`}}}}, "tool_calls"))
	r := runAction(t, actionEnv{workspace: ws, env: stub.env()}, map[string]string{
		"tinyagent-path": bin, "task-file": "task.md", "output-schema": "s.json", "read-only": "true",
		"model": "openai/gpt-4o", "args": "--no-stream",
	})
	if r.failed != "" {
		t.Fatalf("step %s failed:\n%s", r.failed, r.log)
	}
	if r.outputs["status"] != "Submitted" || r.outputs["structured-output"] != `{"label":"bug"}` {
		t.Fatalf("outputs %v\n%s", r.outputs, r.log)
	}
	if reqs := stub.requests(); len(reqs) != 1 || !strings.Contains(reqs[0], "label this issue") {
		t.Fatalf("requests %v", reqs)
	}
}

func TestActionReview(t *testing.T) {
	bin := buildTinyagent(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("needs git")
	}
	ws := t.TempDir()
	git := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = ws
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	os.WriteFile(filepath.Join(ws, "a.txt"), []byte("one\n"), 0o644)
	git("add", ".")
	git("commit", "-qm", "base")
	base := git("rev-parse", "HEAD")
	os.WriteFile(filepath.Join(ws, "a.txt"), []byte("one\nreview-me-sentinel\n"), 0o644)
	git("commit", "-qam", "change")

	stub := newStubModel(t, submitReply("looks fine"))
	r := runAction(t, actionEnv{workspace: ws, env: stub.env()}, map[string]string{
		"tinyagent-path": bin, "review-base": base, "model": "openai/gpt-4o", "args": "--no-stream",
	})
	if r.failed != "" || r.outputs["status"] != "Submitted" || r.outputs["submission"] != "looks fine" {
		t.Fatalf("failed %q, outputs %v\n%s", r.failed, r.outputs, r.log)
	}
	if reqs := stub.requests(); len(reqs) != 1 || !strings.Contains(reqs[0], "review-me-sentinel") {
		t.Fatalf("requests %v", reqs)
	}
}

func TestActionFailureMapping(t *testing.T) {
	bin := buildTinyagent(t)
	apiError := func(w http.ResponseWriter) {
		http.Error(w, `{"error":{"message":"bad request"}}`, http.StatusBadRequest)
	}
	truncated := chatReply(map[string]any{"role": "assistant", "content": "partial"}, "length")
	for _, c := range []struct {
		name        string
		replies     []func(http.ResponseWriter)
		inputs      map[string]string
		code        string
		status      string
		failed      bool
		wantSummary string
	}{
		{"error fails", []func(http.ResponseWriter){apiError}, nil, "1", "Error", true, "Error"},
		{"error tolerated", []func(http.ResponseWriter){apiError}, map[string]string{"fail-on-error": "false"}, "1", "Error", false, ""},
		{"truncated", []func(http.ResponseWriter){truncated, truncated, truncated}, nil, "3", "OutputTruncated", true, ""},
		{"truncated tolerated", []func(http.ResponseWriter){truncated, truncated, truncated}, map[string]string{"fail-on-error": "false"}, "3", "OutputTruncated", false, ""},
		{"usage error always fails", nil, map[string]string{"fail-on-error": "false", "args": "--step-limit=5 $(touch PWNED)"}, "2", "", true, ""},
		{"oversized summary", []func(http.ResponseWriter){submitReply(strings.Repeat("x", 1100000))}, nil, "0", "Submitted", false, "exceeds the 1 MiB job summary limit"},
	} {
		t.Run(c.name, func(t *testing.T) {
			stub := newStubModel(t, c.replies...)
			ws := t.TempDir()
			inputs := map[string]string{"tinyagent-path": bin, "task": "x", "model": "openai/gpt-4o", "args": "--no-stream", "job-summary": "true"}
			for k, v := range c.inputs {
				inputs[k] = v
			}
			r := runAction(t, actionEnv{workspace: ws, env: stub.env()}, inputs)
			if (r.failed != "") != c.failed || r.outputs["exit-code"] != c.code || r.outputs["status"] != c.status {
				t.Fatalf("failed %q, outputs %v\n%s", r.failed, r.outputs, r.log)
			}
			if c.wantSummary != "" && !strings.Contains(r.summary, c.wantSummary) {
				t.Errorf("summary %.300q lacks %q", r.summary, c.wantSummary)
			}
			if _, err := os.Stat(filepath.Join(ws, "PWNED")); err == nil {
				t.Error("args were shell-evaluated")
			}
		})
	}
}

func TestActionInputErrors(t *testing.T) {
	bin := buildTinyagent(t)
	for name, inputs := range map[string]map[string]string{
		"no task":           {},
		"task and file":     {"task": "x", "task-file": "f"},
		"bad boolean":       {"task": "x", "read-only": "yes"},
		"version and path":  {"task": "x", "version": "v1.0.0"},
		"path not a binary": {"task": "x", "tinyagent-path": "/nonexistent/tinyagent"},
	} {
		t.Run(name, func(t *testing.T) {
			stub := newStubModel(t)
			if _, ok := inputs["tinyagent-path"]; !ok {
				inputs["tinyagent-path"] = bin
			}
			r := runAction(t, actionEnv{env: stub.env()}, inputs)
			if r.failed == "" || len(stub.requests()) != 0 || !strings.Contains(r.log, "::error::tinyagent action:") {
				t.Fatalf("failed %q, %d requests\n%s", r.failed, len(stub.requests()), r.log)
			}
		})
	}
}

// TestActionInstall installs from a fixture release through a fake curl that
// serves files by name and logs the URLs it was asked for.
func TestActionInstall(t *testing.T) {
	bin := buildTinyagent(t)
	asset := "tinyagent-" + runtime.GOOS + "-" + runtime.GOARCH
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	good := hex.EncodeToString(sum[:]) + "  " + asset + "\n"
	bad := strings.Repeat("0", 64) + "  " + asset + "\n"

	for _, c := range []struct {
		name, ref, version, sums string
		asset                    bool
		env                      map[string]string
		wantURL, wantErr         string
	}{
		{"tag ref", "v1.2.3", "", good, true, nil, "/releases/download/v1.2.3/" + asset, ""},
		{"explicit version", "main", "v2.0.0-rc.1", good, true, nil, "/releases/download/v2.0.0-rc.1/", ""},
		{"explicit latest", "", "latest", good, true, nil, "/releases/download/v7.7.7/" + asset, ""},
		{"branch ref", "main", "", good, true, nil, "", "not a vX.Y.Z tag"},
		{"sha ref", "fec546f0aa0aa0aa0aa0aa0aa0aa0aa0aa0aa0aa", "", good, true, nil, "", "not a vX.Y.Z tag"},
		{"bad version", "", "v1; touch x", good, true, nil, "", "version must be"},
		{"checksum mismatch", "v1.2.3", "", bad, true, nil, "", "checksum mismatch"},
		{"missing checksum", "v1.2.3", "", "", true, nil, "", "no checksum"},
		{"missing asset", "v1.2.3", "", good, false, nil, "", "download failed"},
		{"windows", "v1.2.3", "", good, true, map[string]string{"RUNNER_OS": "Windows"}, "", "unsupported runner OS"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fixtures, fake := t.TempDir(), t.TempDir()
			os.WriteFile(filepath.Join(fixtures, "SHA256SUMS"), []byte(c.sums), 0o644)
			if c.asset {
				os.WriteFile(filepath.Join(fixtures, asset), data, 0o755)
			}
			urls := filepath.Join(fake, "urls")
			script := `#!/bin/sh
while [ $# -gt 1 ]; do case $1 in -o) out=$2; shift 2 ;; *) shift ;; esac; done
echo "$1" >>"` + urls + `"
case $1 in
	*/releases/latest) body='{"tag_name": "v7.7.7"}'; if [ "$out" = - ]; then echo "$body"; else echo "$body" >"$out"; fi; exit 0 ;;
esac
f="` + fixtures + `/${1##*/}"
[ -f "$f" ] || exit 22
cp "$f" "$out"
`
			os.WriteFile(filepath.Join(fake, "curl"), []byte(script), 0o755)
			stub := newStubModel(t, submitReply("ok"))
			env := stub.env()
			env["PATH"] = fake + string(os.PathListSeparator) + os.Getenv("PATH")
			for k, v := range c.env {
				env[k] = v
			}
			r := runAction(t, actionEnv{ref: c.ref, env: env}, map[string]string{
				"version": c.version, "task": "x", "model": "openai/gpt-4o", "args": "--no-stream",
			})
			got, _ := os.ReadFile(urls)
			if c.wantErr != "" {
				if r.failed != "install" || !strings.Contains(r.log, c.wantErr) || len(stub.requests()) != 0 {
					t.Fatalf("failed %q, %d requests, log:\n%s", r.failed, len(stub.requests()), r.log)
				}
				return
			}
			if r.failed != "" || r.outputs["status"] != "Submitted" || !strings.Contains(string(got), c.wantURL) {
				t.Fatalf("failed %q, outputs %v, urls %s\n%s", r.failed, r.outputs, got, r.log)
			}
			if !strings.Contains(r.log, "Installed tinyagent") {
				t.Errorf("log lacks the installed version:\n%s", r.log)
			}
		})
	}
}
