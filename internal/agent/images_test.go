package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/hooks/hookstest"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

var testPNG = append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), []byte(strings.Repeat("p", 100))...)

func imageAgent(t *testing.T, m model.Model) *Agent {
	a := newAgent(t, m)
	a.Tools.Images = true
	if err := os.MkdirAll(filepath.Join(a.Tools.Root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.Tools.Root, "docs", "ui.png"), testPNG, 0o644); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestImagesOffKeepsSignature(t *testing.T) {
	for _, ro := range []bool{false, true} {
		for _, plan := range []bool{false, true} {
			if ImagesToolSignature(ro, plan, false) != PlanToolSignature(ro, plan) {
				t.Errorf("signature changed with images off (read-only %v, plan %v)", ro, plan)
			}
			if ImagesToolSignature(ro, plan, true) == PlanToolSignature(ro, plan) {
				t.Errorf("images did not change the signature")
			}
		}
	}
	a := newAgent(t, &scripted{})
	for _, s := range a.Tools.Catalog() {
		if s.Function.Name == tools.ViewImageToolName {
			t.Error("view_image offered with images off")
		}
	}
}

func TestViewImageReachesModelNotTextChannels(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "view_image", `{"path":"docs/ui.png"}`)}},
		submit,
	}}
	a := imageAgent(t, m)
	stdin := filepath.Join(t.TempDir(), "stdin.json")
	withHooks(t, a, config.Hooks{PostTool: []config.Hook{hookstest.Hook(t, "view_image", "stdin", stdin)}})
	var events []string
	a.OnEvent = func(kind string, f map[string]any) {
		b, _ := json.Marshal(f)
		events = append(events, kind+" "+string(b))
	}
	var out strings.Builder
	a.Out, a.Verbose = &out, true
	tr := a.Run(context.Background(), "look")
	if tr.ExitStatus != StatusSubmitted || !tr.ImagesEnabled {
		t.Fatalf("status %s, images_enabled %v", tr.ExitStatus, tr.ImagesEnabled)
	}
	sent := m.seen[1][len(m.seen[1])-1]
	if len(sent.Images) != 1 || sent.Images[0].MIMEType != "image/png" || sent.Images[0].Size() != len(testPNG) {
		t.Fatalf("model got images %v", sent.Images)
	}
	if !strings.HasPrefix(sent.Content, "image docs/ui.png (image/png, 116 bytes)\n[image: image/png, 116 bytes, sha256:") {
		t.Errorf("model text = %q", sent.Content)
	}
	data := sent.Images[0].Data
	hookIn, _ := os.ReadFile(stdin)
	for name, s := range map[string]string{"events": strings.Join(events, "\n"), "verbose": out.String(), "hook stdin": string(hookIn)} {
		if strings.Contains(s, data) {
			t.Errorf("%s contains base64 image data", name)
		}
		if !strings.Contains(s, "sha256:") {
			t.Errorf("%s lacks the placeholder: %s", name, s)
		}
	}
}

func TestViewImageErrorsAndReadOnly(t *testing.T) {
	m := &scripted{}
	a := imageAgent(t, m)
	a.Tools.ReadOnly = true
	a.Tools.MaxImageBytes = 1000
	root := a.Tools.Root
	os.WriteFile(filepath.Join(root, "x.svg"), []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), 0o644)
	os.WriteFile(filepath.Join(root, "fake.png"), []byte("just text"), 0o644)
	os.WriteFile(filepath.Join(root, "big.png"), append(append([]byte(nil), testPNG...), make([]byte, 1884)...), 0o644)
	cases := map[string]string{
		"x.svg":          "x.svg is not a PNG, JPEG, GIF, or WebP image",
		"fake.png":       "fake.png is not a PNG, JPEG, GIF, or WebP image",
		"docs":           "docs is a directory",
		"missing.png":    "no such file",
		"../outside.png": "outside",
		"big.png":        "big.png is 2,000 bytes; max_image_bytes is 1,000",
	}
	for path, want := range cases {
		out, images, isErr := a.Tools.RunWithImages(context.Background(), call("c", "view_image", `{"path":"`+path+`"}`))
		if !isErr || len(images) != 0 || !strings.Contains(out, want) {
			t.Errorf("%s: %q, %d images, isErr %v; want error containing %q", path, out, len(images), isErr, want)
		}
	}
	if _, images, isErr := a.Tools.RunWithImages(context.Background(), call("c", "view_image", `{"path":"docs/ui.png"}`)); isErr || len(images) != 1 {
		t.Errorf("read-only view_image failed")
	}
	if !a.Tools.Permits("view_image") || a.Tools.Permits("bash") || a.Tools.Permits("write_file") {
		t.Error("read-only images catalog is wrong")
	}
	if out, isErr := a.Tools.RunWithError(context.Background(), call("c", "bash", `{"command":"true"}`)); !isErr || !strings.Contains(out, "read-only") {
		t.Errorf("bash in read-only = %q", out)
	}
}

func imageResult(id string) model.Message {
	im := model.NewImage("image/png", "docs/ui.png", testPNG)
	return model.Message{Role: "tool", ToolCallID: id, Content: "image docs/ui.png\n" + im.Placeholder(), Images: []model.Image{im}}
}

func TestCompactElidesImages(t *testing.T) {
	m := imageResult("c")
	if got := msgChars(m); got != len(m.Content)+model.ImageTokenEstimate*4 {
		t.Errorf("msgChars = %d; images must count as a fixed estimate", got)
	}
	e := elide(m)
	if len(e.Images) != 0 || !strings.Contains(e.Content, "[image elided: docs/ui.png, 116 bytes]") {
		t.Errorf("elided = %+v", e)
	}

	steps := []reply{}
	for i := 1; i <= 10; i++ {
		steps = append(steps, reply{msg: model.Message{Role: "assistant", Content: "s", ToolCalls: []model.ToolCall{call(fmt.Sprintf("v%d", i), "view_image", `{"path":"docs/ui.png"}`)}}})
	}
	steps[9].input = 15500
	steps = append(steps, reply{msg: submit})
	f := &fakeModel{replies: steps, summary: "sum"}
	a := imageAgent(t, f)
	a.Config.ContextWindow = 20000
	a.Err = &strings.Builder{}
	tr := a.Run(context.Background(), "look")
	if tr.ExitStatus != StatusSubmitted || len(tr.Compactions) == 0 {
		t.Fatalf("status %s, compactions %d", tr.ExitStatus, len(tr.Compactions))
	}
	last := f.agentRequests()[len(f.agentRequests())-1].msgs
	elided, kept := 0, 0
	for _, m := range last {
		if strings.Contains(m.Content, "[image elided: docs/ui.png") {
			elided++
			if len(m.Images) > 0 {
				t.Error("elided message still carries images")
			}
		}
		kept += len(m.Images)
	}
	if elided == 0 || kept == 0 {
		t.Errorf("elided %d, kept %d; want old images elided and recent kept", elided, kept)
	}
	// Reloaded views elide the same way.
	for _, m := range viewFromTrajectory(tr).msgs {
		if strings.Contains(m.Content, "[image elided:") && len(m.Images) > 0 {
			t.Error("reloaded view kept elided image")
		}
	}
}

func TestSummarizerGetsNoImageData(t *testing.T) {
	f := &fakeModel{summary: "sum"}
	a := imageAgent(t, f)
	im := imageResult("c")
	tr := &trajectory.Trajectory{}
	if _, err := a.summarize(context.Background(), tr, "task", []model.Message{{Role: "assistant", ToolCalls: []model.ToolCall{call("c", "view_image", "{}")}}, im}); err != nil {
		t.Fatal(err)
	}
	for _, m := range f.reqs[0].msgs {
		if len(m.Images) > 0 || strings.Contains(m.Content, im.Images[0].Data) {
			t.Error("summarizer request carries image data")
		}
	}
}

func TestResumeImagesMismatchAndReplay(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "view_image", `{"path":"docs/ui.png"}`)}},
	}}
	a := imageAgent(t, m)
	tr := a.Run(context.Background(), "look") // ends: out of replies
	data, err := json.Marshal(tr)
	if err != nil {
		t.Fatal(err)
	}
	load := func() *trajectory.Trajectory {
		var c trajectory.Trajectory
		if err := json.Unmarshal(data, &c); err != nil {
			t.Fatal(err)
		}
		return &c
	}
	if err := ValidateResume(load(), a.Config, a.Tools.Root); err != nil {
		t.Fatalf("ValidateResume: %v", err)
	}
	// Same setting: the first resumed request carries the original image.
	m2 := &scripted{replies: []model.Message{submit}}
	b := &Agent{Model: m2, ModelName: "fake", Tools: &tools.Executor{Root: a.Tools.Root, Images: true}, Config: config.Default(), Resume: load()}
	if got := b.Run(context.Background(), ""); got.ExitStatus != StatusSubmitted {
		t.Fatalf("resume status %s: %s", got.ExitStatus, got.Submission)
	}
	var replayed []model.Image
	for _, msg := range m2.seen[0] {
		replayed = append(replayed, msg.Images...)
	}
	if len(replayed) != 1 || replayed[0].Data != model.NewImage("", "", testPNG).Data {
		t.Errorf("resumed images = %d", len(replayed))
	}
	// Different setting: refused before any request.
	m3 := &scripted{}
	c := &Agent{Model: m3, ModelName: "fake", Tools: &tools.Executor{Root: a.Tools.Root}, Config: config.Default(), Resume: load()}
	if got := c.Run(context.Background(), ""); got.ExitStatus != StatusError || len(m3.seen) != 0 {
		t.Errorf("mismatched resume = %s %q, %d requests", got.ExitStatus, got.Submission, len(m3.seen))
	}
	// A checkpoint claiming images off but holding images is rejected.
	bad := load()
	bad.ImagesEnabled = false
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "images") {
		t.Errorf("Validate = %v", err)
	}
}
