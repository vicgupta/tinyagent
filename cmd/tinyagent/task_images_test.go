package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

var (
	cliPNG  = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDRfakepixels")
	cliJPEG = []byte("\xff\xd8\xff\xe0\x00\x10JFIFfake")
)

// rawServer answers every request with reply, recording each body and path.
type rawServer struct {
	mu     sync.Mutex
	paths  []string
	bodies []string
}

func (s *rawServer) start(t *testing.T, reply string) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.paths, s.bodies = append(s.paths, r.URL.Path), append(s.bodies, string(b))
		s.mu.Unlock()
		io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// imageFiles writes a.png and b.jpg into a new directory.
func imageFiles(t *testing.T) (a, b string) {
	dir := t.TempDir()
	a, b = filepath.Join(dir, "a.png"), filepath.Join(dir, "b.jpg")
	os.WriteFile(a, cliPNG, 0o644)
	os.WriteFile(b, cliJPEG, 0o644)
	return a, b
}

func dataURL(mime string, raw []byte) string {
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw)
}

// firstUser returns the first user message of the first request to a path
// ending in suffix.
func firstUser(t *testing.T, s *rawServer, suffix, key string) map[string]any {
	t.Helper()
	for i, p := range s.paths {
		if !strings.HasSuffix(p, suffix) {
			continue
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(s.bodies[i]), &body); err != nil {
			t.Fatal(err)
		}
		for _, m := range body[key].([]any) {
			if m := m.(map[string]any); m["role"] == "user" {
				return m
			}
		}
	}
	t.Fatalf("no request to %s in %v", suffix, s.paths)
	return nil
}

func TestCLITaskImagesPerProvider(t *testing.T) { // AC1, AC2, AC3
	a, b := imageFiles(t)
	labels := "[attached image 1: a.png, image/png, 26 bytes]\n[attached image 2: b.jpg, image/jpeg, 14 bytes]"
	for _, c := range []struct {
		name, model, reply, path string
		env                      func(url string)
		args                     []string
		check                    func(t *testing.T, s *rawServer)
	}{
		{"anthropic", "anthropic/claude-sonnet-4-5", `{"content":[{"type":"text","text":"COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`, "/v1/messages",
			func(url string) { t.Setenv("ANTHROPIC_BASE_URL", url); t.Setenv("ANTHROPIC_API_KEY", "k") }, nil,
			func(t *testing.T, s *rawServer) {
				blocks := firstUser(t, s, "/v1/messages", "messages")["content"].([]any)
				if len(blocks) != 3 {
					t.Fatalf("blocks = %v", blocks)
				}
				for i, want := range []string{"image/png", "image/jpeg"} {
					if src := blocks[i].(map[string]any)["source"].(map[string]any); src["type"] != "base64" || src["media_type"] != want {
						t.Errorf("block %d = %v", i, blocks[i])
					}
				}
				if text := blocks[2].(map[string]any)["text"].(string); !strings.HasPrefix(text, "x") || !strings.HasSuffix(text, labels) {
					t.Errorf("text = %q", text)
				}
			}},
		{"openai responses", "openai/gpt-5", responsesSubmit, "/responses",
			func(url string) { t.Setenv("OPENAI_BASE_URL", url); t.Setenv("OPENAI_API_KEY", "k") }, []string{"--openai-api", "responses"},
			func(t *testing.T, s *rawServer) {
				parts := firstUser(t, s, "/responses", "input")["content"].([]any)
				if len(parts) != 3 || parts[0].(map[string]any)["image_url"] != dataURL("image/png", cliPNG) || parts[1].(map[string]any)["image_url"] != dataURL("image/jpeg", cliJPEG) || parts[2].(map[string]any)["type"] != "input_text" {
					t.Errorf("parts = %v", parts)
				}
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, cfg := openaiAPISetup(t)
			s := &rawServer{}
			c.env(s.start(t, c.reply))
			args := append([]string{"-t", "x", "-m", c.model, "--cwd", dir, "-c", cfg, "--no-stream", "--image", a, "--image", b}, c.args...)
			var code int
			stderr := captureStderr(t, func() { captureStdout(t, func() { code, _, _ = runWith(t, args...) }) })
			if code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			c.check(t, s)
		})
	}
	chatCheck := func(t *testing.T, s *rawServer) {
		m := firstUser(t, s, "/chat/completions", "messages")
		raw, _ := json.Marshal(m)
		parts, ok := m["content"].([]any)
		if !ok || len(parts) != 3 || strings.Contains(string(raw), "image not shown") {
			t.Fatalf("user message = %s", raw)
		}
		for i, want := range []string{dataURL("image/png", cliPNG), dataURL("image/jpeg", cliJPEG)} {
			if p := parts[i].(map[string]any); p["type"] != "image_url" || p["image_url"].(map[string]any)["url"] != want {
				t.Errorf("part %d = %v", i, p)
			}
		}
		if p := parts[2].(map[string]any); p["type"] != "text" || !strings.HasSuffix(p["text"].(string), labels) {
			t.Errorf("text part = %v", p)
		}
	}
	for _, c := range []struct{ name, model, env string }{
		{"openai chat", "openai/gpt-4o", "OPENAI_BASE_URL"},
		{"ollama", "ollama/llava", "OLLAMA_HOST"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, cfg := openaiAPISetup(t)
			s := &rawServer{}
			t.Setenv(c.env, s.start(t, chatSubmit))
			var code int
			stderr := captureStderr(t, func() {
				captureStdout(t, func() {
					code, _, _ = runWith(t, "-t", "x", "-m", c.model, "--cwd", dir, "-c", cfg, "--no-stream", "--openai-api", "chat", "--image", a, "--image", b)
				})
			})
			if code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			chatCheck(t, s)
		})
	}
}

func TestCLITaskImageValidation(t *testing.T) { // AC5, AC6
	a, _ := imageFiles(t)
	dir, cfg := openaiAPISetup(t)
	fake := filepath.Join(dir, "fake.png")
	os.WriteFile(fake, []byte("just text"), 0o644)
	small := filepath.Join(dir, "small.yaml")
	os.WriteFile(small, []byte("max_image_bytes: 10\n"), 0o644)
	many := []string{}
	for range model.MaxTaskImages + 1 {
		many = append(many, "--image", a)
	}
	for _, c := range []struct {
		name, want string
		args       []string
	}{
		{"missing", "no such file", []string{"-t", "x", "--image", filepath.Join(dir, "nope.png")}},
		{"directory", "is a directory", []string{"-t", "x", "--image", dir}},
		{"not an image", "is not a PNG, JPEG, GIF, or WebP image", []string{"-t", "x", "--image", fake}},
		{"too large", "max_image_bytes is 10", []string{"-t", "x", "--image", a, "-c", small}},
		{"too many", "at most 10 may be attached", append([]string{"-t", "x"}, many...)},
		{"no task", "--image requires a task", []string{"--image", a}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := &rawServer{}
			t.Setenv("OPENAI_BASE_URL", s.start(t, chatSubmit))
			var code int
			stderr := captureStderr(t, func() {
				code, _, _ = runWith(t, append([]string{"-m", "openai/gpt-4o", "--cwd", dir, "-c", cfg, "--no-stream"}, c.args...)...)
			})
			if code != 2 || !strings.Contains(stderr, c.want) || len(s.bodies) != 0 {
				t.Errorf("exit %d, %d requests, stderr: %s", code, len(s.bodies), stderr)
			}
		})
	}
	// A PNG named .jpg is sent as image/png.
	png := filepath.Join(dir, "shot.jpg")
	os.WriteFile(png, cliPNG, 0o644)
	s := &rawServer{}
	t.Setenv("OPENAI_BASE_URL", s.start(t, chatSubmit))
	var code int
	captureStderr(t, func() {
		captureStdout(t, func() {
			code, _, _ = runWith(t, "-t", "x", "-m", "openai/gpt-4o", "--cwd", dir, "-c", cfg, "--no-stream", "--image", png)
		})
	})
	if code != 0 || len(s.bodies) != 1 || !strings.Contains(s.bodies[0], dataURL("image/png", cliPNG)) || !strings.Contains(s.bodies[0], "shot.jpg, image/png") {
		t.Errorf("exit %d, bodies %v", code, s.bodies)
	}
}

func TestCLITaskImagesStayOutOfTextChannels(t *testing.T) { // AC7
	a, b := imageFiles(t)
	dir, cfg := openaiAPISetup(t)
	s := &rawServer{}
	t.Setenv("OPENAI_BASE_URL", s.start(t, chatSubmit))
	db := filepath.Join(t.TempDir(), "x.db")
	var code int
	var stdout string
	stderr := captureStderr(t, func() {
		stdout = captureStdout(t, func() {
			code, _, _ = runWith(t, "-t", "x", "-m", "openai/gpt-4o", "--cwd", dir, "-c", cfg, "--json", "-v", "--log-db="+db, "--image", a, "--image", b)
		})
	})
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	dbBytes, _ := os.ReadFile(db)
	for _, raw := range [][]byte{cliPNG, cliJPEG} {
		data := base64.StdEncoding.EncodeToString(raw)
		for name, out := range map[string]string{"stdout": stdout, "stderr": stderr, "log db": string(dbBytes)} {
			if strings.Contains(out, data) {
				t.Errorf("%s contains image data", name)
			}
		}
	}
	att, _ := runStarted(t, stdout)["attachments"].([]any)
	if len(att) != 2 {
		t.Fatalf("run.started attachments = %v", runStarted(t, stdout))
	}
	first := att[0].(map[string]any)
	if first["index"] != 1.0 || first["name"] != "a.png" || first["mime_type"] != "image/png" || first["bytes"] != 26.0 || len(first["sha256"].(string)) != 64 {
		t.Errorf("attachment = %v", first)
	}
	if !strings.Contains(stdout+stderr, "attached image 2: b.jpg (image/jpeg, 14 bytes)") {
		t.Errorf("no verbose attachment line:\n%s\n%s", stdout, stderr)
	}
}

func TestCLIResumeTaskImages(t *testing.T) { // AC8, AC9
	a, _ := imageFiles(t)
	// Unfinished checkpoint: --image has no new message to attach to.
	tr, source, output, cfg := resumeFixture(t)
	var code int
	stderr := captureStderr(t, func() {
		code, _, _ = runWith(t, "--resume", source, "--cwd", tr.Root, "-c", cfg, "-o", output, "--image", a)
	})
	if code != 2 || !strings.Contains(stderr, "--image needs a follow-up task") {
		t.Errorf("exit %d: %s", code, stderr)
	}
	// Finished checkpoint without -t.
	tr, source, output, cfg = finishedFixture(t)
	stderr = captureStderr(t, func() {
		code, _, _ = runWith(t, "--resume", source, "--cwd", tr.Root, "-c", cfg, "-o", output, "--image", a)
	})
	if code != 2 || !strings.Contains(stderr, "--image requires a follow-up task") {
		t.Errorf("exit %d: %s", code, stderr)
	}
	// Follow-up with an attachment.
	s := &rawServer{}
	t.Setenv("OPENAI_BASE_URL", s.start(t, chatSubmit))
	t.Setenv("AGENT_MODEL", "")
	stderr = captureStderr(t, func() {
		captureStdout(t, func() {
			code, _, _ = runWith(t, "--resume", source, "--cwd", tr.Root, "-c", cfg, "-o", output, "--no-stream", "-t", "see this", "--image", a)
		})
	})
	if code != 0 || len(s.bodies) != 1 {
		t.Fatalf("exit %d, %d requests: %s", code, len(s.bodies), stderr)
	}
	got, err := trajectory.Load(output)
	if err != nil {
		t.Fatal(err)
	}
	fu := got.Messages[len(got.Messages)-2]
	if !strings.HasPrefix(fu.Content, trajectory.FollowUpNotice) || len(fu.Images) != 1 || fu.Images[0].Label != "a.png" || got.Resume == nil || !got.Resume.FollowUp {
		t.Errorf("follow-up message = %q, %d images", fu.Content, len(fu.Images))
	}
	if !strings.Contains(s.bodies[0], dataURL("image/png", cliPNG)) {
		t.Error("follow-up request has no image")
	}
}
