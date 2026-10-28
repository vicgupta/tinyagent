package agent

import (
	"testing"

	"github.com/vicgupta/tinyagent/internal/trajectory"
)

func TestResumeMemoryLoadedFields(t *testing.T) {
	if f := ResumeMemoryLoadedFields(&trajectory.Trajectory{}, "/s.json"); f != nil {
		t.Errorf("memory off: %v", f)
	}
	tr := &trajectory.Trajectory{MemoryEnabled: true, MemoryOmitted: 2, Memory: []trajectory.MemoryLine{
		{ID: "b", Date: "2026-10-21", Text: "secret b"}, {ID: "a", Date: "2026-10-20", Text: "secret a"},
	}}
	f := ResumeMemoryLoadedFields(tr, "/s.json")
	ids, _ := f["ids"].([]string)
	if f["path"] != "/s.json" || f["loaded"] != 2 || f["omitted"] != 2 || f["from_checkpoint"] != true || len(ids) != 2 || ids[0] != "b" || ids[1] != "a" {
		t.Errorf("fields = %v", f)
	}
	for _, v := range f {
		if s, ok := v.(string); ok && (s == "secret a" || s == "secret b") {
			t.Error("entry text leaked")
		}
	}
	empty := ResumeMemoryLoadedFields(&trajectory.Trajectory{MemoryEnabled: true}, "")
	if empty["loaded"] != 0 || empty["path"] != "" {
		t.Errorf("empty snapshot = %v", empty)
	}
}
