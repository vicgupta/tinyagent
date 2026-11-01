package agent

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/outschema"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

func validOptions() Options {
	return Options{Model: &scripted{}, ModelName: "fake", Tools: &tools.Executor{Root: "/nonexistent"}, Config: config.Default()}
}

func checkpoint(status string) *trajectory.Trajectory {
	return &trajectory.Trajectory{Model: "fake", ExitStatus: status, Steps: 3, Cost: 0.5, InputTokens: 100, OutputTokens: 20}
}

func validSchema() *outschema.Schema {
	return &outschema.Schema{Params: map[string]any{"type": "object"}, Digest: "sha256:x"}
}

func TestNewRejectsInvalidCombinations(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(o *Options)
		want string
	}{
		{"nil model", func(o *Options) { o.Model = nil }, "Model is required"},
		{"nil tools", func(o *Options) { o.Tools = nil }, "Tools is required"},
		{"subagents without delegate", func(o *Options) { o.Subagents = &Subagents{} }, "Subagents (set: true) and Tools.Delegate (false)"},
		{"delegate without subagents", func(o *Options) { o.Tools.Delegate = true }, "Subagents (set: false) and Tools.Delegate (true)"},
		{"workers without tool", func(o *Options) { o.Workers = &Workers{} }, "Workers (set: true) and Tools.Workers (false)"},
		{"worker tool without workers", func(o *Options) { o.Tools.Workers = true }, "Workers (set: false) and Tools.Workers (true)"},
		{"negative step limit", func(o *Options) { o.Config.StepLimit = -1 }, "step limit must be >= 0"},
		{"negative cost limit", func(o *Options) { o.Config.CostLimit = -0.5 }, "cost limit must be >= 0"},
		{"negative time limit", func(o *Options) { o.Config.WallTimeSec = -1 }, "time limit must be >= 0"},
		{"negative token limit", func(o *Options) { o.Config.TokenLimit = -1 }, "token limit must be >= 0"},
		{"continue step limit not offset", func(o *Options) {
			o.Resume, o.Continue, o.FollowUp, o.Config.StepLimit = checkpoint(StatusSubmitted), true, "more", 3
		}, "not offset by the 3 steps"},
		{"continue cost limit not offset", func(o *Options) {
			o.Resume, o.Continue, o.FollowUp, o.Config.CostLimit = checkpoint(StatusSubmitted), true, "more", 0.25
		}, "not offset by the $0.5"},
		{"continue token limit not offset", func(o *Options) {
			o.Resume, o.Continue, o.FollowUp, o.Config.TokenLimit = checkpoint(StatusSubmitted), true, "more", 120
		}, "not offset by the 120 tokens"},
		{"schema without params", func(o *Options) { o.OutputSchema = &outschema.Schema{Digest: "sha256:x"} }, "needs Params and Digest"},
		{"schema with resume", func(o *Options) { o.OutputSchema, o.Resume = validSchema(), checkpoint(StatusStepLimit) }, "cannot be resumed"},
		{"schema with ask_user", func(o *Options) { o.OutputSchema, o.Tools.AskUser = validSchema(), true }, "ask_user can't be combined"},
		{"schema with colliding external tool", func(o *Options) {
			o.OutputSchema = validSchema()
			o.Tools.ExternalSpecs = []model.ToolSpec{{Type: "function", Function: model.FunctionSpec{Name: SubmitToolName}}}
		}, "collides with the output schema's submit tool"},
		{"follow-up without resume", func(o *Options) { o.FollowUp = "more" }, "FollowUp needs Resume"},
		{"continue without resume", func(o *Options) { o.Continue = true }, "Continue needs Resume"},
		{"resume source without resume", func(o *Options) { o.ResumeSource = "x.json" }, "ResumeSource needs Resume"},
		{"unfinished with follow-up", func(o *Options) { o.Resume, o.FollowUp = checkpoint(StatusStepLimit), "more" }, "can only continue a finished checkpoint"},
		{"unfinished with images", func(o *Options) {
			o.Resume, o.TaskImages = checkpoint(StatusStepLimit), []model.Image{{}}
		}, "image attachments need a follow-up task"},
		{"submitted without follow-up", func(o *Options) { o.Resume = checkpoint(StatusSubmitted) }, "already finished"},
		{"completed without follow-up", func(o *Options) { o.Resume = checkpoint(StatusCompleted) }, "already finished"},
		{"needs input without answer", func(o *Options) { o.Resume = checkpoint(StatusNeedsInput) }, "waiting for an answer"},
		{"answer with images", func(o *Options) {
			o.Resume, o.FollowUp, o.TaskImages = checkpoint(StatusNeedsInput), "yes", []model.Image{{}}
		}, "can't be added to an answer"},
		{"continue turn without follow-up", func(o *Options) { o.Resume, o.Continue = checkpoint(StatusStepLimit), true }, "already finished"},
		{"MCP checkpoint without external tools", func(o *Options) {
			o.Resume = checkpoint(StatusStepLimit)
			o.Resume.MCPEnabled = true
		}, "MCP-enabled runs cannot be resumed"},
		{"plugin-tool checkpoint without external tools", func(o *Options) {
			o.Resume = checkpoint(StatusStepLimit)
			o.Resume.ExternalTools = []string{"wasm__tool"}
		}, "MCP-enabled runs cannot be resumed"},
		{"MCP checkpoint even with external tools offered", func(o *Options) {
			o.Resume = checkpoint(StatusStepLimit)
			o.Resume.MCPEnabled = true
			o.Tools.ExternalSpecs = []model.ToolSpec{{Type: "function", Function: model.FunctionSpec{Name: "srv__tool"}}}
		}, "MCP-enabled runs cannot be resumed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			o := validOptions()
			c.edit(&o)
			a, err := New(o)
			if err == nil || a != nil {
				t.Fatalf("New = %v, %v; want an error", a, err)
			}
			if !strings.HasPrefix(err.Error(), "agent: ") || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q, want it to contain %q", err, c.want)
			}
		})
	}
}

func TestNewAcceptsValidCombinations(t *testing.T) {
	ext := []model.ToolSpec{{Type: "function", Function: model.FunctionSpec{Name: "srv__tool"}}}
	for _, c := range []struct {
		name string
		edit func(o *Options)
	}{
		{"fresh", func(o *Options) {}},
		{"fresh with limits", func(o *Options) {
			o.Config.StepLimit, o.Config.CostLimit, o.Config.TokenLimit, o.Config.WallTimeSec = 5, 1, 1000, 60
		}},
		{"subagents and workers", func(o *Options) {
			o.Subagents, o.Workers = &Subagents{}, &Workers{}
			o.Tools.Delegate, o.Tools.Workers = true, true
		}},
		{"output schema", func(o *Options) { o.OutputSchema = validSchema() }},
		{"unfinished checkpoint", func(o *Options) { o.Resume, o.ResumeSource = checkpoint(StatusStepLimit), "x.json" }},
		// A loaded checkpoint's limits are absolute: a run already at its
		// step limit stops at once with StepLimitExceeded.
		{"unfinished checkpoint at its step limit", func(o *Options) { o.Resume, o.Config.StepLimit = checkpoint(StatusStepLimit), 3 }},
		{"finished with follow-up", func(o *Options) { o.Resume, o.FollowUp = checkpoint(StatusSubmitted), "more" }},
		{"finished with follow-up images", func(o *Options) {
			o.Resume, o.FollowUp, o.TaskImages = checkpoint(StatusCompleted), "more", []model.Image{{}}
		}},
		{"answer", func(o *Options) { o.Resume, o.FollowUp = checkpoint(StatusNeedsInput), "yes" }},
		{"continue turn with offset limits", func(o *Options) {
			o.Resume, o.Continue, o.FollowUp = checkpoint(StatusError), true, "more"
			o.Config.StepLimit, o.Config.CostLimit, o.Config.TokenLimit = 4, 0.75, 121
		}},
		{"continue turn after MCP", func(o *Options) {
			o.Resume, o.Continue, o.FollowUp = checkpoint(StatusSubmitted), true, "more"
			o.Resume.MCPEnabled = true
		}},
		{"continue turn with external tools", func(o *Options) {
			o.Resume, o.Continue, o.FollowUp = checkpoint(StatusSubmitted), true, "more"
			o.Resume.ExternalTools, o.Tools.ExternalSpecs = []string{"srv__tool"}, ext
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			o := validOptions()
			c.edit(&o)
			a, err := New(o)
			if err != nil || a == nil {
				t.Fatalf("New = %v, %v", a, err)
			}
		})
	}
}

// TestNewCopiesEveryOption fails when an Options field is added without a
// matching Agent field or without New copying it.
func TestNewCopiesEveryOption(t *testing.T) {
	o := validOptions()
	v := reflect.ValueOf(&o).Elem()
	// Fields whose zero value New rejects or that must agree are set above;
	// fill every other field with a non-zero value of its type.
	skip := map[string]bool{"Model": true, "Tools": true, "Subagents": true, "Workers": true,
		"OutputSchema": true, "Resume": true, "FollowUp": true, "Continue": true, "ResumeSource": true, "TaskImages": true, "Config": true}
	for i := 0; i < v.NumField(); i++ {
		f, name := v.Field(i), v.Type().Field(i).Name
		if skip[name] {
			continue
		}
		switch f.Kind() {
		case reflect.Bool:
			f.SetBool(true)
		case reflect.String:
			f.SetString("x")
		case reflect.Pointer:
			f.Set(reflect.New(f.Type().Elem()))
		case reflect.Slice:
			f.Set(reflect.MakeSlice(f.Type(), 1, 1))
		case reflect.Map:
			f.Set(reflect.MakeMap(f.Type()))
		case reflect.Interface:
			// Out, Err (io.Writer) and Snapshots; any non-nil implementation.
			switch name {
			case "Snapshots":
				f.Set(reflect.ValueOf(fakeSnapshotter{}))
			default:
				f.Set(reflect.ValueOf(os.Stderr))
			}
		default:
			t.Fatalf("field %s: unhandled kind %s; extend this test", name, f.Kind())
		}
	}
	o.Subagents, o.Workers = &Subagents{}, &Workers{}
	o.Tools.Delegate, o.Tools.Workers = true, true
	o.Resume, o.FollowUp, o.Continue, o.ResumeSource = checkpoint(StatusSubmitted), "more", true, "src"
	o.TaskImages = []model.Image{{}}
	o.Config.StepLimit = 99
	a, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	av := reflect.ValueOf(a).Elem()
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		af := av.FieldByName(name)
		if !af.IsValid() {
			t.Errorf("Agent has no field %s", name)
			continue
		}
		if af.Type() != v.Field(i).Type() {
			t.Errorf("field %s: Agent type %s, Options type %s", name, af.Type(), v.Field(i).Type())
			continue
		}
		if !reflect.DeepEqual(af.Interface(), v.Field(i).Interface()) {
			t.Errorf("New did not copy %s", name)
		}
	}
}

type fakeSnapshotter struct{}

func (fakeSnapshotter) Take(string, int) (trajectory.Snapshot, error) {
	return trajectory.Snapshot{}, nil
}

// TestNewRunsLikeALiteral checks that a constructed Agent runs exactly as the
// struct literal did.
func TestNewRunsLikeALiteral(t *testing.T) {
	run := func(build func(m model.Model, root string) *Agent) *trajectory.Trajectory {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		m := &scripted{replies: []model.Message{
			{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "write_file", `{"path":"f.txt","content":"hi"}`)}},
			{Role: "assistant", Content: "Done.\n" + SubmitMarker},
		}}
		tr := build(m, root).Run(context.Background(), "write hi")
		if data, err := os.ReadFile(filepath.Join(root, "f.txt")); err != nil || string(data) != "hi" {
			t.Fatalf("f.txt = %q, %v", data, err)
		}
		return tr
	}
	lit := run(func(m model.Model, root string) *Agent {
		return &Agent{Model: m, ModelName: "fake", Tools: &tools.Executor{Root: root}, Config: config.Default()}
	})
	built := run(func(m model.Model, root string) *Agent {
		a, err := New(Options{Model: m, ModelName: "fake", Tools: &tools.Executor{Root: root}, Config: config.Default()})
		if err != nil {
			t.Fatal(err)
		}
		return a
	})
	if lit.ExitStatus != StatusSubmitted || built.ExitStatus != lit.ExitStatus || built.Steps != lit.Steps ||
		built.Submission != lit.Submission || len(built.Messages) != len(lit.Messages) {
		t.Fatalf("literal %s/%d/%q, New %s/%d/%q", lit.ExitStatus, lit.Steps, lit.Submission, built.ExitStatus, built.Steps, built.Submission)
	}
}
