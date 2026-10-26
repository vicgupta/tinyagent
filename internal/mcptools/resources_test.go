package mcptools

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vicgupta/tinyagent/internal/config"
)

// bigText is larger than the 256 KiB result cap.
var bigText = strings.Repeat("0123456789abcdef", 20<<10)

// addFixtureResources gives the fixture server resources and templates.
func addFixtureResources(s *mcp.Server) {
	text := func(uri, body string) mcp.ResourceHandler {
		return func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: uri, MIMEType: "text/markdown", Text: body}}}, nil
		}
	}
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 92)...)
	add := func(uri string, h mcp.ResourceHandler) {
		s.AddResource(&mcp.Resource{URI: uri, Name: filepath.Base(uri), MIMEType: "text/markdown", Description: "doc " + uri + "\nsecond line"}, h)
	}
	add("docs://guides/a", text("docs://guides/a", "guide A"))
	add("docs://guides/b", text("docs://guides/b", "guide B"))
	add("docs://guides/big", text("docs://guides/big", bigText))
	add("docs://guides/token", text("docs://guides/token", "token=fixture-secret-value-42"))
	add("docs://guides/blob", func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "docs://guides/blob", MIMEType: "application/octet-stream", Blob: []byte("0123456789")}}}, nil
	})
	add("docs://guides/png", func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "docs://guides/png", MIMEType: "image/png", Blob: png}}}, nil
	})
	add("docs://guides/missing", func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return nil, &jsonrpc.Error{Code: -32002, Message: "Resource not found"}
	})
	add("docs://guides/slow", func(ctx context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "docs://guides/slow", Text: "late"}}}, nil
	})
	add("secret://x", func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		// The fixture's stdout is the protocol, so a read is recorded in a
		// file the test can check.
		if p := os.Getenv("TINYAGENT_MCP_FIXTURE_TRACE"); p != "" {
			os.WriteFile(p, []byte("read secret://x"), 0o644)
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "secret://x", Text: "secret"}}}, nil
	})
	s.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "docs://guides/{slug}", Name: "guide"}, text("docs://guides/x", "templated"))
	s.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "other://{id}", Name: "other"}, text("other://x", "other"))
}

func startResources(t *testing.T, patterns ...string) *Registry {
	t.Helper()
	r, err := startFixture(t, config.MCPServer{Resources: patterns, CallTimeoutSec: 1})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestResourcePatterns(t *testing.T) {
	for _, bad := range [][]string{{""}, {"*"}, {"docs://*/x"}, {"docs://a", "docs://a"}} {
		if err := config.ValidateResourcePatterns(bad); err == nil {
			t.Errorf("patterns %q accepted", bad)
		}
	}
	ps := []string{"docs://guides/*", "schema://db/orders"}
	if err := config.ValidateResourcePatterns(ps); err != nil {
		t.Fatal(err)
	}
	for uri, want := range map[string]bool{
		"docs://guides/a": true, "docs://guides/": true, "docs://guides/../secret": true,
		"docs://guide": false, "schema://db/orders": true, "schema://db/orders2": false, "DOCS://guides/a": false,
	} {
		if config.MatchResource(ps, uri) != want {
			t.Errorf("MatchResource(%q) = %v", uri, !want)
		}
	}
	for tmpl, want := range map[string]bool{
		"docs://guides/{slug}": true, "docs://{x}": true, "schema://db/{t}": true, "schema://{t}/x": true,
		"other://{id}": false, "docs://other/{x}": false, "docs://guides/fixed": true,
	} {
		if templateAllowed(ps, tmpl) != want {
			t.Errorf("templateAllowed(%q) = %v", tmpl, !want)
		}
	}
}

func TestResourceOnlyServer(t *testing.T) {
	var stderr bytes.Buffer
	t.Setenv("TINYAGENT_MCP_FIXTURE", "1")
	r, err := Start(context.Background(), t.TempDir(), []config.MCPServer{{Alias: "docs", Command: os.Args[0], Args: []string{"-test.run=^TestMCPFixtureProcess$"}, Env: []string{"TINYAGENT_MCP_FIXTURE"}, Resources: []string{"docs://guides/*", "x://y"}}}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var names []string
	for _, s := range r.Specs {
		names = append(names, s.Function.Name)
	}
	if strings.Join(names, ",") != "list_mcp_resources,read_mcp_resource" {
		t.Errorf("specs = %v", names)
	}
	if !strings.Contains(stderr.String(), "mcp: enabled server docs (0 selected tools, 2 resource patterns)") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestNoResourceToolsWithoutResources(t *testing.T) {
	r, err := startFixture(t, config.MCPServer{Tools: []string{"echo"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Specs) != 1 {
		t.Fatalf("specs = %v", r.Specs)
	}
	if _, _, ok := r.Call(context.Background(), ReadResourceTool, `{"server":"docs","uri":"docs://guides/a"}`); ok {
		t.Error("read_mcp_resource dispatched without a resources allowlist")
	}
}

func TestResourcesNeedServerCapability(t *testing.T) {
	_, err := startFixtureMode(t, "plain", config.MCPServer{Tools: []string{"echo"}, Resources: []string{"docs://*"}})
	var invalid *config.InvalidError
	if err == nil || !strings.Contains(err.Error(), `mcp server "docs": resources configured but the server does not declare the resources capability`) || !errors.As(err, &invalid) {
		t.Fatalf("err = %v", err)
	}
}

func TestEmptyAllowlistsRejected(t *testing.T) {
	_, err := Start(context.Background(), t.TempDir(), []config.MCPServer{{Alias: "docs", Command: "x"}}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), `"docs"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestListResourcesFiltersAndPaginates(t *testing.T) {
	r := startResources(t, "docs://guides/*")
	got, isErr, ok := r.Call(context.Background(), ListResourcesTool, `{}`)
	if !ok || isErr {
		t.Fatalf("list = %q, isErr %v", got, isErr)
	}
	for _, want := range []string{
		`server=docs uri=docs://guides/a name="a" mimeType=text/markdown description="doc docs://guides/asecond line"`,
		"uri=docs://guides/b ", "uri=docs://guides/slow ",
		`template server=docs uriTemplate=docs://guides/{slug} name="guide"`,
		"[2 resources hidden by the allowlist on server docs]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("list lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "secret://x") || strings.Contains(got, "other://") {
		t.Errorf("list shows hidden entries:\n%s", got)
	}

	// A low page limit stops the listing with a cursor that continues it.
	listPageLimit = 1
	defer func() { listPageLimit = maxPages }()
	first, _, _ := r.Call(context.Background(), ListResourcesTool, `{"server":"docs"}`)
	i := strings.Index(first, `cursor "`)
	if i < 0 {
		t.Fatalf("no cursor in %q", first)
	}
	cursor := first[i+len(`cursor "`):]
	cursor = cursor[:strings.IndexByte(cursor, '"')]
	next, isErr, _ := r.Call(context.Background(), ListResourcesTool, `{"server":"docs","cursor":"`+cursor+`"}`)
	if isErr || !strings.Contains(next, "uri=docs://guides/") || strings.Contains(next, "template") {
		t.Fatalf("next page = %q", next)
	}
	if strings.Count(first, "uri=")+strings.Count(next, "uri=") < 3 || next == first {
		t.Errorf("pages overlap or are empty:\n%s\n---\n%s", first, next)
	}
	if got, isErr, _ := r.Call(context.Background(), ListResourcesTool, `{"cursor":"x"}`); !isErr {
		t.Errorf("cursor without server = %q", got)
	}
	if got, isErr, _ := r.Call(context.Background(), ListResourcesTool, `{"server":"nope"}`); !isErr || !strings.Contains(got, "servers with resources: docs") {
		t.Errorf("unknown server = %q", got)
	}
}

func TestReadResource(t *testing.T) {
	trace := filepath.Join(t.TempDir(), "trace")
	t.Setenv("TINYAGENT_MCP_FIXTURE_TRACE", trace)
	t.Setenv("TINYAGENT_MCP_FIXTURE", "1")
	var stderr, log bytes.Buffer
	r, err := Start(context.Background(), t.TempDir(), []config.MCPServer{{Alias: "docs", Command: os.Args[0], Args: []string{"-test.run=^TestMCPFixtureProcess$"}, Env: []string{"TINYAGENT_MCP_FIXTURE", "TINYAGENT_MCP_FIXTURE_TRACE"}, Resources: []string{"docs://guides/*"}, CallTimeoutSec: 1}}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.Log = &log
	read := func(uri string) (string, bool) {
		out, isErr, ok := r.Call(context.Background(), ReadResourceTool, `{"server":"docs","uri":"`+uri+`"}`)
		if !ok {
			t.Fatal("read not dispatched")
		}
		return out, isErr
	}
	if got, isErr := read("docs://guides/a"); isErr || got != "[resource: docs://guides/a, text/markdown]\nguide A" {
		t.Errorf("read a = %q, isErr %v", got, isErr)
	}
	if !strings.Contains(log.String(), "mcp: read docs docs://guides/a (") {
		t.Errorf("verbose log = %q", log.String())
	}
	if got, isErr := read("secret://x"); !isErr || got != "error: resource not allowed for server docs: secret://x" {
		t.Errorf("read secret = %q, isErr %v", got, isErr)
	}
	if _, err := os.Stat(trace); err == nil {
		t.Error("the server received a read for a URI outside the allowlist")
	}
	if got, isErr := read("docs://guides/big"); isErr || len(got) != maxResultBytes+len("\n[truncated]") || !strings.HasSuffix(got, "\n[truncated]") {
		t.Errorf("big read: %d bytes, isErr %v", len(got), isErr)
	}
	got, isErr := read("docs://guides/blob")
	if isErr || got != "[resource: docs://guides/blob, application/octet-stream]\n[omitted MCP resource blob: application/octet-stream, 10 bytes]" {
		t.Errorf("blob read = %q", got)
	}
	got, _ = read("docs://guides/png")
	if !strings.Contains(got, "[omitted MCP resource blob: image/png, 100 bytes]") {
		t.Errorf("png without images = %q", got)
	}
	text, images, isErr, _ := r.CallImages(1000)(context.Background(), ReadResourceTool, `{"server":"docs","uri":"docs://guides/png"}`)
	if isErr || len(images) != 1 || !strings.Contains(text, "[image: image/png, 100 bytes, sha256:") {
		t.Errorf("png with images = %q, %d images", text, len(images))
	}
	if got, isErr := read("docs://guides/missing"); !isErr || !strings.Contains(got, "Resource not found (code -32002)") {
		t.Errorf("missing read = %q, isErr %v", got, isErr)
	}
	if got, isErr := read("docs://guides/slow"); !isErr || !strings.Contains(got, "timed out after 1s") {
		t.Errorf("slow read = %q, isErr %v", got, isErr)
	}
	if got, isErr := read("docs://guides/b"); isErr || !strings.HasSuffix(got, "guide B") {
		t.Errorf("read after errors = %q, isErr %v; want the server still usable", got, isErr)
	}
	if got, isErr, _ := r.Call(context.Background(), ReadResourceTool, `{"server":"docs"}`); !isErr || got != "error: server and uri are required" {
		t.Errorf("missing uri = %q", got)
	}
}
