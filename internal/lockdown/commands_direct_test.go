package lockdown

import (
	"reflect"
	"strings"
	"testing"
)

func TestCommandsDirectUntilTaint(t *testing.T) {
	var nilState *State
	nilState.SetCommandsDirectUntilTaint()
	if nilState.StopsProcessesAtTaint() {
		t.Error("nil state stops processes")
	}

	s, _ := New(nil)
	if !s.StopsProcessesAtTaint() {
		t.Error("unconfined commands: processes not stopped at taint")
	}
	s.ConfineCommands(false, nil)
	if s.StopsProcessesAtTaint() {
		t.Error("confined commands: processes stopped at taint")
	}
	s.SetCommandsDirectUntilTaint()
	if !s.StopsProcessesAtTaint() {
		t.Error("direct until taint: processes not stopped at taint")
	}
	if !strings.Contains(s.Describe(), "after taint: commands sandboxed (direct network until taint), network denied,") {
		t.Errorf("describe = %q", s.Describe())
	}
	want := map[string]any{"policy": "sandboxed", "network": "denied", "network_before_taint": "direct"}
	if got := s.CommandsInfo(); !reflect.DeepEqual(got, want) {
		t.Errorf("CommandsInfo = %v, want %v", got, want)
	}

	p, _ := New(nil)
	if err := p.SetCommandAllowedDomains([]string{"proxy.golang.org"}); err != nil {
		t.Fatal(err)
	}
	var narrowed []string
	p.ConfineCommands(true, func(d []string) { narrowed = d })
	p.SetCommandsDirectUntilTaint()
	if !strings.Contains(p.Describe(), "commands sandboxed (direct network until taint), network → 1 domain (proxy.golang.org),") {
		t.Errorf("describe = %q", p.Describe())
	}
	want = map[string]any{"policy": "sandboxed", "network": "proxy", "allowed_domains": []string{"proxy.golang.org"}, "network_before_taint": "direct"}
	if got := p.CommandsInfo(); !reflect.DeepEqual(got, want) {
		t.Errorf("CommandsInfo = %v, want %v", got, want)
	}
	p.Taint(Source{Source: SourceWebFetch})
	if !reflect.DeepEqual(narrowed, []string{"proxy.golang.org"}) {
		t.Errorf("narrow hook got %v", narrowed)
	}
}
