package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/tools"
)

// A read-only run with deny_read [".env"]: the model cannot read, grep or
// list the secret, and the trajectory records the rules.
func TestFileAccessEndToEnd(t *testing.T) {
	const secret = "sk-file-access-sentinel"
	var root string
	tr, _ := runSteps(t, func(a *Agent) {
		root = a.Tools.Root
		os.WriteFile(filepath.Join(root, ".env"), []byte("TOKEN="+secret+"\n"), 0o644)
		os.WriteFile(filepath.Join(root, "main.go"), []byte("package main // TOKEN\n"), 0o644)
		fa, err := tools.NewFileAccess(root, []string{".env"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		a.Tools.ReadOnly = true
		a.Tools.FileAccess = fa
	},
		toolStep(call("1", "read_file", `{"path":".env"}`)),
		toolStep(call("2", "grep", `{"pattern":"TOKEN"}`)),
		toolStep(call("3", "list_files", `{}`)),
		toolStep(call("4", "read_file", `{"path":"main.go"}`)),
	)
	res := toolResults(tr.Messages)
	if len(res) != 4 {
		t.Fatalf("results = %q", res)
	}
	if !strings.Contains(res[0], `.env is denied by file_access.deny_read rule ".env"`) {
		t.Errorf("read_file .env = %q", res[0])
	}
	if !strings.Contains(res[1], "main.go") || !strings.Contains(res[1], "(1 path hidden by file_access rules)") {
		t.Errorf("grep = %q", res[1])
	}
	if strings.Contains(res[2], ".env") || !strings.Contains(res[2], "(1 path hidden") {
		t.Errorf("list_files = %q", res[2])
	}
	if !strings.Contains(res[3], "package main") {
		t.Errorf("read_file main.go = %q", res[3])
	}
	for _, m := range tr.Messages {
		if strings.Contains(m.Content, secret) {
			t.Errorf("secret reached the model: %q", m.Content)
		}
	}
	if fa := tr.FileAccess; fa == nil || len(fa.DenyRead) != 1 || fa.DenyRead[0] != ".env" || len(fa.DenyWrite) != 0 {
		t.Errorf("trajectory file_access = %+v", tr.FileAccess)
	}
}

func TestFileAccessOffLeavesTrajectoryUnchanged(t *testing.T) {
	tr, _ := runSteps(t, nil)
	if tr.FileAccess != nil {
		t.Errorf("file_access = %+v", tr.FileAccess)
	}
}
