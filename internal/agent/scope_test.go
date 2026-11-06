package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/tools"
)

func TestDelegateToolsParamScopesChild(t *testing.T) {
	root := t.TempDir()
	parent := &tools.Executor{Root: root, Delegate: true}
	childSel := parent.Child(tools.ChildSpec{ReadOnly: true}).Selection()
	only, err := scopedChildTools([]string{"read_file"}, childSel)
	if err != nil || strings.Join(only, ",") != "read_file" {
		t.Fatalf("%v %v", only, err)
	}
	if _, err := scopedChildTools([]string{"bash"}, childSel); err == nil || !strings.Contains(err.Error(), "bash") {
		t.Fatalf("bash on read-only delegate: %v", err)
	}
	if only, err := scopedChildTools(nil, childSel); only != nil || err != nil {
		t.Fatalf("omit tools: %v %v", only, err)
	}
}

func TestWorkerToolsParamIntersects(t *testing.T) {
	got, err := intersectScopedTools([]string{"read_file", "bash"}, []string{"read_file", "grep", "bash"})
	if err != nil || strings.Join(got, ",") != "read_file,bash" {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := intersectScopedTools([]string{"write_file"}, []string{"read_file"}); err == nil || !strings.Contains(err.Error(), "write_file") {
		t.Fatalf("disallowed: %v", err)
	}
}

func TestScopedRunOffersOnlyListedTools(t *testing.T) {
	e := &tools.Executor{Root: t.TempDir(), Plan: true, Delegate: true, WebFetch: &tools.WebFetcher{}, WebSearch: &tools.WebSearcher{}, Only: []string{"read_file", "grep"}}
	names := toolNames(e.Catalog())
	if strings.Join(names, ",") != "read_file,grep" {
		t.Fatalf("catalog = %v", names)
	}
	// Agent-handled tools outside Only are unknown to FormatError.
	if reason := e.FormatError(model.ToolCall{ID: "1", Function: model.FunctionCall{Name: "update_plan", Arguments: `{"items":[]}`}}); !strings.Contains(reason, "unknown tool") {
		t.Fatalf("update_plan format: %q", reason)
	}
	if reason := e.FormatError(model.ToolCall{ID: "1", Function: model.FunctionCall{Name: "delegate", Arguments: `{"task":"x"}`}}); !strings.Contains(reason, "unknown tool") {
		t.Fatalf("delegate format: %q", reason)
	}
	res := e.RunResult(context.Background(), model.ToolCall{ID: "1", Function: model.FunctionCall{Name: "bash", Arguments: `{"command":"true"}`}})
	if !res.IsError || !strings.Contains(res.Text, "not available in this run") {
		t.Fatalf("bash: %+v", res)
	}
}

func TestDefaultOffersWebTools(t *testing.T) {
	e := &tools.Executor{Root: t.TempDir(), WebFetch: &tools.WebFetcher{}, WebSearch: &tools.WebSearcher{}}
	names := toolNames(e.Catalog())
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "web_fetch") || !strings.Contains(joined, "web_search") {
		t.Fatalf("default catalog missing web tools: %v", names)
	}
	if e.Only != nil {
		t.Fatal("Only should be nil by default")
	}
}

func TestDelegateArgsToolsJSON(t *testing.T) {
	raw := `{"task":"find X","tools":["read_file","grep"]}`
	var args struct {
		Task  string   `json:"task"`
		Tools []string `json:"tools"`
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		t.Fatal(err)
	}
	if len(args.Tools) != 2 {
		t.Fatalf("%v", args.Tools)
	}
}
