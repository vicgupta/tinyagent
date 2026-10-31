package agent

import "testing"

func TestPickChild(t *testing.T) {
	routes := []ChildRoute{
		{Match: []string{"Schema"}, ModelName: "a/schema"},
		{Type: "docs", ModelName: "a/docs"},
	}
	for _, c := range []struct{ task, typ, model, reason string }{
		{"update the SCHEMA file", "", "a/schema", `match "schema"`},
		{"write things", "docs", "a/docs", "type docs"},
		{"other", "", "def", "default"},
	} {
		_, name, reason := pickChild(routes, nil, "def", c.task, c.typ)
		if name != c.model || reason != c.reason {
			t.Errorf("%q/%q -> %s, %s", c.task, c.typ, name, reason)
		}
	}
	if _, name, reason := pickChild(nil, nil, "def", "schema", "docs"); name != "def" || reason != "" {
		t.Errorf("no routes -> %s, %q", name, reason)
	}
}
