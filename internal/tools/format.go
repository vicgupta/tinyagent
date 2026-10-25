package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/vicgupta/tinyagent/internal/model"
)

// maxFormatErrorChars caps the correction message sent for a format error.
const maxFormatErrorChars = 500

// FormatError reports why call is not a usable tool call, or "" when it is
// well formed. A format error is a call whose arguments are not valid JSON or
// not a JSON object, whose name is not in the catalog, or whose arguments miss
// a required field or give a field the wrong JSON type. Failures that happen
// while a well-formed call runs are not format errors.
//
// In read-only mode a built-in tool outside the read-only set is not a format
// error: it keeps its read-only denial.
func (e *Executor) FormatError(call model.ToolCall) string {
	name := call.Function.Name
	var spec *model.ToolSpec
	catalog := e.Catalog()
	for i := range catalog {
		if catalog[i].Function.Name == name {
			spec = &catalog[i]
			break
		}
	}
	if spec == nil {
		if e.ReadOnly && isBuiltin(name) {
			return ""
		}
		return fmt.Sprintf("unknown tool %q", name)
	}
	raw := strings.TrimSpace(call.Function.Arguments)
	if raw == "" {
		raw = "{}"
	}
	var v any
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil || dec.More() {
		if err == nil {
			err = fmt.Errorf("trailing data after the JSON value")
		}
		return fmt.Sprintf("invalid arguments JSON for %s: %v", name, err)
	}
	args, ok := v.(map[string]any)
	if !ok {
		return fmt.Sprintf("arguments for %s must be a JSON object", name)
	}
	params := spec.Function.Parameters
	for _, field := range requiredFields(params) {
		if val, ok := args[field]; !ok || val == nil {
			return fmt.Sprintf("%s is missing required field %q", name, field)
		}
	}
	properties, _ := params["properties"].(map[string]any)
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		prop, _ := properties[k].(map[string]any)
		typ, _ := prop["type"].(string)
		if typ == "" || args[k] == nil {
			continue
		}
		if !hasType(args[k], typ) {
			return fmt.Sprintf("%s field %q must be of type %s", name, k, typ)
		}
	}
	return ""
}

// FormatCorrection is the tool result sent to the model for a format error.
func (e *Executor) FormatCorrection(reason string) string {
	names := make([]string, 0)
	for _, s := range e.Catalog() {
		names = append(names, s.Function.Name)
	}
	msg := fmt.Sprintf("format error: %s. Call one of: %s with arguments matching its schema.", reason, strings.Join(names, ", "))
	if len(msg) > maxFormatErrorChars {
		msg = msg[:maxFormatErrorChars-3] + "..."
	}
	return msg
}

func isBuiltin(name string) bool {
	if name == ApplyPatchToolName {
		return true
	}
	for _, s := range Specs() {
		if s.Function.Name == name {
			return true
		}
	}
	return false
}

func requiredFields(params map[string]any) []string {
	var out []string
	switch r := params["required"].(type) {
	case []string:
		out = r
	case []any:
		for _, f := range r {
			if s, ok := f.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// hasType reports whether v, decoded with UseNumber, has JSON Schema type
// typ. Integers may also be sent as numeric strings, which the built-in tools
// have always accepted.
func hasType(v any, typ string) bool {
	switch typ {
	case "string":
		_, ok := v.(string)
		return ok
	case "integer":
		switch n := v.(type) {
		case json.Number:
			_, err := strconv.Atoi(n.String())
			return err == nil
		case string:
			_, err := strconv.Atoi(n)
			return err == nil
		}
		return false
	case "number":
		_, ok := v.(json.Number)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	}
	return true
}
