package agent

import (
	"testing"

	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// TestConfiguredProviderContextWindow covers AC8: a configured model's
// context_window drives compaction at 80%, and a top-level window wins.
func TestConfiguredProviderContextWindow(t *testing.T) {
	model.SetProviders([]model.ProviderDef{{ID: "gw", API: "chat", BaseURL: "http://x", Models: map[string]model.ModelMeta{"m1": {ContextWindow: 2000}}}})
	t.Cleanup(func() { model.SetProviders(nil) })
	a := newAgent(t, &fakeModel{})
	a.ModelName = "gw/m1"
	if w := a.contextWindow(); w != 2000 {
		t.Fatalf("configured window = %d", w)
	}
	tr := &trajectory.Trajectory{}
	// bigContent is ~2,000 tokens: over 80% of 2,000.
	v := newView([]model.Message{{Role: "user", Content: bigContent}})
	if !a.needsCompaction(tr, v) {
		t.Error("no compaction above 80% of the configured window")
	}
	a.Config.ContextWindow = 100_000
	if w := a.contextWindow(); w != 100_000 || a.needsCompaction(tr, v) {
		t.Errorf("top-level window %d should win", w)
	}
	// 2,000 tokens is under 80% of 3,000 but over 80% of 2,400.
	a.Config.ContextWindow = 0
	model.SetProviders([]model.ProviderDef{{ID: "gw", API: "chat", BaseURL: "http://x", Models: map[string]model.ModelMeta{"m1": {ContextWindow: 3000}}}})
	if a.needsCompaction(tr, v) {
		t.Error("compacted below 80% of the configured window")
	}
}
