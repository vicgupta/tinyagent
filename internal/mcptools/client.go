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
	reserved := map[string]bool{}
	for _, s := range []string{"read_file", "write_file", "edit_file", "list_files", "grep", "bash", "start_process", "read_process", "stop_process"} {
		reserved[s] = true
	}
	aliases := map[string]bool{}
	schemaBytes := 0
	fail := func(err error) (*Registry, error) { r.Close(); return nil, err }
	for _, def := range cfg {
		if !validName(def.Alias) || aliases[def.Alias] {
			return fail(fmt.Errorf("mcp server %q: alias must be unique and contain only letters, digits, _ or -", def.Alias))
		}
		aliases[def.Alias] = true
		if def.Command == "" || len(def.Tools) == 0 {
			return fail(fmt.Errorf("mcp server %q: command and a nonempty tools allowlist are required", def.Alias))
		}
		if def.DiscoveryTimeoutSec == 0 {
			def.DiscoveryTimeoutSec = 15
		}
		if def.CallTimeoutSec == 0 {
			def.CallTimeoutSec = 60
		}
		if def.DiscoveryTimeoutSec < 1 || def.DiscoveryTimeoutSec > 300 || def.CallTimeoutSec < 1 || def.CallTimeoutSec > 600 {
			return fail(fmt.Errorf("mcp server %q: timeouts must be between 1 and 300 seconds (discovery) and 1 and 600 seconds (call)", def.Alias))
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
		seenEnv := map[string]bool{}
		for _, k := range def.Env {
			if k == "" || strings.ContainsAny(k, "=\x00") || seenEnv[k] {
				return fail(fmt.Errorf("mcp server %q: invalid or duplicate forwarded environment variable name", def.Alias))
			}
			seenEnv[k] = true
			v, ok := os.LookupEnv(k)
			if !ok {
				return fail(fmt.Errorf("mcp server %q: forwarded environment variable %s is not set", def.Alias, k))
			}
			env = append(env, k+"="+v)
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
			return fail(fmt.Errorf("mcp server %q: connect failed: %w", def.Alias, err))
		}
		r.sessions = append(r.sessions, sess)
		sv := &server{alias: def.Alias, session: sess, timeout: time.Duration(def.CallTimeoutSec) * time.Second}
		selected := map[string]bool{}
		for _, name := range def.Tools {
			if name == "" || selected[name] {
				return fail(fmt.Errorf("mcp server %q: tool allowlist contains an empty or duplicate name", def.Alias))
			}
			selected[name] = true
		}
		found := map[string]bool{}
		cursor := ""
		cursors := map[string]bool{}
		count := 0
		for page := 0; page < maxPages; page++ {
			res, err := sess.ListTools(dctx, &mcp.ListToolsParams{Cursor: cursor})
			if err != nil {
				cancel()
				return fail(fmt.Errorf("mcp server %q: tool discovery failed: %w", def.Alias, err))
			}
			count += len(res.Tools)
			if count > maxTools {
				cancel()
				return fail(fmt.Errorf("mcp server %q: discovery exceeds %d tools", def.Alias, maxTools))
			}
			for _, t := range res.Tools {
				if t == nil {
					continue
				}
				if !selected[t.Name] {
					continue
				}
				found[t.Name] = true
				name := def.Alias + "__" + t.Name
				if !validName(name) || reserved[name] {
					cancel()
					return fail(fmt.Errorf("mcp server %q: external tool name %q is invalid or reserved", def.Alias, t.Name))
				}
				if _, ok := r.tools[name]; ok {
					cancel()
					return fail(fmt.Errorf("mcp server %q: external tool name collision %q", def.Alias, name))
				}
				params, err := schema(t.InputSchema)
				if err != nil {
					cancel()
					return fail(fmt.Errorf("mcp server %q tool %q: unsupported input schema: %w", def.Alias, t.Name, err))
				}
				encodedSchema, _ := json.Marshal(params)
				schemaBytes += len(encodedSchema)
				if schemaBytes > maxCatalogSchemaBytes {
					cancel()
					return fail(fmt.Errorf("mcp catalog exceeds %d schema bytes", maxCatalogSchemaBytes))
				}
				r.tools[name] = Tool{server: sv, original: t.Name}
				desc := t.Description
				if desc == "" {
					desc = "MCP tool from configured server " + def.Alias
				}
				r.Specs = append(r.Specs, model.ToolSpec{Type: "function", Function: model.FunctionSpec{Name: name, Description: desc, Parameters: params}})
				if len(r.Specs) > maxTools {
					cancel()
					return fail(fmt.Errorf("mcp catalog exceeds %d selected tools", maxTools))
				}
			}
			if res.NextCursor == "" {
				break
			}
			if cursors[res.NextCursor] {
				cancel()
				return fail(fmt.Errorf("mcp server %q: tool discovery pagination loop", def.Alias))
			}
			cursors[res.NextCursor] = true
			cursor = res.NextCursor
			if page == maxPages-1 {
				cancel()
				return fail(fmt.Errorf("mcp server %q: discovery exceeds %d pages", def.Alias, maxPages))
			}
		}
		cancel()
		for name := range selected {
			if !found[name] {
				return fail(fmt.Errorf("mcp server %q: selected tool %q was not discovered", def.Alias, name))
			}
		}
		fmt.Fprintf(stderr, "mcp: enabled server %s (%d selected tools)\n", def.Alias, len(selected))
	}
	if len(r.Specs) == 0 {
		return fail(errors.New("mcp: configured servers produced no tools"))
	}
	return r, nil
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
func (r *Registry) Call(ctx context.Context, name, raw string) (string, bool) {
	t, ok := r.tools[name]
	if !ok {
		return "", false
	}
	if t.server.failed {
		return "error: MCP server " + t.server.alias + " connection is unavailable after an earlier failure", true
	}
	var args map[string]any
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&args); err != nil || args == nil {
		return "error: invalid arguments JSON object", true
	}
	if decoder.Decode(new(any)) != io.EOF {
		return "error: invalid arguments JSON object", true
	}
	cctx, cancel := context.WithTimeout(ctx, t.server.timeout)
	defer cancel()
	res, err := t.server.session.CallTool(cctx, &mcp.CallToolParams{Name: t.original, Arguments: args})
	if err != nil {
		t.server.failed = true
		return truncate("error: MCP server " + t.server.alias + " call failed: " + err.Error()), true
	}
	var out strings.Builder
	for _, c := range res.Content {
		switch v := c.(type) {
		case *mcp.TextContent:
			if out.Len() > 0 {
				out.WriteByte('\n')
			}
			out.WriteString(v.Text)
		default:
			if out.Len() > 0 {
				out.WriteByte('\n')
			}
			out.WriteString("[omitted unsupported MCP content]")
		}
	}
	if res.StructuredContent != nil {
		b, _ := json.Marshal(res.StructuredContent)
		if out.Len() > 0 {
			out.WriteByte('\n')
		}
		out.WriteString("structured JSON: ")
		out.Write(b)
	}
	if res.NeedsInput() {
		return "error: MCP tool requested unsupported interactive input", true
	}
	text := out.String()
	if res.IsError {
		text = "tool error: " + text
	}
	if text == "" {
		text = "[MCP tool returned no text or structured JSON]"
	}
	return truncate(text), true
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
		case "type", "description", "properties", "required", "items", "additionalProperties", "enum", "title", "default", "minimum", "maximum", "minLength", "maxLength", "minItems", "maxItems":
		default:
			return fmt.Errorf("keyword %q is unsupported", k)
		}
	}
	t, ok := m["type"].(string)
	if !ok {
		return errors.New("every schema must declare a type")
	}
	switch t {
	case "object", "array", "string", "number", "integer", "boolean", "null":
	default:
		return fmt.Errorf("type %q is unsupported", t)
	}
	if p, ok := m["properties"]; ok {
		props, yes := p.(map[string]any)
		if !yes || t != "object" {
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
		if !yes || t != "array" {
			return errors.New("items requires array type")
		}
		if err := checkSchema(item, depth+1); err != nil {
			return err
		}
	}
	if a, ok := m["additionalProperties"]; ok {
		if _, yes := a.(bool); !yes || t != "object" {
			return errors.New("additionalProperties must be boolean")
		}
	}
	if req, ok := m["required"]; ok {
		arr, yes := req.([]any)
		if !yes || t != "object" {
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
