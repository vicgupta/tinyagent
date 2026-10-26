package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/outschema"
	"github.com/vicgupta/tinyagent/internal/skills"
)

// CommandTool is a checked command_tools entry: a model-callable tool that
// runs a fixed argv, filled in with schema-checked arguments, without a
// shell, in the run's environment.
type CommandTool struct {
	Name        string
	Description string
	Schema      *outschema.Schema
	Argv        []string // template; placeholders are whole elements
	Timeout     time.Duration
	Dir         string // absolute working directory inside Root
}

var (
	commandToolName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
	placeholder     = regexp.MustCompile(`^\{\{([A-Za-z0-9_-]+)\}\}$`)
)

// ReservedToolNames are the built-in tool names, including opt-in ones,
// that a command tool may not use.
func ReservedToolNames() []string {
	var names []string
	for _, s := range Specs() {
		names = append(names, s.Function.Name)
	}
	return append(names, PlanToolName, ViewImageToolName, ApplyPatchToolName, DelegateToolName,
		WebFetchToolName, LSPToolName, skills.ToolName, "submit_result")
}

// NewCommandTools checks the command_tools definitions for a run rooted at
// root; bashTimeoutSec is the default timeout. Errors name the tool and key.
func NewCommandTools(defs []config.CommandTool, root string, bashTimeoutSec int) ([]*CommandTool, error) {
	reserved := ReservedToolNames()
	seen := map[string]bool{}
	var out []*CommandTool
	for i, d := range defs {
		where := fmt.Sprintf("command_tools[%d]", i)
		if d.Name != "" {
			where = fmt.Sprintf("command_tools[%d] (%s)", i, d.Name)
		}
		bad := func(format string, a ...any) error {
			return fmt.Errorf("%s: %s", where, fmt.Sprintf(format, a...))
		}
		switch {
		case !commandToolName.MatchString(d.Name):
			return nil, bad("name %q must match %s", d.Name, commandToolName)
		case slices.Contains(reserved, d.Name):
			return nil, bad("name %q is a built-in tool name", d.Name)
		case seen[d.Name]:
			return nil, bad("name %q is used by another command tool", d.Name)
		case strings.TrimSpace(d.Description) == "":
			return nil, bad("description is required")
		case len(d.Argv) == 0:
			return nil, bad("argv must not be empty")
		case d.Argv[0] == "" || placeholder.MatchString(d.Argv[0]):
			return nil, bad("argv[0] must be a literal executable, not a placeholder")
		case d.TimeoutSec < 0 || d.TimeoutSec > maxBashTimeoutSeconds:
			return nil, bad("timeout_seconds must be between 1 and %d, got %d", maxBashTimeoutSeconds, d.TimeoutSec)
		}
		seen[d.Name] = true
		params := d.Parameters
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
		}
		raw, err := json.Marshal(params)
		if err != nil {
			return nil, bad("parameters: %v", err)
		}
		schema, err := outschema.Parse(raw)
		if err != nil {
			return nil, bad("parameters: %v", err)
		}
		props, _ := schema.Params["properties"].(map[string]any)
		for j, el := range d.Argv {
			m := placeholder.FindStringSubmatch(el)
			if m == nil {
				if strings.Contains(el, "{{") {
					return nil, bad("argv[%d] %q: a placeholder must be the whole element", j, el)
				}
				continue
			}
			p, ok := props[m[1]].(map[string]any)
			if !ok {
				return nil, bad("argv[%d] %q names an undeclared property", j, el)
			}
			if err := placeholderType(p); err != nil {
				return nil, bad("argv[%d] %q: %v", j, el, err)
			}
		}
		dir := root
		if d.Dir != "" {
			if dir, err = Resolve(root, d.Dir); err != nil {
				return nil, bad("dir %q must be inside --cwd", d.Dir)
			}
			if !isDir(dir) {
				return nil, bad("dir %q is not a directory", d.Dir)
			}
		}
		timeout := time.Duration(d.TimeoutSec) * time.Second
		if d.TimeoutSec == 0 {
			timeout = time.Duration(bashTimeoutSec) * time.Second
		}
		out = append(out, &CommandTool{Name: d.Name, Description: d.Description, Schema: schema,
			Argv: d.Argv, Timeout: timeout, Dir: dir})
	}
	return out, nil
}

// placeholderType accepts a property usable as a placeholder: a scalar, or
// an array of scalars.
func placeholderType(p map[string]any) error {
	for _, t := range typeNames(p["type"]) {
		switch t {
		case "string", "number", "integer", "boolean", "null":
		case "array":
			item, _ := p["items"].(map[string]any)
			if item == nil {
				return errors.New("an array property needs scalar items")
			}
			for _, it := range typeNames(item["type"]) {
				if it == "array" || it == "object" {
					return errors.New("an array property needs scalar items")
				}
			}
		default:
			return fmt.Errorf("a %s property can't be a placeholder", t)
		}
	}
	return nil
}

func typeNames(t any) []string {
	switch v := t.(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// Spec is the tool definition offered to the model.
func (c *CommandTool) Spec() model.ToolSpec {
	return model.ToolSpec{Type: "function", Function: model.FunctionSpec{
		Name: c.Name, Description: c.Description, Parameters: c.Schema.Params}}
}

// Expand checks raw arguments against the schema and returns the argv to
// run, or the validation problems.
func (c *CommandTool) Expand(raw string) ([]string, []string) {
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	valid, errs := c.Schema.Validate(raw)
	if errs != nil {
		return nil, errs
	}
	dec := json.NewDecoder(strings.NewReader(string(valid)))
	dec.UseNumber()
	var args map[string]any
	if err := dec.Decode(&args); err != nil {
		return nil, []string{"arguments must be a JSON object"}
	}
	var argv []string
	for _, el := range c.Argv {
		m := placeholder.FindStringSubmatch(el)
		if m == nil {
			argv = append(argv, el)
			continue
		}
		switch v := args[m[1]].(type) {
		case []any:
			for _, x := range v {
				if s, ok := scalarArg(x); ok {
					argv = append(argv, s)
				}
			}
		default:
			if s, ok := scalarArg(v); ok {
				argv = append(argv, s)
			}
		}
	}
	return argv, nil
}

// scalarArg renders one argument value as an argv element: strings
// verbatim, numbers and booleans in JSON form. nil (absent or null) is
// dropped.
func scalarArg(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case json.Number:
		return x.String(), true
	case bool:
		return strconv.FormatBool(x), true
	}
	return "", false
}

// commandTool returns the command tool called name, or nil.
func (e *Executor) commandTool(name string) *CommandTool {
	for _, c := range e.CommandTools {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// runCommandTool runs one command tool call in e.Env.
func (e *Executor) runCommandTool(ctx context.Context, c *CommandTool, raw string) (string, error) {
	argv, errs := c.Expand(raw)
	if errs != nil {
		return "", fmt.Errorf("arguments do not match the %s schema; nothing ran:\n- %s", c.Name, strings.Join(errs, "\n- "))
	}
	if e.CommandLog != nil {
		quoted, _ := json.Marshal(argv)
		fmt.Fprintf(e.CommandLog, "command tool %s: %s\n", c.Name, quoted)
	}
	out, err := runStream(ctx, c.Timeout, nil, func(ctx context.Context) *exec.Cmd {
		return environment(e.Env).Exec(ctx, c.Dir, argv)
	})
	if err != nil {
		return "", fmt.Errorf("running %q in the %s environment: %v", argv[0], envName(e.Env), err)
	}
	return out, nil
}

// envName names env for error messages.
func envName(env Environment) string {
	switch v := environment(env).(type) {
	case *Container:
		return "container"
	case Local:
		if v.Sandbox != nil {
			return "sandboxed local"
		}
	}
	return "local"
}
