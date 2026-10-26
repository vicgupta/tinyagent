package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/hooks/hookstest"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

func taskImages() []model.Image {
	return []model.Image{
		model.NewImage("image/png", "mock.png", testPNG),
		model.NewImage("image/jpeg", "after.jpg", []byte("\xff\xd8\xff\xe0\x00\x10JFIFfake")),
	}
}

func TestTaskImagesReachModelNotTextChannels(t *testing.T) { // AC7, AC11
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "write_file", `{"path":"f.txt","content":"hi"}`)}},
		submit,
	}}
	a := newAgent(t, m)
	a.TaskImages = taskImages()
	stdin := filepath.Join(t.TempDir(), "stdin.json")
	withHooks(t, a, config.Hooks{PostTool: []config.Hook{hookstest.Hook(t, "write_file", "stdin", stdin)}})
	var events []string
	a.OnEvent = func(kind string, f map[string]any) {
		b, _ := json.Marshal(f)
		events = append(events, kind+" "+string(b))
	}
	var out strings.Builder
	a.Out, a.Verbose = &out, true
	tr := a.Run(context.Background(), "make it look like this")
	if tr.ExitStatus != StatusSubmitted || tr.ImagesEnabled || tr.Task != "make it look like this" {
		t.Fatalf("status %s, images_enabled %v, task %q", tr.ExitStatus, tr.ImagesEnabled, tr.Task)
	}
	for _, req := range m.seen {
		task := req[1]
		if !reflect.DeepEqual(task.Images, taskImages()) || task.Content != "make it look like this"+model.AttachmentLabels(taskImages()) {
			t.Fatalf("task message = %q, %d images", task.Content, len(task.Images))
		}
	}
	if !strings.Contains(m.seen[0][1].Content, "[attached image 1: mock.png, image/png, 116 bytes]") {
		t.Errorf("label missing: %q", m.seen[0][1].Content)
	}
	for _, s := range a.Tools.Catalog() {
		if s.Function.Name == tools.ViewImageToolName {
			t.Error("view_image offered without --images")
		}
	}
	hookIn, _ := os.ReadFile(stdin)
	for name, s := range map[string]string{"events": strings.Join(events, "\n"), "verbose": out.String(), "hook stdin": string(hookIn)} {
		for _, im := range taskImages() {
			if strings.Contains(s, im.Data) {
				t.Errorf("%s contains image data", name)
			}
		}
	}
	if err := roundTrip(t, tr).Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestTaskImagesSurviveResume(t *testing.T) { // AC8
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "write_file", `{"path":"f.txt","content":"hi"}`)}},
	}}
	a := newAgent(t, m)
	a.TaskImages = taskImages()
	tr := a.Run(context.Background(), "fix it") // ends: out of replies
	m2 := &scripted{replies: []model.Message{submit}}
	b := &Agent{Model: m2, ModelName: "fake", Tools: &tools.Executor{Root: a.Tools.Root}, Config: config.Default(), Resume: roundTrip(t, tr)}
	if got := b.Run(context.Background(), ""); got.ExitStatus != StatusSubmitted {
		t.Fatalf("resume status %s: %s", got.ExitStatus, got.Submission)
	}
	if !reflect.DeepEqual(m2.seen[0][1], m.seen[0][1]) {
		t.Errorf("resumed task message = %+v", m2.seen[0][1])
	}
	// --image on an unfinished checkpoint has no new message to attach to.
	m3 := &scripted{}
	c := &Agent{Model: m3, ModelName: "fake", Tools: &tools.Executor{Root: a.Tools.Root}, Config: config.Default(), Resume: roundTrip(t, tr), TaskImages: taskImages()}
	if got := c.Run(context.Background(), ""); got.ExitStatus != StatusError || len(m3.seen) != 0 {
		t.Errorf("unfinished resume with images = %s %q, %d requests", got.ExitStatus, got.Submission, len(m3.seen))
	}
	// Labels must match the attachments.
	bad := roundTrip(t, tr)
	bad.Messages[1].Images = bad.Messages[1].Images[:1]
	if err := bad.Validate(); err == nil {
		t.Error("Validate accepted a task message whose labels don't match its images")
	}
}

func TestFollowUpTaskImages(t *testing.T) { // AC9
	a, tr := finishedRun(t, model.Message{Role: "assistant", Content: "done\n" + SubmitMarker})
	tr = roundTrip(t, tr)
	m := &scripted{replies: []model.Message{submit}}
	shot := []model.Image{model.NewImage("image/png", "after.png", testPNG)}
	b := &Agent{Model: m, ModelName: "fake", Tools: &tools.Executor{Root: a.Tools.Root}, Config: config.Default(), Resume: tr, FollowUp: "still wrong, see this", TaskImages: shot}
	got := b.Run(context.Background(), "")
	if got.ExitStatus != StatusSubmitted || got.Resume == nil || !got.Resume.FollowUp {
		t.Fatalf("follow-up = %s %q", got.ExitStatus, got.Submission)
	}
	sent := m.seen[0]
	fu := sent[len(sent)-1]
	if !strings.HasPrefix(fu.Content, trajectory.FollowUpNotice) || !strings.HasSuffix(fu.Content, "still wrong, see this"+model.AttachmentLabels(shot)) || !reflect.DeepEqual(fu.Images, shot) {
		t.Errorf("follow-up message = %q, %d images", fu.Content, len(fu.Images))
	}
	if err := roundTrip(t, got).Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestCompactKeepsTaskImages(t *testing.T) { // AC10
	f := &fakeModel{replies: []reply{
		{msg: reads(1, 1)}, {msg: reads(2, 1)}, {msg: reads(3, 1)},
		{msg: reads(4, 1)}, {msg: reads(5, 1)}, {msg: reads(6, 1), input: 19000},
		{msg: submit},
	}, summary: "sum"}
	a, _ := compactAgent(t, f, 20000)
	a.TaskImages = taskImages()
	tr := a.Run(context.Background(), "the task")
	if tr.ExitStatus != StatusSubmitted || len(tr.Compactions) == 0 || f.summaryRequests() == 0 {
		t.Fatalf("status %s, compactions %d, summaries %d", tr.ExitStatus, len(tr.Compactions), f.summaryRequests())
	}
	reqs := f.agentRequests()
	if task := reqs[len(reqs)-1].msgs[1]; !reflect.DeepEqual(task.Images, taskImages()) {
		t.Errorf("task images after compaction = %d", len(task.Images))
	}
	if got := msgChars(tr.Messages[1]); got != len(tr.Messages[1].Content)+2*model.ImageTokenEstimate*4 {
		t.Errorf("msgChars = %d", got)
	}
	for _, r := range f.reqs {
		if r.tools != nil {
			continue
		}
		for _, m := range r.msgs {
			if len(m.Images) > 0 || strings.Contains(m.Content, taskImages()[0].Data) {
				t.Error("summarizer request carries image data")
			}
		}
	}
}
