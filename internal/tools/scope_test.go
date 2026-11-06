package tools

import (
	"strings"
	"testing"
)

func TestNormalizeScopedTools(t *testing.T) {
	got, err := NormalizeScopedTools([]string{"read_file,grep", "bash"})
	if err != nil || strings.Join(got, ",") != "read_file,grep,bash" {
		t.Fatalf("got %v err %v", got, err)
	}
	if got, err := NormalizeScopedTools(nil); got != nil || err != nil {
		t.Fatalf("empty = %v, %v", got, err)
	}
	if got, err := NormalizeScopedTools([]string{"*"}); got != nil || err != nil {
		t.Fatalf("* = %v, %v", got, err)
	}
	for _, tc := range []struct {
		in   []string
		want string
	}{
		{[]string{""}, "empty tool name"},
		{[]string{"read_file,"}, "empty tool name"},
		{[]string{"a", "a"}, `duplicate tool "a"`},
		{[]string{"read_file,*"}, "clears tool scoping"},
	} {
		_, err := NormalizeScopedTools(tc.in)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want %q", tc.in, err, tc.want)
		}
	}
}

func TestValidateScopedTools(t *testing.T) {
	sel := Selection{WebFetch: true, WebSearch: true}
	if err := ValidateScopedTools([]string{"read_file", "web_search"}, sel); err != nil {
		t.Fatal(err)
	}
	if err := ValidateScopedTools([]string{"write_file"}, Selection{ReadOnly: true}); err == nil || !strings.Contains(err.Error(), "write_file") {
		t.Fatalf("read-only conflict: %v", err)
	}
	if err := ValidateScopedTools([]string{"nope"}, sel); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("unknown: %v", err)
	}
}

func TestWithToolsParamKeepsFingerprint(t *testing.T) {
	base := Selection{Delegate: true, Workers: true}
	offered := base.Specs()
	var hasTools bool
	for _, sp := range offered {
		if sp.Function.Name == DelegateToolName || sp.Function.Name == WorkerToolName {
			props := sp.Function.Parameters["properties"].(map[string]any)
			if _, ok := props["tools"]; !ok {
				t.Errorf("%s missing tools param", sp.Function.Name)
			} else {
				hasTools = true
			}
		}
	}
	if !hasTools {
		t.Fatal("no tools param on delegate/worker")
	}
	// Same as main before this feature: tools param is stripped from the fingerprint.
	want := "subagents-v1:tools-v1:cc05986dfc222b83e896404417012229bf0af71c7176482f35f82c59114c17a1"
	if got := (Selection{Delegate: true}).Signature(); got != want {
		t.Fatalf("delegate signature = %s, want %s (tools param must not change fingerprint)", got, want)
	}
	pinned := "ask-user-v1:lsp-v1:web-fetch-v1:subagents-v1:apply-patch-v1:images-v1:plan-v1:tools-read-only-v1:8c03ab6427062b0a47d8ecc80c8c47cb0bf6eb56c93162fce28b367bfcf90d89"
	combo := Selection{ReadOnly: true, Plan: true, Images: true, ApplyPatch: true, Delegate: true, WebFetch: true, LSP: true, AskUser: true}
	if got := combo.Signature(); got != pinned {
		t.Fatalf("combo signature = %s, want pinned %s", got, pinned)
	}
}

func TestEqualScopedTools(t *testing.T) {
	if !EqualScopedTools(nil, nil) || !EqualScopedTools([]string{"a"}, []string{"a"}) {
		t.Fatal("equal")
	}
	if equalScopedTools([]string{"a"}, []string{"b"}) || equalScopedTools([]string{"a"}, nil) {
		t.Fatal("unequal")
	}
}

// equalScopedTools is tested via EqualScopedTools.
func equalScopedTools(a, b []string) bool { return EqualScopedTools(a, b) }
