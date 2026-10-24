// Package mcptools connects explicitly configured local MCP servers.
package mcptools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/model"
)

const (
	maxServers            = 8
	maxPages              = 100
	maxTools              = 256
	maxSchemaBytes        = 64 << 10
	maxCatalogSchemaBytes = 1 << 20
	maxResultBytes        = 256 << 10
)

var safeName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func validName(s string) bool { return len(s) <= 64 && safeName.MatchString(s) }

type server struct {
	alias   string
	session *mcp.ClientSession
	timeout time.Duration
	failed  bool
}
type Tool struct {
	server   *server
	original string
}
type Registry struct {
	Specs    []model.ToolSpec
	tools    map[string]Tool
	sessions []*mcp.ClientSession
	once     sync.Once
}

// Start validates and connects all configured servers, building a fixed catalog.
func Start(ctx context.Context, root string, cfg []config.MCPServer, stderr io.Writer) (*Registry, error) {
	r := &Registry{tools: map[string]Tool{}}
	if len(cfg) == 0 {
		return r, nil
	}
	if len(cfg) > maxServers {
		return nil, fmt.Errorf("mcp: at most %d servers are allowed", maxServers)
	}
	if err := validateDefinitions(cfg); err != nil {
		return nil, err
	}
	schemaBytes := 0
	for _, def := range cfg {
		if err := r.start(ctx, root, def, stderr, &schemaBytes); err != nil {
			r.Close()
			return nil, err
		}
		fmt.Fprintf(stderr, "mcp: enabled server %s (%d selected tools)\n", def.Alias, len(def.Tools))
	}
	if len(r.Specs) == 0 {
		r.Close()
		return nil, errors.New("mcp: configured servers produced no tools")
	}
	return r, nil
}

// start launches one validated server and adds its allowlisted tools to the
// catalog. schemaBytes accumulates the catalog's schema size across servers.
func (r *Registry) start(ctx context.Context, root string, def config.MCPServer, stderr io.Writer, schemaBytes *int) error {
	if def.DiscoveryTimeoutSec == 0 {
		def.DiscoveryTimeoutSec = 15
	}
	if def.CallTimeoutSec == 0 {
		def.CallTimeoutSec = 60
	}
	env := make([]string, 0, len(def.Env)+2)
	if p := os.Getenv("PATH"); p != "" {
		env = append(env, "PATH="+p)
	}
	if runtime.GOOS == "windows" {
		for _, k := range []string{"SystemRoot", "WINDIR", "USERPROFILE", "TEMP", "TMP"} {
			if v, ok := os.LookupEnv(k); ok {
				env = append(env, k+"="+v)
			}
		}
	}
	for _, k := range def.Env {
		env = append(env, k+"="+os.Getenv(k)) // presence checked by validateDefinitions
	}
	cmd := exec.Command(def.Command, def.Args...)
	cmd.Dir = root
	cmd.Env = env
	cmd.Stderr = &limitedWriter{w: stderr, n: 32 << 10}
	client := mcp.NewClient(&mcp.Implementation{Name: "tinyagent", Version: "1"}, nil)
	dctx, cancel := context.WithTimeout(ctx, time.Duration(def.DiscoveryTimeoutSec)*time.Second)
	defer cancel()
	sess, err := client.Connect(dctx, &mcp.CommandTransport{Command: cmd, TerminateDuration: 2 * time.Second}, nil)
	if err != nil {
		return fmt.Errorf("mcp server %q: connect failed: %w", def.Alias, err)
	}
	r.sessions = append(r.sessions, sess)
	sv := &server{alias: def.Alias, session: sess, timeout: time.Duration(def.CallTimeoutSec) * time.Second}
	selected := map[string]bool{}
	for _, name := range def.Tools {
		selected[name] = true
	}
	found := map[string]bool{}
	cursor := ""
	cursors := map[string]bool{}
	count := 0
	for page := 0; ; page++ {
		if page == maxPages {
			return fmt.Errorf("mcp server %q: discovery exceeds %d pages", def.Alias, maxPages)
		}
		res, err := sess.ListTools(dctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return fmt.Errorf("mcp server %q: tool discovery failed: %w", def.Alias, err)
		}
		count += len(res.Tools)
		if count > maxTools {
			return fmt.Errorf("mcp server %q: discovery exceeds %d tools", def.Alias, maxTools)
		}
		for _, t := range res.Tools {
			if t == nil || !selected[t.Name] {
				continue
			}
			found[t.Name] = true
			// The alias__ prefix keeps names clear of the built-in tools.
			name := def.Alias + "__" + t.Name
			if !validName(name) {
				return fmt.Errorf("mcp server %q: external tool name %q is invalid", def.Alias, t.Name)
			}
			if _, ok := r.tools[name]; ok {
				return fmt.Errorf("mcp server %q: external tool name collision %q", def.Alias, name)
			}
			params, err := schema(t.InputSchema)
			if err != nil {
				return fmt.Errorf("mcp server %q tool %q: unsupported input schema: %w", def.Alias, t.Name, err)
			}
			encodedSchema, _ := json.Marshal(params)
			*schemaBytes += len(encodedSchema)
			if *schemaBytes > maxCatalogSchemaBytes {
				return fmt.Errorf("mcp catalog exceeds %d schema bytes", maxCatalogSchemaBytes)
			}
			r.tools[name] = Tool{server: sv, original: t.Name}
			desc := t.Description
			if desc == "" {
				desc = "MCP tool from configured server " + def.Alias
			}
			r.Specs = append(r.Specs, model.ToolSpec{Type: "function", Function: model.FunctionSpec{Name: name, Description: desc, Parameters: params}})
			if len(r.Specs) > maxTools {
				return fmt.Errorf("mcp catalog exceeds %d selected tools", maxTools)
			}
		}
		if res.NextCursor == "" {
			break
		}
		if cursors[res.NextCursor] {
			return fmt.Errorf("mcp server %q: tool discovery pagination loop", def.Alias)
		}
		cursors[res.NextCursor] = true
		cursor = res.NextCursor
	}
	for _, name := range def.Tools {
		if !found[name] {
			return fmt.Errorf("mcp server %q: selected tool %q was not discovered", def.Alias, name)
		}
	}
	return nil
}

func validateDefinitions(cfg []config.MCPServer) error {
	aliases := map[string]bool{}
	for _, def := range cfg {
		if !validName(def.Alias) || aliases[def.Alias] {
			return fmt.Errorf("mcp server %q: alias must be unique and contain only letters, digits, _ or -", def.Alias)
		}
		aliases[def.Alias] = true
		if strings.TrimSpace(def.Command) == "" || len(def.Tools) == 0 {
			return fmt.Errorf("mcp server %q: command and a nonempty tools allowlist are required", def.Alias)
		}
		if def.DiscoveryTimeoutSec < 0 || def.DiscoveryTimeoutSec > 300 || def.CallTimeoutSec < 0 || def.CallTimeoutSec > 600 {
			return fmt.Errorf("mcp server %q: timeouts must be between 1 and 300 seconds (discovery) and 1 and 600 seconds (call)", def.Alias)
		}
		tools := map[string]bool{}
		for _, name := range def.Tools {
			if name == "" || tools[name] {
				return fmt.Errorf("mcp server %q: tool allowlist contains an empty or duplicate name", def.Alias)
			}
			tools[name] = true
		}
		env := map[string]bool{}
		for _, key := range def.Env {
			if key == "" || strings.ContainsAny(key, "=\x00") || env[key] {
				return fmt.Errorf("mcp server %q: invalid or duplicate forwarded environment variable name", def.Alias)
			}
			env[key] = true
			if _, ok := os.LookupEnv(key); !ok {
				return fmt.Errorf("mcp server %q: forwarded environment variable %s is not set", def.Alias, key)
			}
		}
	}
	return nil
}

// Call executes only a catalogued tool, preserving the original JSON values.
// ok is false when name is not catalogued; isErr marks a failed call.
func (r *Registry) Call(ctx context.Context, name, raw string) (out string, isErr, ok bool) {
	t, ok := r.tools[name]
	if !ok {
		return "", false, false
	}
	if t.server.failed {
		return "error: MCP server " + t.server.alias + " connection is unavailable after an earlier failure", true, true
	}
	var args map[string]any
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&args); err != nil || args == nil {
		return "error: invalid arguments JSON object", true, true
	}
	if decoder.Decode(new(any)) != io.EOF {
		return "error: invalid arguments JSON object", true, true
	}
	cctx, cancel := context.WithTimeout(ctx, t.server.timeout)
	defer cancel()
	res, err := t.server.session.CallTool(cctx, &mcp.CallToolParams{Name: t.original, Arguments: args})
	if err != nil {
		// A timeout or interrupt cancels only this request; the session stays
		// usable. Any other error means the connection itself is suspect.
		if cctx.Err() != nil {
			if ctx.Err() == nil {
				return fmt.Sprintf("error: MCP tool %s timed out after %s", name, t.server.timeout), true, true
			}
			return "error: MCP tool " + name + " was interrupted: " + ctx.Err().Error(), true, true
		}
		t.server.failed = true
		return truncate("error: MCP server " + t.server.alias + " call failed: " + err.Error()), true, true
	}
	var b strings.Builder
	for _, c := range res.Content {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		if v, text := c.(*mcp.TextContent); text {
			b.WriteString(v.Text)
		} else {
			b.WriteString("[omitted unsupported MCP content]")
		}
	}
	if res.StructuredContent != nil {
		data, _ := json.Marshal(res.StructuredContent)
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("structured JSON: ")
		b.Write(data)
	}
	if res.NeedsInput() {
		return "error: MCP tool requested unsupported interactive input", true, true
	}
	text := b.String()
	if res.IsError {
		text = "tool error: " + text
	}
	if text == "" {
		text = "[MCP tool returned no text or structured JSON]"
	}
	return truncate(text), res.IsError, true
}

func (r *Registry) Close() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		for i := len(r.sessions) - 1; i >= 0; i-- {
			_ = r.sessions[i].Close()
		}
	})
}
func truncate(s string) string {
	if len(s) <= maxResultBytes {
		return s
	}
	return s[:maxResultBytes] + "\n[truncated]"
}

type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	total := len(p)
	if l.n <= 0 {
		return total, nil
	}
	if len(p) > l.n {
		p = p[:l.n]
	}
	l.n -= len(p)
	if l.w == nil {
		return total, nil
	}
	_, err := l.w.Write(p)
	if err != nil {
		return 0, err
	}
	return total, nil
}

func schema(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(b) > maxSchemaBytes {
		return nil, fmt.Errorf("schema exceeds %d bytes", maxSchemaBytes)
	}
	var m map[string]any
	if err = json.Unmarshal(b, &m); err != nil || m == nil {
		return nil, errors.New("schema must be an object")
	}
	// SDK-generated schemas commonly declare their draft; it is metadata
	// only, and providers don't need it.
	delete(m, "$schema")
	if err = checkSchema(m, 0); err != nil {
		return nil, err
	}
	if m["type"] != "object" {
		return nil, errors.New("root schema must have type object")
	}
	return m, nil
}
func checkSchema(m map[string]any, depth int) error {
	if depth > 16 {
		return errors.New("schema nesting exceeds 16")
	}
	for k := range m {
		switch k {
		case "type", "description", "properties", "required", "items", "additionalProperties", "enum", "const", "title", "default", "examples",
			"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf",
			"minLength", "maxLength", "pattern", "format", "minItems", "maxItems", "uniqueItems", "minProperties", "maxProperties":
		default:
			return fmt.Errorf("keyword %q is unsupported", k)
		}
	}
	types, err := schemaTypes(m["type"])
	if err != nil {
		return err
	}
	if p, ok := m["properties"]; ok {
		props, yes := p.(map[string]any)
		if !yes || !types["object"] {
			return errors.New("properties requires object type")
		}
		for _, v := range props {
			child, yes := v.(map[string]any)
			if !yes {
				return errors.New("property schema must be an object")
			}
			if err := checkSchema(child, depth+1); err != nil {
				return err
			}
		}
	}
	if i, ok := m["items"]; ok {
		item, yes := i.(map[string]any)
		if !yes || !types["array"] {
			return errors.New("items requires array type")
		}
		if err := checkSchema(item, depth+1); err != nil {
			return err
		}
	}
	if a, ok := m["additionalProperties"]; ok {
		if _, yes := a.(bool); !yes || !types["object"] {
			return errors.New("additionalProperties must be boolean")
		}
	}
	if req, ok := m["required"]; ok {
		arr, yes := req.([]any)
		if !yes || !types["object"] {
			return errors.New("required must be an array")
		}
		for _, v := range arr {
			if _, yes := v.(string); !yes {
				return errors.New("required entries must be strings")
			}
		}
	}
	return nil
}

// schemaTypes returns the set of types a schema declares: one type name, or
// an array of them such as ["string", "null"] for a nullable value.
func schemaTypes(v any) (map[string]bool, error) {
	var names []any
	switch t := v.(type) {
	case string:
		names = []any{t}
	case []any:
		names = t
	}
	if len(names) == 0 {
		return nil, errors.New("every schema must declare a type")
	}
	types := map[string]bool{}
	for _, n := range names {
		name, _ := n.(string)
		switch name {
		case "object", "array", "string", "number", "integer", "boolean", "null":
			types[name] = true
		default:
			return nil, fmt.Errorf("type %v is unsupported", n)
		}
	}
	return types, nil
}
