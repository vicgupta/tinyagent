package mcptools

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vicgupta/tinyagent/internal/config"
)

// remoteServer is an in-process Streamable HTTP MCP server that records the
// requests it receives.
type remoteServer struct {
	*httptest.Server
	requests atomic.Int64
	mu       sync.Mutex
	auth     []string
	apiKey   []string
}

func newRemoteServer(t *testing.T, jsonResponse bool) *remoteServer {
	t.Helper()
	s := mcp.NewServer(&mcp.Implementation{Name: "remote", Version: "1"}, nil)
	mcp.AddTool[map[string]any, any](s, &mcp.Tool{Name: "search", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}}}, func(_ context.Context, _ *mcp.CallToolRequest, in map[string]any) (*mcp.CallToolResult, any, error) {
		q, _ := in["q"].(string)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "found " + q}}}, nil, nil
	})
	mcp.AddTool[map[string]any, any](s, &mcp.Tool{Name: "slow", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, _ *mcp.CallToolRequest, _ map[string]any) (*mcp.CallToolResult, any, error) {
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "late"}}}, nil, nil
	})
	mcp.AddTool[map[string]any, any](s, &mcp.Tool{Name: "drop", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "not dropped"}}}, nil, nil
	})
	mcp.AddTool[map[string]any, any](s, &mcp.Tool{Name: "union", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"v": map[string]any{"anyOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "number"}}}}}}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
		return nil, nil, nil
	})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{JSONResponse: jsonResponse})
	rs := &remoteServer{}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.requests.Add(1)
		rs.mu.Lock()
		rs.auth = append(rs.auth, r.Header.Get("Authorization"))
		rs.apiKey = append(rs.apiKey, r.Header.Get("X-Api-Key"))
		rs.mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(`"name":"drop"`)) {
			// Simulate the connection dropping mid-call.
			if hj, ok := w.(http.Hijacker); ok {
				if c, _, err := hj.Hijack(); err == nil {
					c.Close()
					return
				}
			}
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(rs.Close)
	return rs
}

func startRemote(t *testing.T, defs ...config.MCPServer) (*Registry, string, error) {
	t.Helper()
	var stderr bytes.Buffer
	r, err := Start(context.Background(), t.TempDir(), defs, &stderr)
	if r != nil {
		t.Cleanup(r.Close)
	}
	return r, stderr.String(), err
}

func TestRemoteServerSSEAndJSONWithStdio(t *testing.T) {
	sse := newRemoteServer(t, false)
	js := newRemoteServer(t, true)
	t.Setenv("TINYAGENT_MCP_FIXTURE", "1")
	stdio := config.MCPServer{Alias: "docs", Command: os.Args[0], Args: []string{"-test.run=^TestMCPFixtureProcess$"}, Env: []string{"TINYAGENT_MCP_FIXTURE"}, Tools: []string{"fetch"}}
	r, stderr, err := startRemote(t, stdio,
		config.MCPServer{Alias: "gh", URL: sse.URL + "/mcp?token=abc", Tools: []string{"search"}},
		config.MCPServer{Alias: "js", URL: js.URL + "/mcp", Tools: []string{"search"}})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range r.Specs {
		names = append(names, s.Function.Name)
	}
	if strings.Join(names, ",") != "docs__fetch,gh__search,js__search" {
		t.Fatalf("catalog = %v", names)
	}
	for _, name := range []string{"gh__search", "js__search"} {
		got, isErr, ok := r.Call(context.Background(), name, `{"q":"x"}`)
		if !ok || isErr || got != "found x" {
			t.Errorf("%s = %q, isErr %v, ok %v", name, got, isErr, ok)
		}
	}
	if got, isErr, _ := r.Call(context.Background(), "docs__fetch", `{"url":"u"}`); isErr || got != "fetched u" {
		t.Errorf("stdio call = %q", got)
	}
	want := "mcp: enabled server gh (http " + sse.URL + ", 1 selected tools)"
	if !strings.Contains(stderr, want) || strings.Contains(stderr, "/mcp") || strings.Contains(stderr, "token=abc") {
		t.Errorf("stderr = %q, want %q and no path or query", stderr, want)
	}
}

func TestRemoteCredentialsSentAndNeverPrinted(t *testing.T) {
	rs := newRemoteServer(t, false)
	t.Setenv("GH_MCP_TOKEN", "secret123")
	t.Setenv("DOCS_KEY", "key456")
	r, stderr, err := startRemote(t, config.MCPServer{Alias: "gh", URL: rs.URL + "/mcp", BearerTokenEnv: "GH_MCP_TOKEN", HeadersEnv: map[string]string{"X-Api-Key": "DOCS_KEY"}, Tools: []string{"search", "drop"}})
	if err != nil {
		t.Fatal(err)
	}
	got, _, _ := r.Call(context.Background(), "gh__search", `{"q":"y"}`)
	dropped, _, _ := r.Call(context.Background(), "gh__drop", `{}`)
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if len(rs.auth) == 0 {
		t.Fatal("server received no requests")
	}
	for i := range rs.auth {
		if rs.auth[i] != "Bearer secret123" || rs.apiKey[i] != "key456" {
			t.Fatalf("request %d headers: Authorization %q, X-Api-Key %q", i, rs.auth[i], rs.apiKey[i])
		}
	}
	for _, out := range []string{stderr, got, dropped} {
		if strings.Contains(out, "secret123") || strings.Contains(out, "key456") {
			t.Errorf("credential leaked: %q", out)
		}
	}
	vals := strings.Join(CredentialValues([]config.MCPServer{{URL: "x", BearerTokenEnv: "GH_MCP_TOKEN", HeadersEnv: map[string]string{"X-Api-Key": "DOCS_KEY"}}}), ",")
	if vals != "key456,secret123" && vals != "secret123,key456" {
		t.Errorf("CredentialValues = %q", vals)
	}
}

func TestRemoteConfigValidation(t *testing.T) {
	t.Setenv("SET_TOKEN", "v4lue")
	t.Setenv("EMPTY_TOKEN", "")
	cases := []struct {
		name string
		def  config.MCPServer
		want string
	}{
		{"both", config.MCPServer{Command: "/bin/x", URL: "https://example.com/mcp"}, "exactly one of command"},
		{"neither", config.MCPServer{}, "exactly one of command"},
		{"args", config.MCPServer{URL: "https://example.com/mcp", Args: []string{"-x"}}, "args and env apply only"},
		{"env", config.MCPServer{URL: "https://example.com/mcp", Env: []string{"SET_TOKEN"}}, "args and env apply only"},
		{"http", config.MCPServer{URL: "http://example.com/mcp"}, "must use https"},
		{"scheme", config.MCPServer{URL: "ftp://example.com/mcp"}, "must use https"},
		{"relative", config.MCPServer{URL: "/mcp"}, "absolute"},
		{"userinfo", config.MCPServer{URL: "https://u:p@example.com/mcp"}, "must not contain credentials"},
		{"unset", config.MCPServer{URL: "https://example.com/mcp", HeadersEnv: map[string]string{"X-Api-Key": "NOPE_UNSET_VAR"}}, "NOPE_UNSET_VAR is unset or empty"},
		{"empty", config.MCPServer{URL: "https://example.com/mcp", BearerTokenEnv: "EMPTY_TOKEN"}, "EMPTY_TOKEN is unset or empty"},
		{"badheader", config.MCPServer{URL: "https://example.com/mcp", HeadersEnv: map[string]string{"X Api": "SET_TOKEN"}}, "invalid header name"},
		{"authdup", config.MCPServer{URL: "https://example.com/mcp", BearerTokenEnv: "SET_TOKEN", HeadersEnv: map[string]string{"authorization": "SET_TOKEN"}}, "Authorization while bearer_token_env"},
		{"stdiocreds", config.MCPServer{Command: "/bin/x", BearerTokenEnv: "SET_TOKEN"}, "apply only to url servers"},
	}
	for _, h := range []string{"Accept", "content-type", "MCP-Session-Id", "MCP-Protocol-Version", "Host", "Content-Length"} {
		cases = append(cases, struct {
			name string
			def  config.MCPServer
			want string
		}{"reserved " + h, config.MCPServer{URL: "https://example.com/mcp", HeadersEnv: map[string]string{h: "SET_TOKEN"}}, "may not set"})
	}
	for _, c := range cases {
		c.def.Alias = "gh"
		c.def.Tools = []string{"search"}
		_, _, err := startRemote(t, c.def)
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), `"gh"`) {
			t.Errorf("%s: err = %v, want %q naming the alias", c.name, err, c.want)
		}
		if err != nil && strings.Contains(err.Error(), "v4lue") {
			t.Errorf("%s: error leaks a credential value: %v", c.name, err)
		}
	}
	// Loopback http is allowed (it fails later, at connect, not validation).
	_, _, err := startRemote(t, config.MCPServer{Alias: "gh", URL: "http://127.0.0.1:1/mcp", Tools: []string{"search"}, DiscoveryTimeoutSec: 2})
	if err == nil || strings.Contains(err.Error(), "must use https") {
		t.Errorf("loopback http: err = %v, want a connection error", err)
	}
}

func TestRemoteRedirectToOtherOriginFails(t *testing.T) {
	target := newRemoteServer(t, false)
	front := httptest.NewServer(http.RedirectHandler(target.URL+"/mcp", http.StatusTemporaryRedirect))
	defer front.Close()
	t.Setenv("GH_MCP_TOKEN", "secret123")
	_, _, err := startRemote(t, config.MCPServer{Alias: "gh", URL: front.URL + "/mcp", BearerTokenEnv: "GH_MCP_TOKEN", Tools: []string{"search"}})
	if err == nil || !strings.Contains(err.Error(), "different origin") {
		t.Fatalf("err = %v, want a cross-origin redirect failure", err)
	}
	target.mu.Lock()
	defer target.mu.Unlock()
	for _, a := range target.auth {
		if a != "" {
			t.Errorf("redirect target received Authorization %q", a)
		}
	}
}

func TestRemoteStartupErrors(t *testing.T) {
	t.Setenv("GH_MCP_TOKEN", "secret123")
	unauthorized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer unauthorized.Close()
	_, _, err := startRemote(t, config.MCPServer{Alias: "gh", URL: unauthorized.URL + "/mcp", BearerTokenEnv: "GH_MCP_TOKEN", Tools: []string{"search"}})
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") || !strings.Contains(err.Error(), "GH_MCP_TOKEN") || strings.Contains(err.Error(), "secret123") {
		t.Errorf("401: err = %v", err)
	}
	notFound := httptest.NewServer(http.NotFoundHandler())
	defer notFound.Close()
	_, _, err = startRemote(t, config.MCPServer{Alias: "gh", URL: notFound.URL + "/mcp", Tools: []string{"search"}})
	if err == nil || !strings.Contains(err.Error(), "may not be a Streamable HTTP MCP endpoint") {
		t.Errorf("404: err = %v", err)
	}
	html := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<html>hi</html>")
	}))
	defer html.Close()
	_, _, err = startRemote(t, config.MCPServer{Alias: "gh", URL: html.URL + "/mcp", Tools: []string{"search"}})
	if err == nil || !strings.Contains(err.Error(), "may not be a Streamable HTTP MCP endpoint") {
		t.Errorf("html: err = %v", err)
	}
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	_, _, err = startRemote(t, config.MCPServer{Alias: "gh", URL: "http://" + addr + "/mcp", Tools: []string{"search"}})
	if err == nil || !strings.Contains(err.Error(), "could not reach the server") {
		t.Errorf("refused: err = %v", err)
	}
	tlsSrv := httptest.NewTLSServer(http.NotFoundHandler())
	defer tlsSrv.Close()
	_, _, err = startRemote(t, config.MCPServer{Alias: "gh", URL: tlsSrv.URL + "/mcp", Tools: []string{"search"}})
	if err == nil || !strings.Contains(err.Error(), "TLS verification failed") {
		t.Errorf("tls: err = %v", err)
	}
	// The same catalog checks as stdio servers apply.
	rs := newRemoteServer(t, false)
	_, _, err = startRemote(t, config.MCPServer{Alias: "gh", URL: rs.URL, Tools: []string{"missing"}})
	if err == nil || !strings.Contains(err.Error(), `selected tool "missing" was not discovered`) {
		t.Errorf("missing tool: err = %v", err)
	}
	_, _, err = startRemote(t, config.MCPServer{Alias: "gh", URL: rs.URL, Tools: []string{"union"}})
	if err == nil || !strings.Contains(err.Error(), `keyword "anyOf" is unsupported`) {
		t.Errorf("anyOf: err = %v", err)
	}
}

func TestRemoteCallsTimeoutDropAndDenial(t *testing.T) {
	rs := newRemoteServer(t, false)
	r, _, err := startRemote(t, config.MCPServer{Alias: "gh", URL: rs.URL, Tools: []string{"search", "slow", "drop"}, CallTimeoutSec: 1})
	if err != nil {
		t.Fatal(err)
	}
	before := rs.requests.Load()
	if _, _, ok := r.Call(context.Background(), "gh__fabricated", `{}`); ok {
		t.Error("a non-allowlisted tool was dispatched")
	}
	if _, _, ok := r.Call(context.Background(), "gh__union", `{}`); ok {
		t.Error("a discovered but non-allowlisted tool was dispatched")
	}
	if rs.requests.Load() != before {
		t.Error("a denied call reached the server")
	}
	got, isErr, _ := r.Call(context.Background(), "gh__slow", `{}`)
	if !isErr || !strings.Contains(got, "timed out after 1s") {
		t.Fatalf("slow = %q", got)
	}
	if got, isErr, _ := r.Call(context.Background(), "gh__search", `{"q":"z"}`); isErr || got != "found z" {
		t.Fatalf("call after timeout = %q, isErr %v", got, isErr)
	}
	got, isErr, _ = r.Call(context.Background(), "gh__drop", `{}`)
	if !isErr || !strings.Contains(got, "call failed") {
		t.Fatalf("dropped call = %q, isErr %v", got, isErr)
	}
	afterDrop := rs.requests.Load()
	got, isErr, _ = r.Call(context.Background(), "gh__search", `{"q":"z"}`)
	if !isErr || !strings.Contains(got, "unavailable after an earlier failure") {
		t.Errorf("call after drop = %q, isErr %v", got, isErr)
	}
	if rs.requests.Load() != afterDrop {
		t.Error("a call after a dropped connection contacted the server (or the dropped call was replayed)")
	}
	done := make(chan struct{})
	go func() { r.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not return within 10s")
	}
}
