package resume

import (
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

func TestScopedNamesAndCheck(t *testing.T) {
	sel := tools.Selection{WebFetch: true, WebSearch: true}
	full := sel.Names()
	if !strings.Contains(strings.Join(full, ","), "web_search") {
		t.Fatalf("%v", full)
	}
	narrow := scopedNames(sel, []string{"read_file", "web_search"})
	if strings.Join(narrow, ",") != "read_file,web_search" {
		t.Fatalf("%v", narrow)
	}
	tr := &trajectory.Trajectory{
		Root: t.TempDir(), SchemaVersion: trajectory.SchemaVersion,
		ToolSignature: sel.Signature(), Tools: narrow, ScopedTools: []string{"read_file", "web_search"},
		WebFetchEnabled: true, WebSearchEnabled: true,
	}
	if err := Check(tr, Request{Root: tr.Root}); err != nil {
		t.Fatal(err)
	}
	// Pre-scoping checkpoint (no ScopedTools) still matches full names.
	old := &trajectory.Trajectory{
		Root: tr.Root, SchemaVersion: trajectory.SchemaVersion,
		ToolSignature: sel.Signature(), Tools: full,
		WebFetchEnabled: true, WebSearchEnabled: true,
	}
	if err := Check(old, Request{Root: old.Root}); err != nil {
		t.Fatal(err)
	}
}
