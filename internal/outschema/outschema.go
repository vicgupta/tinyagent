// Package outschema loads a user-supplied JSON Schema for --output-schema and
// validates a run's structured result against it.
//
// The accepted subset is the one tinyagent accepts for MCP tool input schemas
// (see mcptools.CheckSchema): a root "type": "object", every schema declaring
// a type, and only these keywords: type, description, title, default,
// examples, properties, required, items, additionalProperties (boolean),
// enum, const, minimum, maximum, exclusiveMinimum, exclusiveMaximum,
// multipleOf, minLength, maxLength, pattern, format, minItems, maxItems,
// uniqueItems, minProperties, maxProperties. "$schema" is ignored; "format"
// is an annotation and is not enforced.
package outschema

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/vicgupta/tinyagent/internal/mcptools"
)

// MaxFileBytes caps the schema file size.
const MaxFileBytes = 64 << 10

// maxErrors caps how many validation errors are reported per attempt.
const maxErrors = 10

// Schema is a loaded, checked output schema.
type Schema struct {
	// Params is the schema (minus "$schema"), used as the submit tool's
	// parameters.
	Params map[string]any
	// Digest is "sha256:<hex>" of the canonical JSON of Params.
	Digest string
}

// Load reads and checks the schema at path. Errors name the file and reason.
func Load(path string) (*Schema, error) {
	fail := func(format string, a ...any) error {
		return fmt.Errorf("--output-schema %s: %s", path, fmt.Sprintf(format, a...))
	}
	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fail("file does not exist")
		}
		return nil, fail("%v", err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fail("not a regular file")
	}
	if fi.Size() > MaxFileBytes {
		return nil, fail("file exceeds %d bytes", MaxFileBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fail("%v", err)
	}
	if len(data) > MaxFileBytes {
		return nil, fail("file exceeds %d bytes", MaxFileBytes)
	}
	s, err := Parse(data)
	if err != nil {
		return nil, fail("%v", err)
	}
	return s, nil
}

// Parse checks a schema document.
func Parse(data []byte) (*Schema, error) {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("invalid JSON: %v", err)
	}
	if m, ok := v.(map[string]any); !ok || m["type"] != "object" {
		return nil, errors.New("root schema must have \"type\": \"object\"")
	}
	params, err := mcptools.CheckSchema(v)
	if err != nil {
		return nil, err
	}
	if err := checkPatterns(params); err != nil {
		return nil, err
	}
	canon, _ := json.Marshal(params)
	return &Schema{Params: params, Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(canon))}, nil
}

func checkPatterns(m map[string]any) error {
	if p, ok := m["pattern"]; ok {
		s, yes := p.(string)
		if !yes {
			return errors.New("pattern must be a string")
		}
		if _, err := regexp.Compile(s); err != nil {
			return fmt.Errorf("pattern %q is not a valid Go regular expression", s)
		}
	}
	if props, ok := m["properties"].(map[string]any); ok {
		for _, c := range props {
			if err := checkPatterns(c.(map[string]any)); err != nil {
				return err
			}
		}
	}
	if item, ok := m["items"].(map[string]any); ok {
		return checkPatterns(item)
	}
	return nil
}

// Validate decodes raw JSON arguments and validates them. It returns the
// compact JSON of the object on success, or the validation errors.
func (s *Schema) Validate(raw string) (json.RawMessage, []string) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, []string{"arguments are not valid JSON: " + err.Error()}
	}
	if dec.More() {
		return nil, []string{"arguments are not a single JSON value"}
	}
	var errs []string
	validate(s.Params, v, "$", &errs)
	if len(errs) > 0 {
		if len(errs) > maxErrors {
			errs = append(errs[:maxErrors], fmt.Sprintf("... and %d more errors", len(errs)-maxErrors))
		}
		return nil, errs
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(raw)); err != nil {
		return nil, []string{err.Error()}
	}
	return buf.Bytes(), nil
}

func typeOf(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case json.Number:
		if isInteger(x) {
			return "integer"
		}
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return "unknown"
}

func isInteger(n json.Number) bool {
	r, ok := new(big.Rat).SetString(n.String())
	return ok && r.IsInt()
}

func typeMatches(want string, v any) bool {
	got := typeOf(v)
	return got == want || (want == "number" && got == "integer")
}

func declaredTypes(t any) []string {
	switch x := t.(type) {
	case string:
		return []string{x}
	case []any:
		out := make([]string, 0, len(x))
		for _, n := range x {
			out = append(out, n.(string))
		}
		return out
	}
	return nil
}

func validate(s map[string]any, v any, path string, errs *[]string) {
	add := func(format string, a ...any) {
		*errs = append(*errs, path+": "+fmt.Sprintf(format, a...))
	}
	types := declaredTypes(s["type"])
	ok := false
	for _, t := range types {
		if typeMatches(t, v) {
			ok = true
			break
		}
	}
	if !ok {
		add("expected type %s, got %s", strings.Join(types, " or "), typeOf(v))
		return
	}
	if e, has := s["enum"].([]any); has {
		found := false
		for _, c := range e {
			if equal(c, v) {
				found = true
				break
			}
		}
		if !found {
			add("value %s is not one of the allowed enum values %s", short(v), short(e))
		}
	}
	if c, has := s["const"]; has && !equal(c, v) {
		add("value must equal %s", short(c))
	}
	switch x := v.(type) {
	case json.Number:
		validateNumber(s, x, add)
	case string:
		n := utf8.RuneCountInString(x)
		if m, has := num(s["minLength"]); has && float64(n) < m {
			add("string is shorter than minLength %v", m)
		}
		if m, has := num(s["maxLength"]); has && float64(n) > m {
			add("string is longer than maxLength %v", m)
		}
		if p, has := s["pattern"].(string); has {
			if re, err := regexp.Compile(p); err == nil && !re.MatchString(x) {
				add("string does not match pattern %q", p)
			}
		}
	case []any:
		if m, has := num(s["minItems"]); has && float64(len(x)) < m {
			add("array has fewer than minItems %v", m)
		}
		if m, has := num(s["maxItems"]); has && float64(len(x)) > m {
			add("array has more than maxItems %v", m)
		}
		if u, _ := s["uniqueItems"].(bool); u {
			for i := range x {
				for j := i + 1; j < len(x); j++ {
					if equal(x[i], x[j]) {
						add("array items %d and %d are equal but uniqueItems is true", i, j)
					}
				}
			}
		}
		if item, has := s["items"].(map[string]any); has {
			for i, e := range x {
				validate(item, e, fmt.Sprintf("%s[%d]", path, i), errs)
			}
		}
	case map[string]any:
		props, _ := s["properties"].(map[string]any)
		if req, has := s["required"].([]any); has {
			for _, r := range req {
				if _, present := x[r.(string)]; !present {
					add("missing required property %q", r)
				}
			}
		}
		if m, has := num(s["minProperties"]); has && float64(len(x)) < m {
			add("object has fewer than minProperties %v", m)
		}
		if m, has := num(s["maxProperties"]); has && float64(len(x)) > m {
			add("object has more than maxProperties %v", m)
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			child := path + "." + k
			if ps, has := props[k].(map[string]any); has {
				validate(ps, x[k], child, errs)
			} else if ap, has := s["additionalProperties"].(bool); has && !ap {
				*errs = append(*errs, fmt.Sprintf("%s: property %q is not allowed (additionalProperties is false)", path, k))
			}
		}
	}
}

func validateNumber(s map[string]any, n json.Number, add func(string, ...any)) {
	f, err := n.Float64()
	if err != nil {
		add("number %s is out of range", n)
		return
	}
	if m, has := num(s["minimum"]); has && f < m {
		add("%s is less than minimum %v", n, m)
	}
	if m, has := num(s["maximum"]); has && f > m {
		add("%s is greater than maximum %v", n, m)
	}
	if m, has := num(s["exclusiveMinimum"]); has && f <= m {
		add("%s must be greater than %v", n, m)
	}
	if m, has := num(s["exclusiveMaximum"]); has && f >= m {
		add("%s must be less than %v", n, m)
	}
	if m, has := num(s["multipleOf"]); has && m > 0 {
		if q := f / m; math.Abs(q-math.Round(q)) > 1e-9 {
			add("%s is not a multiple of %v", n, m)
		}
	}
}

func num(v any) (float64, bool) {
	f, ok := v.(float64)
	return f, ok
}

// equal compares a schema value (decoded with float64 numbers) or a submitted
// value (json.Number) by their canonical JSON.
func equal(a, b any) bool {
	return reflect.DeepEqual(normalize(a), normalize(b))
}

func normalize(v any) any {
	switch x := v.(type) {
	case json.Number:
		f, _ := x.Float64()
		return f
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normalize(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = normalize(e)
		}
		return out
	}
	return v
}

// short renders v as JSON, truncated, for error messages.
func short(v any) string {
	b, _ := json.Marshal(v)
	if len(b) > 80 {
		return string(b[:77]) + "..."
	}
	return string(b)
}
