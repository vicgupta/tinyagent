package lockdown

import (
	"strings"
	"testing"
)

func TestParseSources(t *testing.T) {
	if set, err := ParseSources(nil); err != nil || set != nil {
		t.Errorf("nil = %v, %v", set, err)
	}
	set, err := ParseSources([]string{SourceMCP, SourceACPClientMCP})
	if err != nil || len(set) != 2 || !set[SourceMCP] || !set[SourceACPClientMCP] {
		t.Errorf("set = %v, %v", set, err)
	}
	for _, c := range []struct {
		list []string
		want string
	}{
		{[]string{}, "is empty"},
		{[]string{"webfetch"}, `unknown source "webfetch"`},
		{[]string{SourceUntrustedTask}, `unknown source "untrusted_task"`},
		{[]string{SourceResume}, `unknown source "resume"`},
		{[]string{SourceMCP, SourceMCP}, `duplicate source "mcp"`},
	} {
		if _, err := ParseSources(c.list); err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "untrusted_content.sources") {
			t.Errorf("%q: err = %v, want %q", c.list, err, c.want)
		}
	}
}

func TestSourcesDefaultAll(t *testing.T) {
	s, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range append(ConfigurableSources, SourceUntrustedTask, SourceResume) {
		if !s.TaintsFrom(src) {
			t.Errorf("%s doesn't taint by default", src)
		}
	}
	if !strings.Contains(s.Describe(), "sources: web_fetch, mcp, review, untrusted task;") {
		t.Errorf("describe = %q", s.Describe())
	}
}

func TestSetSourcesLimitsTaint(t *testing.T) {
	s, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSources([]string{SourceMCP}); err != nil {
		t.Fatal(err)
	}
	if added, _ := s.Taint(Source{Source: SourceWebFetch, Host: "example.com"}); added || s.Tainted() {
		t.Error("web_fetch tainted with sources: [mcp]")
	}
	if added, _ := s.Taint(Source{Source: SourceReview}); added || s.Tainted() {
		t.Error("review tainted with sources: [mcp]")
	}
	if added, first := s.Taint(Source{Source: SourceMCP, Tool: "docs__search"}); !added || !first {
		t.Errorf("mcp taint added=%v first=%v", added, first)
	}
	if got := s.Describe(); !strings.Contains(got, "sources: mcp, untrusted task;") {
		t.Errorf("describe = %q", got)
	}

	// An untrusted task and a resumed untrusted checkpoint always taint.
	for _, src := range []string{SourceUntrustedTask, SourceResume} {
		s, _ := New(nil)
		if err := s.SetSources([]string{SourceWebFetch}); err != nil {
			t.Fatal(err)
		}
		if added, _ := s.Taint(Source{Source: src}); !added || !s.Tainted() {
			t.Errorf("%s didn't taint with sources: [web_fetch]", src)
		}
	}

	if err := s.SetSources([]string{"bogus"}); err == nil {
		t.Error("bogus source accepted")
	}
	var nilState *State
	if err := nilState.SetSources([]string{SourceMCP}); err != nil || nilState.TaintsFrom(SourceMCP) {
		t.Error("nil state")
	}
}
