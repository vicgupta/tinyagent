package tools

import (
	"reflect"
	"testing"

	"github.com/vicgupta/tinyagent/internal/model"
)

// Pinned signatures: checkpoints written by earlier builds carry these, so
// a change here makes them unresumable and needs a deliberate version bump.
func TestSelectionSignaturePinned(t *testing.T) {
	for _, tc := range []struct {
		sel  Selection
		want string
	}{
		{Selection{}, "tools-v1:a0b5bfbc3c6e43214e4a14257ca5ed6b813d44d5d840800f0936acfc78d17b51"},
		{Selection{ReadOnly: true}, "tools-read-only-v1:f6704d29b0179e388506ce2908431f7ff96d9dd388ef7ef59ce95944fbc0913b"},
		{Selection{ReadOnly: true, Plan: true, Images: true, ApplyPatch: true, Delegate: true, WebFetch: true, LSP: true, AskUser: true},
			"ask-user-v1:lsp-v1:web-fetch-v1:subagents-v1:apply-patch-v1:images-v1:plan-v1:tools-read-only-v1:8c03ab6427062b0a47d8ecc80c8c47cb0bf6eb56c93162fce28b367bfcf90d89"},
	} {
		if got := tc.sel.Signature(); got != tc.want {
			t.Errorf("%+v: signature = %s, want %s", tc.sel, got, tc.want)
		}
	}
}

// builtinFlags lists every opt-in flag the fingerprint covers.
var builtinFlags = []func(*Selection){
	func(s *Selection) { s.ReadOnly = true },
	func(s *Selection) { s.Plan = true },
	func(s *Selection) { s.Images = true },
	func(s *Selection) { s.ApplyPatch = true },
	func(s *Selection) { s.Delegate = true },
	func(s *Selection) { s.WebFetch = true },
	func(s *Selection) { s.LSP = true },
	func(s *Selection) { s.AskUser = true },
}

func TestSelectionSpecsAndSignatureAgree(t *testing.T) {
	seen := map[string][]string{}
	for m := 0; m < 1<<len(builtinFlags); m++ {
		var sel Selection
		for i, set := range builtinFlags {
			if m&(1<<i) != 0 {
				set(&sel)
			}
		}
		// Same selection, same tools and same fingerprint.
		again := sel
		if !reflect.DeepEqual(sel.Specs(), again.Specs()) || sel.Signature() != again.Signature() {
			t.Fatalf("selection %+v is not deterministic", sel)
		}
		sig := sel.Signature()
		if prev, ok := seen[sig]; ok {
			t.Fatalf("selections offering %v and %v share signature %s", prev, sel.Names(), sig)
		}
		seen[sig] = sel.Names()
		for _, name := range sel.Names() {
			if !sel.Offers(name) {
				t.Errorf("Offers(%q) = false for a listed tool", name)
			}
		}
	}
}

func TestSelectionChangeChangesSignature(t *testing.T) {
	base := Selection{}
	for i, set := range builtinFlags {
		sel := base
		set(&sel)
		if sel.Signature() == base.Signature() {
			t.Errorf("flag %d does not change the signature", i)
		}
		if reflect.DeepEqual(sel.Names(), base.Names()) {
			t.Errorf("flag %d does not change the offered tools", i)
		}
	}
}

func TestSelectionFingerprintIgnoresCheckedSeparately(t *testing.T) {
	base := Selection{Delegate: true}
	extra := base
	extra.Workers = true
	extra.TaskTypes = []string{"quick"}
	extra.Remember, extra.MemoryTools = true, true
	extra.External = []model.ToolSpec{spec("mcp__x__y", "external", props{})}
	if extra.Signature() != base.Signature() {
		t.Error("workers, task types, memory or external tools changed the signature")
	}
	for _, name := range []string{WorkerToolName, ApplyWorkerToolName, RememberToolName, MemorySearchToolName, MemoryListToolName, "mcp__x__y"} {
		if !extra.Offers(name) {
			t.Errorf("%s not offered", name)
		}
	}
}

func TestSelectionReadOnlyDropsWriters(t *testing.T) {
	sel := Selection{ReadOnly: true, ApplyPatch: true, Workers: true, Remember: true, MemoryTools: true,
		External: []model.ToolSpec{spec("mcp__x__y", "external", props{})}}
	want := []string{"read_file", "list_files", "grep"}
	if got := sel.Names(); !reflect.DeepEqual(got, want) {
		t.Errorf("read-only names = %v, want %v", got, want)
	}
}

func TestExecutorCatalogIsSelection(t *testing.T) {
	e := &Executor{Plan: true, Delegate: true, Workers: true, AskUser: true, TaskTypes: []string{"quick"},
		ExternalSpecs: []model.ToolSpec{spec("mcp__x__y", "external", props{})}}
	if !reflect.DeepEqual(e.Catalog(), e.Selection().Specs()) {
		t.Error("Executor.Catalog differs from its Selection")
	}
	for _, s := range e.Catalog() {
		if !e.Permits(s.Function.Name) || !e.Selection().Offers(s.Function.Name) {
			t.Errorf("%s offered but not permitted", s.Function.Name)
		}
	}
}
