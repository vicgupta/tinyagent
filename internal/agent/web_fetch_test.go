package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/hooks/hookstest"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

func fetchCall(id, url string) model.ToolCall {
	b, _ := json.Marshal(map[string]string{"url": url})
	return call(id, "web_fetch", string(b))
}

func webFetchServer(t *testing.T, hits *atomic.Int32) (*httptest.Server, *tools.WebFetcher) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<p>docs page</p>")
	}))
	t.Cleanup(srv.Close)
	f, err := tools.NewWebFetcher([]string{srv.Listener.Addr().String()}, 0, 5*time.Second, "tinyagent/test")
	if err != nil {
		t.Fatal(err)
	}
	return srv, f
}

func TestWebFetchOffKeepsSignature(t *testing.T) { // AC8
	for _, ro := range []bool{false, true} {
		for _, d := range []bool{false, true} {
			if WebFetchToolSignature(ro, false, false, false, d, false) != (tools.Selection{ReadOnly: ro, Delegate: d}).Signature() {
				t.Errorf("signature changed with web_fetch off (read-only %v, delegate %v)", ro, d)
			}
			if s := WebFetchToolSignature(ro, false, false, false, d, true); !strings.HasPrefix(s, "web-fetch-v1:") {
				t.Errorf("web_fetch signature %s", s)
			}
		}
	}
	if WebFetchToolSignature(false, false, false, false, false, false) != ToolSignature() {
		t.Error("default signature changed")
	}
}

func TestWebFetchOffByDefault(t *testing.T) { // AC1
	m := &specModel{scripted: scripted{replies: []model.Message{
		assistantCalls(fetchCall("1", "https://pkg.go.dev/fmt")),
		{Role: "assistant", Content: "done"},
	}}}
	a := newAgent(t, m)
	tr := a.Run(context.Background(), "x")
	if slices.Contains(toolNames(m.specs[0]), "web_fetch") {
		t.Errorf("web_fetch advertised by default")
	}
	if res := toolResults(tr.Messages); len(res) != 1 || !strings.HasPrefix(res[0], "format error: unknown tool") {
		t.Errorf("results = %q", res)
	}
	b, _ := json.Marshal(tr)
	if strings.Contains(string(b), `"web_fetch_enabled"`) || strings.Contains(string(b), `"web_fetch":`) || tr.ToolSignature != ToolSignature() {
		t.Errorf("trajectory has web_fetch fields: %s", b)
	}
}

func TestWebFetchRunEventsAndResume(t *testing.T) { // AC7, AC8, AC9
	srv, f := webFetchServer(t, nil)
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{fetchCall("1", srv.URL+"/doc"), fetchCall("2", "http://elsewhere.invalid/")}},
		submit,
	}}
	a := newAgent(t, m)
	a.Tools.WebFetch = f
	var errBuf bytes.Buffer
	a.Err, a.Verbose = &errBuf, true
	var events []string
	var fetches []map[string]any
	a.OnEvent = func(kind string, fields map[string]any) {
		events = append(events, kind)
		if kind == "web_fetch" {
			fetches = append(fetches, fields)
		}
	}
	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusSubmitted || !tr.WebFetchEnabled || !strings.HasPrefix(tr.ToolSignature, "web-fetch-v1:") {
		t.Fatalf("status %s, enabled %v, signature %s", tr.ExitStatus, tr.WebFetchEnabled, tr.ToolSignature)
	}
	if tr.WebFetch == nil || !slices.Equal(tr.WebFetch.AllowedDomains, f.Domains) {
		t.Errorf("trajectory web_fetch = %+v", tr.WebFetch)
	}
	res := toolResults(tr.Messages)
	if len(res) != 2 || !strings.HasSuffix(res[0], "\ndocs page") || !strings.Contains(res[1], "web_fetch.allowed_domains") {
		t.Fatalf("results = %q", res)
	}
	if len(fetches) != 2 || fetches[0]["status"] != 200 || fetches[0]["url"] != srv.URL+"/doc" || fetches[0]["final_url"] != srv.URL+"/doc" ||
		fetches[0]["content_type"] != "text/html" || fetches[0]["truncated"] != false || fetches[0]["duration_ms"] == nil ||
		fetches[1]["denied_reason"] != tools.WebFetchDeniedHost {
		t.Errorf("web_fetch events = %v", fetches)
	}
	if i := slices.Index(events, "web_fetch"); i < 0 || events[i+1] != "tool.result" {
		t.Errorf("events = %v", events)
	}
	if !strings.Contains(errBuf.String(), "web_fetch: "+srv.URL+"/doc -> 200 text/html") || !strings.Contains(errBuf.String(), "denied") {
		t.Errorf("verbose output = %q", errBuf.String())
	}
	if err := ValidateResume(tr, a.Config, a.Tools.Root); err != nil {
		t.Errorf("ValidateResume: %v", err)
	}
	a.Tools.WebFetch = nil
	a.Resume, a.Model, a.FollowUp = tr, &scripted{replies: []model.Message{submit}}, "more"
	if r := a.Run(context.Background(), "more"); r.ExitStatus != StatusError || !strings.Contains(r.Submission, "web_fetch setting differs") {
		t.Errorf("mismatched resume: %s %q", r.ExitStatus, r.Submission)
	}
	tampered := *tr
	tampered.WebFetchEnabled = false
	if err := ValidateResume(&tampered, a.Config, a.Tools.Root); err == nil {
		t.Error("ValidateResume accepted a changed web_fetch setting")
	}
	v1 := trajectory.Trajectory{SchemaVersion: 1, WebFetchEnabled: true}
	if err := v1.Validate(); err == nil || !strings.Contains(err.Error(), "version 2 fields") {
		t.Errorf("schema 1 with web_fetch: %v", err)
	}
}

func TestWebFetchHookDenyMakesNoRequest(t *testing.T) { // AC10
	var hits atomic.Int32
	srv, f := webFetchServer(t, &hits)
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{fetchCall("1", srv.URL+"/secret")}},
		{Role: "assistant", Content: "ok"},
	}}
	a := newAgent(t, m)
	a.Tools.WebFetch = f
	withHooks(t, a, config.Hooks{PreTool: []config.Hook{hookstest.Hook(t, "web_fetch", "exit2", "no fetching")}})
	tr := a.Run(context.Background(), "task")
	if res := toolResults(tr.Messages); len(res) != 1 || !strings.HasPrefix(res[0], "error: denied by pre_tool hook ") {
		t.Fatalf("results = %q", res)
	}
	if hits.Load() != 0 {
		t.Error("denied fetch reached the server")
	}
}

func TestWebFetchReadOnlyAndNotForSubagents(t *testing.T) { // AC3, AC7
	_, f := webFetchServer(t, nil)
	e := &tools.Executor{Root: t.TempDir(), ReadOnly: true, WebFetch: f}
	if !slices.Contains(toolNames(e.Catalog()), "web_fetch") {
		t.Errorf("read-only catalog %v lacks web_fetch", toolNames(e.Catalog()))
	}
	a, pm, cm := subagentAgent(t, []model.Message{
		assistantCalls(delegateCall("1", "look around")),
		{Role: "assistant", Content: "done" + submitMarker},
	}, []model.Message{{Role: "assistant", Content: "found it" + submitMarker}})
	a.Tools.WebFetch = f
	a.Run(context.Background(), "x")
	if !slices.Contains(toolNames(pm.specs[0]), "web_fetch") {
		t.Errorf("parent tools %v lack web_fetch", toolNames(pm.specs[0]))
	}
	if len(cm.specs) == 0 {
		t.Fatal("child never ran")
	}
	if slices.Contains(toolNames(cm.specs[0]), "web_fetch") {
		t.Errorf("child offered web_fetch: %v", toolNames(cm.specs[0]))
	}
}
