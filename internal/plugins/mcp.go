package plugins

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vicgupta/tinyagent/internal/config"
)

// MCPSchemaV1 is the only mcp.json $schema value recognized.
const MCPSchemaV1 = "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json"

// MCPFile is the MCP configuration's name at the plugin root.
const MCPFile = "mcp.json"

// ErrUnsupported marks an mcp.json entry whose transport tinyagent can't
// run (streamable-http and sse: remote MCP is not supported).
var ErrUnsupported = errors.New("unsupported transport")

// MCPEntry is one mcp.json server. Err is set when the entry can't be
// started; the other fields hold the stdio definition before expansion.
type MCPEntry struct {
	Name    string
	Type    string
	Err     error
	Command string
	Args    []string
	Env     map[string]string
	Cwd     string
}

// MCPConfig is a plugin's parsed mcp.json. Present is false when the file
// doesn't exist; Err set means MCP is disabled for the plugin.
type MCPConfig struct {
	Present bool
	Err     error
	Digest  string // SHA-256 of the file, hex
	Entries []MCPEntry
}

// Entry returns the named entry.
func (c *MCPConfig) Entry(name string) (MCPEntry, bool) {
	for _, e := range c.Entries {
		if e.Name == name {
			return e, true
		}
	}
	return MCPEntry{}, false
}

// LoadMCP reads <root>/mcp.json, where root is the resolved plugin root.
func LoadMCP(root string) *MCPConfig {
	c := &MCPConfig{}
	p := filepath.Join(root, MCPFile)
	if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
		return c
	}
	c.Present = true
	data, err := readInside(root, p)
	if err != nil {
		c.Err = err
		return c
	}
	c.Digest = digest(data)
	c.Entries, c.Err = parseMCP(root, data)
	return c
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func readInside(root, p string) ([]byte, error) {
	path, err := ResolveInside(root, p)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(p), err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(p), err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", filepath.Base(p))
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxManifestBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxManifestBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", filepath.Base(p), MaxManifestBytes)
	}
	return data, nil
}

func objectFields(data []byte) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	var obj map[string]json.RawMessage
	if err := dec.Decode(&obj); err != nil {
		return nil, fmt.Errorf("invalid JSON object: %w", err)
	}
	if obj == nil {
		return nil, errors.New("must be a JSON object")
	}
	if dec.More() {
		return nil, errors.New("trailing data after JSON object")
	}
	return obj, nil
}

func parseMCP(root string, data []byte) ([]MCPEntry, error) {
	obj, err := objectFields(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", MCPFile, err)
	}
	for k := range obj {
		if k != "$schema" && k != "mcpServers" {
			return nil, fmt.Errorf("%s: unknown top-level field %q", MCPFile, k)
		}
	}
	var schema string
	if v, ok := obj["$schema"]; !ok || json.Unmarshal(v, &schema) != nil || schema != MCPSchemaV1 {
		return nil, fmt.Errorf("%s: missing or unsupported $schema (want %s)", MCPFile, MCPSchemaV1)
	}
	raw, ok := obj["mcpServers"]
	if !ok {
		return nil, fmt.Errorf("%s: missing mcpServers", MCPFile)
	}
	servers, err := objectFields(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: mcpServers %w", MCPFile, err)
	}
	names := make([]string, 0, len(servers))
	for n := range servers {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]MCPEntry, 0, len(names))
	for _, n := range names {
		e := MCPEntry{Name: n}
		e.Err = parseEntry(root, servers[n], &e)
		out = append(out, e)
	}
	return out, nil
}

func parseEntry(root string, raw json.RawMessage, e *MCPEntry) error {
	obj, err := objectFields(raw)
	if err != nil {
		return err
	}
	if v, ok := obj["type"]; !ok || json.Unmarshal(v, &e.Type) != nil || e.Type == "" {
		return errors.New("missing or invalid type")
	}
	switch e.Type {
	case "streamable-http", "sse":
		return ErrUnsupported
	case "stdio":
	default:
		return fmt.Errorf("unknown type %q", e.Type)
	}
	for k, v := range obj {
		var err error
		switch k {
		case "type":
		case "command":
			err = json.Unmarshal(v, &e.Command)
		case "args":
			err = json.Unmarshal(v, &e.Args)
		case "env":
			err = json.Unmarshal(v, &e.Env)
		case "cwd":
			err = json.Unmarshal(v, &e.Cwd)
		default:
			return fmt.Errorf("unknown field %q", k)
		}
		if err != nil {
			return fmt.Errorf("invalid %s", k)
		}
	}
	switch c := e.Command; {
	case c == "":
		return errors.New("command is required")
	case strings.Contains(c, "${"):
		return errors.New("placeholders are not expanded in command")
	case strings.HasPrefix(c, "./"):
		p, err := ResolveInside(root, filepath.Join(root, filepath.FromSlash(c)))
		if err != nil {
			return fmt.Errorf("command %s: %w", c, err)
		}
		if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
			return fmt.Errorf("command %s is not a file in the plugin", c)
		}
		e.Command = p
	case strings.ContainsAny(c, `/\`):
		return fmt.Errorf("command %q must be a bare name or a ./ path inside the plugin", c)
	}
	for k := range e.Env {
		if k == "" || strings.ContainsAny(k, "=\x00") {
			return fmt.Errorf("invalid env name %q", k)
		}
	}
	return checkCwd(e.Cwd)
}

const (
	rootVar = "${PLUGIN_ROOT}"
	dataVar = "${PLUGIN_DATA}"
)

// checkCwd validates an explicit cwd's form; containment of the expanded
// path is checked by resolveCwd.
func checkCwd(cwd string) error {
	rest, ok := "", false
	switch {
	case cwd == "":
		return nil
	case strings.HasPrefix(cwd, rootVar):
		rest, ok = strings.TrimPrefix(cwd, rootVar), true
	case strings.HasPrefix(cwd, dataVar):
		rest, ok = strings.TrimPrefix(cwd, dataVar), true
	case strings.HasPrefix(cwd, "./"):
		rest, ok = cwd[1:], true
	}
	if !ok || (rest != "" && !strings.HasPrefix(rest, "/")) || strings.Contains(rest, "${") {
		return fmt.Errorf("cwd %q must start with ./, ${PLUGIN_ROOT} or ${PLUGIN_DATA}", cwd)
	}
	for _, part := range strings.Split(rest, "/") {
		if part == ".." {
			return fmt.Errorf("cwd %q leaves its directory", cwd)
		}
	}
	return nil
}

func resolveCwd(cwd, root, data string) (string, error) {
	base, rest := root, ""
	switch {
	case cwd == "":
		return root, nil
	case strings.HasPrefix(cwd, dataVar):
		base, rest = data, strings.TrimPrefix(cwd, dataVar)
	case strings.HasPrefix(cwd, rootVar):
		rest = strings.TrimPrefix(cwd, rootVar)
	default:
		rest = cwd[1:]
	}
	p := filepath.Join(base, filepath.FromSlash(rest))
	if base == root {
		r, err := ResolveInside(root, p)
		if err != nil {
			return "", fmt.Errorf("cwd %q: %w", cwd, err)
		}
		p = r
	}
	return p, nil
}

// DataDir returns the plugin's persistent PLUGIN_DATA directory, under the
// user config directory. It is not created here.
func DataDir(name string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tinyagent", "plugin-data", name), nil
}

// Server maps an authorized stdio entry to a tinyagent MCP server
// definition: placeholders expanded once in args, env values and cwd, the
// mcp.json env set, and PLUGIN_ROOT and PLUGIN_DATA set last.
func Server(e MCPEntry, auth config.PluginServer, root, data string) (config.MCPServer, error) {
	if e.Err != nil {
		return config.MCPServer{}, e.Err
	}
	rep := strings.NewReplacer(rootVar, root, dataVar, data)
	dir, err := resolveCwd(e.Cwd, root, data)
	if err != nil {
		return config.MCPServer{}, err
	}
	def := config.MCPServer{
		Alias: auth.Alias, Command: e.Command, Tools: auth.Tools, Resources: auth.Resources,
		DiscoveryTimeoutSec: auth.DiscoveryTimeoutSec, CallTimeoutSec: auth.CallTimeoutSec, Dir: dir,
	}
	for _, a := range e.Args {
		def.Args = append(def.Args, rep.Replace(a))
	}
	keys := make([]string, 0, len(e.Env))
	for k := range e.Env {
		if k != "PLUGIN_ROOT" && k != "PLUGIN_DATA" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		def.SetEnv = append(def.SetEnv, k+"="+rep.Replace(e.Env[k]))
	}
	def.SetEnv = append(def.SetEnv, "PLUGIN_ROOT="+root, "PLUGIN_DATA="+data)
	return def, nil
}
