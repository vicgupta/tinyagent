package telemetry

import "testing"

func TestLockdownCommandAttributes(t *testing.T) {
	for _, c := range []struct {
		name     string
		commands any
		want     map[string]any
	}{
		{"refused", map[string]any{"policy": "refused"},
			map[string]any{attrLockdownCommands: "refused"}},
		{"network denied", map[string]any{"policy": "sandboxed", "network": "denied"},
			map[string]any{attrLockdownCommands: "sandboxed", attrLockdownCommandNetwork: "denied"}},
		{"proxy", map[string]any{"policy": "sandboxed", "network": "proxy", "allowed_domains": []string{"proxy.golang.org", "a.example"}},
			map[string]any{attrLockdownCommands: "sandboxed", attrLockdownCommandNetwork: "proxy", attrLockdownCommandAllowedDomains: int64(2)}},
		{"proxy decoded from JSON", map[string]any{"policy": "sandboxed", "network": "proxy", "allowed_domains": []any{}},
			map[string]any{attrLockdownCommands: "sandboxed", attrLockdownCommandNetwork: "proxy", attrLockdownCommandAllowedDomains: int64(0)}},
		{"no command info", nil, map[string]any{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newReceiver(t)
			e, warn := New(Options{Settings: testSettings(r, "http/json"), Model: "openai/gpt-4o", Task: "task"})
			if warn != "" {
				t.Fatal(warn)
			}
			info := map[string]any{"enabled": true, "sources": []string{"web_fetch"}, "web_fetch_allowed_domains": []string{}, "tainted": false}
			if c.commands != nil {
				info["commands"] = c.commands
			}
			e.Emit("run.started", map[string]any{"schema_version": 1, "model": "openai/gpt-4o", "lockdown": info})
			e.Emit("result", map[string]any{"status": "Submitted", "steps": 0})
			if w := e.Close(); w != "" {
				t.Fatal(w)
			}
			var root tspan
			for _, s := range r.spans(t) {
				if s.name == "invoke_agent tinyagent" {
					root = s
				}
			}
			if root.attrs[attrLockdownEnabled] != true {
				t.Fatalf("root attrs = %v", root.attrs)
			}
			for _, k := range []string{attrLockdownCommands, attrLockdownCommandNetwork, attrLockdownCommandAllowedDomains} {
				got, ok := root.attrs[k]
				want, wantOK := c.want[k]
				if ok != wantOK || got != want {
					t.Errorf("%s = %v (set %v), want %v (set %v)", k, got, ok, want, wantOK)
				}
			}
		})
	}
}
