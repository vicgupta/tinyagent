package trajectory

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/model"
)

func checkpoint() *Trajectory {
	calls := []model.ToolCall{
		{ID: "a", Type: "function", Function: model.FunctionCall{Name: "list_files", Arguments: `{}`}},
		{ID: "b", Type: "function", Function: model.FunctionCall{Name: "read_file", Arguments: `{"path":"x"}`}},
	}
	return &Trajectory{
		Task: "task", Model: "openai/gpt-4o", Steps: 2, Cost: 0.5, InputTokens: 100,
		SchemaVersion: 1, Root: func() string { root, _ := filepath.Abs("project"); return root }(), SystemPrompt: "instructions", ToolSignature: "hash", Tools: []string{"list_files", "read_file"},
		ExitStatus: "StepLimitExceeded",
		Messages: []model.Message{
			{Role: "system", Content: "instructions"}, {Role: "user", Content: "task"},
			{Role: "assistant", ToolCalls: calls},
			{Role: "tool", ToolCallID: "a", Content: "first"},
			{Role: "tool", ToolCallID: "b", Content: "second"},
			{Role: "assistant", ToolCalls: calls}, // IDs may repeat in another turn.
			{Role: "tool", ToolCallID: "a", Content: "third"},
			{Role: "tool", ToolCallID: "b", Content: "fourth"},
		},
	}
}

func TestCheckpointValidationContract(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Trajectory)
		want string
	}{
		{"valid repeated IDs", func(*Trajectory) {}, ""},
		{"legacy", func(c *Trajectory) { c.SchemaVersion = 0 }, "metadata"},
		{"version", func(c *Trajectory) { c.SchemaVersion = 99 }, "version"},
		{"mode", func(c *Trajectory) { c.SystemPrompt = "" }, "metadata"},
		{"completed", func(c *Trajectory) { c.ExitStatus = "Completed" }, "finished"},
		{"submitted", func(c *Trajectory) { c.ExitStatus = "Submitted" }, "finished"},
		{"status", func(c *Trajectory) { c.ExitStatus = "unknown" }, "status"},
		{"negative", func(c *Trajectory) { c.InputTokens = -1 }, "accounting"},
		{"nan", func(c *Trajectory) { c.Cost = math.NaN() }, "accounting"},
		{"infinity", func(c *Trajectory) { c.Cost = math.Inf(1) }, "accounting"},
		{"cache", func(c *Trajectory) { c.CacheReadTokens = 101 }, "accounting"},
		{"steps", func(c *Trajectory) { c.Steps++ }, "steps"},
		{"system", func(c *Trajectory) { c.Messages[0].Role = "user" }, "system"},
		{"task", func(c *Trajectory) { c.Messages[1].Content = "different" }, "task"},
		{"role", func(c *Trajectory) { c.Messages[2].Role = "developer" }, "history"},
		{"missing", func(c *Trajectory) { c.Messages = c.Messages[:7] }, "result"},
		{"duplicate result", func(c *Trajectory) { c.Messages[4].ToolCallID = "a" }, "result"},
		{"out of order", func(c *Trajectory) { c.Messages[3], c.Messages[4] = c.Messages[4], c.Messages[3] }, "result"},
		{"orphan", func(c *Trajectory) { c.Messages = append(c.Messages, c.Messages[7]) }, "history"},
		{"duplicate call", func(c *Trajectory) { c.Messages[2].ToolCalls[1].ID = "a" }, "call"},
		{"arguments", func(c *Trajectory) { c.Messages[2].ToolCalls[0].Function.Arguments = "bad" }, "call"},
		{"unknown call", func(c *Trajectory) { c.Messages[2].ToolCalls[0].Function.Name = "other" }, "call"},
		{"empty call ID", func(c *Trajectory) { c.Messages[2].ToolCalls[0].ID = "" }, "call"},
		{"call type", func(c *Trajectory) { c.Messages[2].ToolCalls[0].Type = "other" }, "call"},
		{"array args", func(c *Trajectory) { c.Messages[2].ToolCalls[0].Function.Arguments = "[]" }, "call"},
		{"null args", func(c *Trajectory) { c.Messages[2].ToolCalls[0].Function.Arguments = "null" }, "call"},
		{"system mismatch", func(c *Trajectory) { c.Messages[0].Content = "changed" }, "system"},
		{"tool with calls", func(c *Trajectory) { c.Messages[3].ToolCalls = c.Messages[2].ToolCalls }, "result"},
		{"terminal assistant", func(c *Trajectory) { c.Messages[5].ToolCalls = nil; c.Messages = c.Messages[:6] }, "history"},
		{"submit marker", func(c *Trajectory) { c.Messages[2].Content = "COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT" }, "history"},
		{"extra instruction", func(c *Trajectory) {
			c.Messages = append(c.Messages, model.Message{Role: "user", Content: ContinuationNotice})
		}, "history"},
		{"bad compaction", func(c *Trajectory) { c.Compactions = []Compaction{{Step: 1}} }, "compaction"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := checkpoint()
			tc.edit(c)
			err := c.Validate()
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestCheckpointLoadRefusals(t *testing.T) {
	p := filepath.Join(t.TempDir(), "run.json")
	for _, data := range []string{"", "null", "{secret transcript", `{}`, `{"resume":{"version":999}}`, `{"model_view":[]}`} {
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p); err == nil || strings.Contains(err.Error(), "secret transcript") {
			t.Fatalf("invalid input: %v", err)
		}
	}
	if _, err := Load(p + ".missing"); err == nil {
		t.Fatal("missing accepted")
	}
	c := checkpoint()
	if runtime.GOOS == "windows" {
		c.Root = `C:\project`
	}
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err != nil {
		t.Fatal(err)
	}
}

func TestCheckpointPrivateAtomicSave(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "run.json")
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The former predictable temporary path must never be followed.
	_ = os.Symlink(victim, p+".tmp")
	c := checkpoint()
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	c.Cost = math.NaN()
	if err := c.Save(p); err == nil {
		t.Fatal("invalid JSON saved")
	}
	b, err := os.ReadFile(p)
	if err != nil || !json.Valid(b) {
		t.Fatal("last valid checkpoint lost")
	}
	b, _ = os.ReadFile(victim)
	if string(b) != "untouched" {
		t.Fatal("followed temporary symlink")
	}
}

func TestCheckpointExclusivePublication(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "checkpoint.json")
	c := checkpoint()
	if err := c.SaveNew(path); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if err := c.SaveNew(path); err == nil {
		t.Fatal("overwrote an existing destination")
	}
	alias := filepath.Join(dir, "alias.json")
	if err := os.Symlink(path, alias); err == nil {
		if err := c.SaveNew(alias); err == nil {
			t.Fatal("overwrote a symlink destination")
		}
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("exclusive publication changed the prior checkpoint")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tinyagent-") {
			t.Fatal("temporary file leaked")
		}
	}
}

func TestCheckpointSizeBoundAndUnknownState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.json")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxCheckpointBytes + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "64 MiB") {
		t.Fatalf("size bound: %v", err)
	}
	c := checkpoint()
	data, _ := json.Marshal(c)
	data = append(data[:len(data)-1], []byte(`,"model_view":[]}`)...)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "unsupported checkpoint fields") {
		t.Fatalf("unknown state: %v", err)
	}
}

func TestCheckpointContinuationProvenance(t *testing.T) {
	c := checkpoint()
	c.Resume = &ResumeInfo{Source: "first.json", ExitStatus: "Error", Steps: 2, Message: len(c.Messages)}
	c.Messages = append(c.Messages, model.Message{Role: "user", Content: ContinuationNotice})
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Resume = &ResumeInfo{Source: "second.json", ExitStatus: "Interrupted", Steps: 2, Message: len(c.Messages), Previous: c.Resume}
	c.Messages = append(c.Messages, model.Message{Role: "user", Content: ContinuationNotice})
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Resume.Previous.Message = 3
	if err := c.Validate(); err == nil {
		t.Fatal("accepted provenance inside a tool turn")
	}
}
