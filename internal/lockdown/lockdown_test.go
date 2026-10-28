package lockdown

import (
	"strings"
	"sync"
	"testing"
)

func TestNilStateIsOff(t *testing.T) {
	var s *State
	if s.Tainted() || !s.WebFetchAllowed("example.com", 443) || s.WebFetchGate("example.com", 443) != nil {
		t.Error("nil state restricts")
	}
	if added, first := s.Taint(Source{Source: SourceMCP}); added || first {
		t.Error("nil state recorded a taint")
	}
	s.Deny(Denial{})
	if s.Sources() != nil || s.Denials() != nil || s.Describe() != "" {
		t.Error("nil state has records")
	}
}

func TestNewRejectsBadEntries(t *testing.T) {
	for _, bad := range [][]string{{"*"}, {"exa mple.com"}, {""}} {
		if _, err := New(bad); err == nil || !strings.Contains(err.Error(), "untrusted_content.lockdown.web_fetch_allowed_domains") {
			t.Errorf("New(%q) err = %v", bad, err)
		}
	}
}

func TestTaintDedupesAndNotifiesOnce(t *testing.T) {
	s, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.Tainted() {
		t.Fatal("new state tainted")
	}
	if _, ok := s.First(); ok {
		t.Fatal("First on untainted state")
	}
	web := Source{Source: SourceWebFetch, Step: 1, ToolCallID: "1", Tool: "web_fetch", Host: "pkg.go.dev"}
	if added, first := s.Taint(web); !added || !first {
		t.Errorf("first taint added %v first %v", added, first)
	}
	again := web
	again.Step, again.ToolCallID = 2, "2"
	if added, _ := s.Taint(again); added {
		t.Error("same source, tool and host recorded twice")
	}
	if added, first := s.Taint(Source{Source: SourceMCP, Step: 3, Tool: "docs__search"}); !added || first {
		t.Errorf("new source added %v first %v", added, first)
	}
	src := s.Sources()
	if len(src) != 2 || src[0] != web || !s.Tainted() {
		t.Errorf("sources = %+v", src)
	}
	if f, _ := s.First(); f != web {
		t.Errorf("first = %+v", f)
	}
	f := web.Fields()
	if f["source"] != "web_fetch" || f["host"] != "pkg.go.dev" || f["tool_call_id"] != "1" || f["step"] != 1 {
		t.Errorf("fields = %v", f)
	}
	if f := (Source{Source: SourceUntrustedTask}).Fields(); len(f) != 2 {
		t.Errorf("start taint fields = %v", f)
	}
}

func TestStartTaintUsesUpNotice(t *testing.T) {
	s, _ := New(nil)
	s.Taint(Source{Source: SourceUntrustedTask})
	if _, first := s.Taint(Source{Source: SourceMCP, Tool: "x"}); first {
		t.Error("notice offered after a start-of-run taint")
	}
}

func TestWebFetchAllowlistAppliesOnlyAfterTaint(t *testing.T) {
	s, err := New([]string{"pkg.go.dev", "*.golang.org"})
	if err != nil {
		t.Fatal(err)
	}
	if !s.WebFetchAllowed("evil.example", 443) {
		t.Error("untainted state refused a host")
	}
	s.Taint(Source{Source: SourceMCP})
	for host, want := range map[string]bool{"pkg.go.dev": true, "PKG.GO.DEV.": true, "go.golang.org": true, "evil.example": false, "pkg.go.dev.evil.example": false} {
		if got := s.WebFetchAllowed(host, 443); got != want {
			t.Errorf("WebFetchAllowed(%s) = %v", host, got)
		}
	}
	if err := s.WebFetchGate("evil.example", 443); err == nil || !strings.Contains(err.Error(), "evil.example") {
		t.Errorf("gate err = %v", err)
	}
	if !strings.Contains(s.Describe(), "web_fetch → 2 domains") {
		t.Errorf("describe = %q", s.Describe())
	}
}

func TestURLHostPort(t *testing.T) {
	for raw, want := range map[string]struct {
		host string
		port int
		ok   bool
	}{
		"https://pkg.go.dev/fmt?q=secret": {"pkg.go.dev", 443, true},
		"http://Example.COM./x":           {"example.com", 80, true},
		"http://127.0.0.1:8080/":          {"127.0.0.1", 8080, true},
		"http://[::1]:9/":                 {"::1", 9, true},
		"ftp://example.com/":              {"", 0, false},
		"https:///nohost":                 {"", 0, false},
		"http://example.com:0/":           {"", 0, false},
		"::bad":                           {"", 0, false},
	} {
		h, p, ok := URLHostPort(raw)
		if h != want.host || p != want.port || ok != want.ok {
			t.Errorf("URLHostPort(%q) = %q %d %v", raw, h, p, ok)
		}
	}
}

func TestRefusalMessage(t *testing.T) {
	first := Source{Source: SourceWebFetch, Step: 2}
	for channel, want := range map[string]string{
		ChannelCommand:  "can't yet restrict their network",
		ChannelWebFetch: "host evil.example is not in untrusted_content.lockdown.web_fetch_allowed_domains",
		ChannelMCP:      "MCP tools are not callable",
	} {
		msg := RefusalMessage("tool", channel, first, "evil.example")
		if !strings.HasPrefix(msg, "error: lockdown: tool refused because this run has read untrusted content (web_fetch, step 2)") || !strings.Contains(msg, want) {
			t.Errorf("%s: %q", channel, msg)
		}
	}
}

func TestConcurrentUse(t *testing.T) {
	s, _ := New(nil)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Taint(Source{Source: SourceMCP, Tool: "t"})
			s.Deny(Denial{Channel: ChannelCommand})
			_ = s.Tainted()
		}()
	}
	wg.Wait()
	if len(s.Sources()) != 1 || len(s.Denials()) != 20 {
		t.Errorf("sources %d denials %d", len(s.Sources()), len(s.Denials()))
	}
}
