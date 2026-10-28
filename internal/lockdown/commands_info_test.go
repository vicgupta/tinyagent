package lockdown

import (
	"reflect"
	"testing"
)

func TestCommandsInfo(t *testing.T) {
	var off *State
	if off.CommandsInfo() != nil {
		t.Error("nil state reports command info")
	}
	for _, c := range []struct {
		name    string
		domains []string
		confine func(*State)
		want    map[string]any
	}{
		{"refused", []string{"proxy.golang.org"}, func(*State) {}, map[string]any{"policy": "refused"}},
		{"network denied", []string{"proxy.golang.org"}, func(s *State) { s.ConfineCommands(false, nil) },
			map[string]any{"policy": "sandboxed", "network": "denied"}},
		{"proxy, no domains", nil, func(s *State) { s.ConfineCommands(true, func([]string) {}) },
			map[string]any{"policy": "sandboxed", "network": "proxy", "allowed_domains": []string{}}},
		{"proxy with domains", []string{"proxy.golang.org", "*.example.com"}, func(s *State) { s.ConfineCommands(true, func([]string) {}) },
			map[string]any{"policy": "sandboxed", "network": "proxy", "allowed_domains": []string{"proxy.golang.org", "*.example.com"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			st, err := New(nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.SetCommandAllowedDomains(c.domains); err != nil {
				t.Fatal(err)
			}
			c.confine(st)
			if got := st.CommandsInfo(); !reflect.DeepEqual(got, c.want) {
				t.Errorf("CommandsInfo = %v, want %v", got, c.want)
			}
			if got := st.Info()["commands"]; !reflect.DeepEqual(got, c.want) {
				t.Errorf("Info commands = %v, want %v", got, c.want)
			}
		})
	}
}

func TestCommandsInfoIsACopy(t *testing.T) {
	st, _ := New(nil)
	if err := st.SetCommandAllowedDomains([]string{"proxy.golang.org"}); err != nil {
		t.Fatal(err)
	}
	st.ConfineCommands(true, func([]string) {})
	st.CommandsInfo()["allowed_domains"].([]string)[0] = "evil.example"
	if d := st.CommandDomains(); d[0] != "proxy.golang.org" {
		t.Errorf("CommandsInfo leaked internal state: %v", d)
	}
}
