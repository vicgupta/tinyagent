package main

import (
	"os"
	"strings"
	"testing"
)

// workflowHelpFlags are the flags that belong to skill workflows. Each must
// be named in "tinyagent help workflows", in docs/workflows.md, and have a
// --help --verbose description that mentions workflows.
var workflowHelpFlags = []string{"workflow", "workflow-name", "workflow-observe", "skills-validate"}

// TestWorkflowHelpCoversEveryFlag keeps the workflows help topic and
// docs/workflows.md in sync with the workflow flags and events.
func TestWorkflowHelpCoversEveryFlag(t *testing.T) {
	doc, err := os.ReadFile("../../docs/workflows.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, topic := range []string{"workflows", "workflow"} {
		code, got, _ := runWith(t, "help", topic)
		if code != 0 {
			t.Fatalf("help %s exit = %d", topic, code)
		}
		if got != helpWorkflows {
			t.Errorf("help %s did not print the workflows topic:\n%s", topic, got)
		}
	}
	fs := newFlagSet(&options{})
	for _, name := range workflowHelpFlags {
		f := fs.Lookup(name)
		if f == nil {
			t.Fatalf("--%s is not a registered flag", name)
		}
		if !strings.Contains(helpWorkflows, "--"+name+" ") {
			t.Errorf("tinyagent help workflows lacks --%s", name)
		}
		if !strings.Contains(string(doc), "`--"+name) {
			t.Errorf("docs/workflows.md lacks --%s", name)
		}
		if !strings.Contains(f.Usage, "workflow") {
			t.Errorf("--%s usage doesn't mention workflows: %q", name, f.Usage)
		}
	}
	for _, ev := range []string{"workflow.started", "stage.started", "stage.gate", "stage.finished", "workflow.finished", "observer.proposed", "observer.recurrence"} {
		if !strings.Contains(helpWorkflows, ev) {
			t.Errorf("tinyagent help workflows lacks event %s", ev)
		}
		if !strings.Contains(string(doc), "`"+ev+"`") {
			t.Errorf("docs/workflows.md lacks event %s", ev)
		}
	}
	_, brief, _ := runWith(t, "--help")
	if !strings.Contains(brief, "tinyagent help workflows") || !strings.Contains(brief, "--workflow ") {
		t.Errorf("brief help doesn't point at workflows:\n%s", brief)
	}
}
