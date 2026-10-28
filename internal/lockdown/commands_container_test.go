package lockdown

import (
	"reflect"
	"strings"
	"testing"
)

func TestCommandsInContainer(t *testing.T) {
	var nilState *State
	nilState.SetCommandsInContainer(true)
	nilState.RefuseCommands()

	s, _ := New(nil)
	removed := 0
	s.ConfineCommands(false, func([]string) { removed++ })
	s.SetCommandsInContainer(true)
	if s.StopsProcessesAtTaint() {
		t.Error("container: processes stopped at taint, though they lose their network with it")
	}
	if !strings.Contains(s.Describe(), "after taint: commands in container (network removed at taint), network denied,") {
		t.Errorf("describe = %q", s.Describe())
	}
	want := map[string]any{"policy": "sandboxed", "network": "denied", "network_before_taint": "direct", "environment": "container"}
	if got := s.CommandsInfo(); !reflect.DeepEqual(got, want) {
		t.Errorf("CommandsInfo = %v, want %v", got, want)
	}
	s.Taint(Source{Source: SourceWebFetch, Tool: "web_fetch", Host: "a.example"})
	s.Taint(Source{Source: SourceWebFetch, Tool: "web_fetch", Host: "b.example"})
	if removed != 1 {
		t.Errorf("network removed %d times, want once at the first taint", removed)
	}

	n, _ := New(nil)
	n.ConfineCommands(false, nil)
	n.SetCommandsInContainer(false)
	if !strings.Contains(n.Describe(), "after taint: commands in container, network denied,") {
		t.Errorf("describe = %q", n.Describe())
	}
	want = map[string]any{"policy": "sandboxed", "network": "denied", "environment": "container"}
	if got := n.CommandsInfo(); !reflect.DeepEqual(got, want) {
		t.Errorf("CommandsInfo = %v, want %v", got, want)
	}
}

func TestRefuseCommands(t *testing.T) {
	s, _ := New(nil)
	if err := s.SetCommandAllowedDomains([]string{"proxy.golang.org"}); err != nil {
		t.Fatal(err)
	}
	s.ConfineCommands(false, nil)
	s.SetCommandsInContainer(true)
	s.RefuseCommands()
	if s.CommandsConfined() || !s.StopsProcessesAtTaint() {
		t.Error("commands still confined after RefuseCommands")
	}
	if got := s.CommandsInfo(); !reflect.DeepEqual(got, map[string]any{"policy": "refused"}) {
		t.Errorf("CommandsInfo = %v", got)
	}
	if got := s.CommandDomains(); !reflect.DeepEqual(got, []string{"proxy.golang.org"}) {
		t.Errorf("CommandDomains = %v", got)
	}
}
