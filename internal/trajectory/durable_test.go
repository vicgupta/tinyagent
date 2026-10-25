package trajectory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSchemaV2Validation(t *testing.T) {
	pending := func(c *Trajectory) {
		c.SchemaVersion = 2
		c.Messages = c.Messages[:6] // last turn: both calls without results
		c.PendingCalls = []PendingCall{{ID: "a", Name: "list_files"}, {ID: "b", Name: "read_file"}}
	}
	cases := []struct {
		name string
		edit func(*Trajectory)
		want string
	}{
		{"v1 still valid", func(*Trajectory) {}, ""},
		{"v2 valid", func(c *Trajectory) { c.SchemaVersion = 2 }, ""},
		{"pending before tools", pending, ""},
		{"pending mid-turn", func(c *Trajectory) {
			pending(c)
			c.Messages = append(c.Messages, c.Messages[3])
			c.PendingCalls = c.PendingCalls[1:]
		}, ""},
		{"pending mismatch", func(c *Trajectory) { pending(c); c.PendingCalls = c.PendingCalls[1:] }, "result"},
		{"pending wrong name", func(c *Trajectory) { pending(c); c.PendingCalls[0].Name = "bash" }, "result"},
		{"stale pending", func(c *Trajectory) {
			c.SchemaVersion = 2
			c.PendingCalls = []PendingCall{{ID: "a", Name: "list_files"}}
		}, "pending"},
		{"v1 with pending", func(c *Trajectory) { pending(c); c.SchemaVersion = 1 }, "version 2 fields"},
		{"plan while disabled", func(c *Trajectory) { c.SchemaVersion = 2; c.Plan = &Plan{Version: 1} }, "not enabled"},
		{"invalid plan", func(c *Trajectory) {
			c.SchemaVersion, c.PlanEnabled = 2, true
			c.Plan = &Plan{Version: 1, Items: []PlanItem{{ID: "a", Text: "x", Status: "nope"}}}
		}, "invalid plan"},
		{"valid plan", func(c *Trajectory) {
			c.SchemaVersion, c.PlanEnabled = 2, true
			c.Plan = &Plan{Version: 2, UpdatedStep: 1, Items: []PlanItem{{ID: "a", Text: "x", Status: "in_progress"}}}
		}, ""},
		{"compaction ok", func(c *Trajectory) {
			c.Compactions = []Compaction{{Step: 1, Stage: "summarize", Summary: "s", Summarized: []int{2, 3, 4}, View: []int{0, 1, -1, 5, 6, 7}}}
		}, ""},
		{"compaction elide ok", func(c *Trajectory) {
			c.Compactions = []Compaction{{Step: 1, Stage: "elide", Elided: []int{3}, View: []int{0, 1, 2, 3, 4, 5, 6, 7}}}
		}, ""},
		{"compaction out of range", func(c *Trajectory) { c.Compactions = []Compaction{{Step: 1, View: []int{0, 1, 99}}} }, "compaction"},
		{"summary without text", func(c *Trajectory) { c.Compactions = []Compaction{{Step: 1, View: []int{0, 1, -1, 5}}} }, "compaction"},
		{"elided non-tool", func(c *Trajectory) { c.Compactions = []Compaction{{Step: 1, Elided: []int{2}, View: []int{0, 1, 2}}} }, "compaction"},
		{"unordered view", func(c *Trajectory) { c.Compactions = []Compaction{{Step: 1, View: []int{0, 1, 5, 3}}} }, "compaction"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := checkpoint()
			tc.edit(c)
			err := c.Validate()
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestLoadV1AndRejectPartialFiles(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "v1.json")
	c := checkpoint()
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	if got, err := Load(p); err != nil || got.SchemaVersion != 1 {
		t.Fatalf("v1 load: %v", err)
	}
	data, _ := os.ReadFile(p)
	partial := filepath.Join(dir, "partial.json")
	if err := os.WriteFile(partial, data[:len(data)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(partial); err == nil || !strings.Contains(err.Error(), "invalid checkpoint JSON") {
		t.Fatalf("partial: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tinyagent-") {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
}
