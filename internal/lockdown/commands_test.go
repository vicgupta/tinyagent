package lockdown

import (
	"reflect"
	"strings"
	"testing"
)

func TestCommandPolicyDescribe(t *testing.T) {
	s, _ := New(nil)
	if !strings.Contains(s.Describe(), "after taint: commands refused,") || s.CommandsConfined() {
		t.Errorf("unconfined describe = %q", s.Describe())
	}
	s.ConfineCommands(false, nil)
	if !s.CommandsConfined() || !strings.Contains(s.Describe(), "after taint: commands sandboxed, network denied,") {
		t.Errorf("network-denied describe = %q", s.Describe())
	}
	for _, tc := range []struct {
		domains []string
		want    string
	}{
		{nil, "commands sandboxed, network → 0 domains,"},
		{[]string{"proxy.golang.org"}, "commands sandboxed, network → 1 domain (proxy.golang.org),"},
		{[]string{"a.test", "*.b.test"}, "commands sandboxed, network → 2 domains (a.test, *.b.test),"},
	} {
		s, _ := New(nil)
		if err := s.SetCommandAllowedDomains(tc.domains); err != nil {
			t.Fatal(err)
		}
		s.ConfineCommands(true, func([]string) {})
		if !strings.Contains(s.Describe(), tc.want) {
			t.Errorf("describe = %q, want %q", s.Describe(), tc.want)
		}
		if !reflect.DeepEqual(s.CommandDomains(), append([]string(nil), tc.domains...)) {
			t.Errorf("CommandDomains = %v", s.CommandDomains())
		}
	}
}

func TestSetCommandAllowedDomainsRejectsBadEntries(t *testing.T) {
	s, _ := New(nil)
	for _, bad := range [][]string{{"*"}, {"bad host"}} {
		if err := s.SetCommandAllowedDomains(bad); err == nil || !strings.Contains(err.Error(), "untrusted_content.lockdown.allowed_domains") {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
	var nilState *State
	if nilState.SetCommandAllowedDomains([]string{"*"}) != nil || nilState.CommandsConfined() || nilState.CommandDomains() != nil {
		t.Error("nil state")
	}
	nilState.ConfineCommands(true, func([]string) { t.Error("nil state narrowed") })
}

func TestConfineCommandsNarrowsOnceAtFirstTaint(t *testing.T) {
	s, _ := New(nil)
	s.SetCommandAllowedDomains([]string{"proxy.golang.org"})
	var calls [][]string
	s.ConfineCommands(true, func(d []string) { calls = append(calls, d) })
	if len(calls) != 0 {
		t.Fatal("narrowed before taint")
	}
	s.Taint(Source{Source: SourceWebFetch, Tool: "web_fetch", Host: "a.test"})
	s.Taint(Source{Source: SourceWebFetch, Tool: "web_fetch", Host: "b.test"})
	if len(calls) != 1 || !reflect.DeepEqual(calls[0], []string{"proxy.golang.org"}) {
		t.Errorf("narrow calls = %v", calls)
	}
}

func TestConfineCommandsOnTaintedRunNarrowsNow(t *testing.T) {
	s, _ := New(nil)
	s.Taint(Source{Source: SourceUntrustedTask})
	n := 0
	s.ConfineCommands(true, func([]string) { n++ })
	s.Taint(Source{Source: SourceReview})
	if n != 1 {
		t.Errorf("narrow ran %d times", n)
	}
}

func TestTaintFromDisabledSourceDoesNotNarrow(t *testing.T) {
	s, _ := New(nil)
	if err := s.SetSources([]string{"mcp"}); err != nil {
		t.Fatal(err)
	}
	n := 0
	s.ConfineCommands(true, func([]string) { n++ })
	s.Taint(Source{Source: SourceWebFetch, Tool: "web_fetch"})
	if n != 0 || s.Tainted() {
		t.Errorf("a disabled source narrowed (%d) or tainted (%v)", n, s.Tainted())
	}
}
