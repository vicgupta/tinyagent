package lockdown

import (
	"reflect"
	"testing"
)

func TestInfo(t *testing.T) {
	var off *State
	if off.Info() != nil || off.EnabledSources() != nil || off.WebFetchDomains() != nil {
		t.Error("nil state reports info")
	}
	st, err := New([]string{"docs.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSources([]string{"web_fetch", "review"}); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"enabled": true, "sources": []string{"web_fetch", "review"}, "web_fetch_allowed_domains": []string{"docs.example.com"}, "tainted": false, "commands": map[string]any{"policy": "refused"}}
	if got := st.Info(); !reflect.DeepEqual(got, want) {
		t.Errorf("Info = %v, want %v", got, want)
	}
	st, _ = New(nil)
	st.Taint(Source{Source: SourceUntrustedTask})
	info := st.Info()
	if !reflect.DeepEqual(info["sources"], ConfigurableSources) || !reflect.DeepEqual(info["web_fetch_allowed_domains"], []string{}) || info["tainted"] != true {
		t.Errorf("default Info = %v", info)
	}
}

func TestRestore(t *testing.T) {
	st, _ := New(nil)
	// A source turned off now still restores: taint is sticky.
	if err := st.SetSources([]string{"review"}); err != nil {
		t.Fatal(err)
	}
	st.Taint(Source{Source: SourceUntrustedTask})
	old := []Source{{Source: SourceWebFetch, Step: 2, ToolCallID: "c1", Tool: "web_fetch", Host: "a.example"}, {Source: SourceWebFetch, Step: 3, ToolCallID: "c2", Tool: "web_fetch", Host: "a.example"}}
	st.Restore(old, []Denial{{Channel: ChannelCommand, Tool: "bash", ToolCallID: "c3", Step: 4}})
	src := st.Sources()
	if len(src) != 2 || src[0].Source != SourceWebFetch || !src[0].Resumed || src[0].Step != 2 || src[1].Source != SourceUntrustedTask || src[1].Resumed {
		t.Errorf("sources = %+v", src)
	}
	if d := st.Denials(); len(d) != 1 || d[0].Tool != "bash" {
		t.Errorf("denials = %+v", d)
	}
	if f := src[0].Fields(); f["resumed"] != true || f["host"] != "a.example" {
		t.Errorf("fields = %v", f)
	}
	// The restored source counts as seen, and the notice as given.
	if added, _ := st.Taint(Source{Source: SourceWebFetch, Tool: "web_fetch", Host: "a.example", Step: 9}); added {
		t.Error("restored source added again")
	}
	fresh, _ := New(nil)
	fresh.Restore(old[:1], nil)
	if _, first := fresh.Taint(Source{Source: SourceMCP, Tool: "m"}); first {
		t.Error("first-taint notice repeated after restore")
	}
	fresh.Restore(nil, nil)
	if len(fresh.Sources()) != 2 {
		t.Errorf("empty restore changed sources: %+v", fresh.Sources())
	}
}
