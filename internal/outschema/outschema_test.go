package outschema

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const reviewSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "verdict": {"type": "string", "enum": ["pass", "fail"]},
    "score": {"type": "integer", "minimum": 0, "maximum": 10},
    "findings": {"type": "array", "maxItems": 3, "uniqueItems": true, "items": {
      "type": "object",
      "properties": {"file": {"type": "string", "pattern": "^[a-z/]+\\.go$"}, "line": {"type": "integer"}},
      "required": ["file"],
      "additionalProperties": false
    }},
    "note": {"type": ["string", "null"], "maxLength": 5}
  },
  "required": ["verdict", "findings"],
  "additionalProperties": false
}`

func write(t *testing.T, data string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "schema.json")
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadDropsSchemaKeyword(t *testing.T) {
	s, err := Load(write(t, reviewSchema))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Params["$schema"]; ok || s.Params["type"] != "object" || !strings.HasPrefix(s.Digest, "sha256:") {
		t.Errorf("params = %v, digest %q", s.Params, s.Digest)
	}
}

func TestLoadRejects(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]struct{ path, want string }{
		"missing":     {filepath.Join(dir, "nope.json"), "does not exist"},
		"directory":   {dir, "not a regular file"},
		"invalid":     {write(t, `{"type":`), "invalid JSON"},
		"non-object":  {write(t, `{"type":"array","items":{"type":"string"}}`), `root schema must have "type": "object"`},
		"array root":  {write(t, `[1]`), `root schema must have "type": "object"`},
		"keyword":     {write(t, `{"type":"object","properties":{"a":{"type":"string","oneOf":[]}}}`), `keyword "oneOf" is unsupported`},
		"ref":         {write(t, `{"type":"object","$ref":"#/x"}`), `keyword "$ref" is unsupported`},
		"type":        {write(t, `{"type":"object","properties":{"a":{"type":"date"}}}`), "type date is unsupported"},
		"no type":     {write(t, `{"type":"object","properties":{"a":{}}}`), "every schema must declare a type"},
		"bad pattern": {write(t, `{"type":"object","properties":{"a":{"type":"string","pattern":"("}}}`), "not a valid Go regular expression"},
		"oversized":   {write(t, `{"type":"object","description":"`+strings.Repeat("x", MaxFileBytes)+`"}`), "exceeds"},
	}
	for name, c := range cases {
		_, err := Load(c.path)
		if err == nil || !strings.Contains(err.Error(), c.path) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want file name and %q", name, err, c.want)
		}
	}
}

func TestValidate(t *testing.T) {
	s, err := Parse([]byte(reviewSchema))
	if err != nil {
		t.Fatal(err)
	}
	obj, errs := s.Validate(`{"verdict": "pass", "findings": [{"file": "a/b.go", "line": 3}], "score": 7.0, "note": null}`)
	if errs != nil || string(obj) != `{"verdict":"pass","findings":[{"file":"a/b.go","line":3}],"score":7.0,"note":null}` {
		t.Errorf("valid: obj %s, errs %v", obj, errs)
	}
	cases := map[string]struct{ args, want string }{
		"malformed":   {`{"verdict":`, "not valid JSON"},
		"trailing":    {`{} {}`, "not a single JSON value"},
		"required":    {`{"findings":[]}`, `$: missing required property "verdict"`},
		"type":        {`{"verdict":"pass","findings":{}}`, "$.findings: expected type array, got object"},
		"enum":        {`{"verdict":"maybe","findings":[]}`, "not one of the allowed enum values"},
		"extra":       {`{"verdict":"pass","findings":[],"x":1}`, `property "x" is not allowed`},
		"nested":      {`{"verdict":"pass","findings":[{"line":1}]}`, `$.findings[0]: missing required property "file"`},
		"nested type": {`{"verdict":"pass","findings":[{"file":"a.go","line":1.5}]}`, "$.findings[0].line: expected type integer, got number"},
		"pattern":     {`{"verdict":"pass","findings":[{"file":"A.txt"}]}`, "does not match pattern"},
		"maximum":     {`{"verdict":"pass","findings":[],"score":11}`, "greater than maximum"},
		"maxItems":    {`{"verdict":"pass","findings":[{"file":"a.go"},{"file":"b.go"},{"file":"c.go"},{"file":"d.go"}]}`, "more than maxItems"},
		"unique":      {`{"verdict":"pass","findings":[{"file":"a.go"},{"file":"a.go"}]}`, "uniqueItems"},
		"maxLength":   {`{"verdict":"pass","findings":[],"note":"toolong"}`, "longer than maxLength"},
		"not object":  {`[]`, "$: expected type object, got array"},
	}
	for name, c := range cases {
		obj, errs := s.Validate(c.args)
		if obj != nil || !strings.Contains(strings.Join(errs, "\n"), c.want) {
			t.Errorf("%s: obj %s, errs %v, want %q", name, obj, errs, c.want)
		}
	}
}

func TestValidateCapsErrors(t *testing.T) {
	var props, req []string
	for i := 0; i < 30; i++ {
		name := string(rune('a'+i%26)) + strings.Repeat("x", i/26)
		props = append(props, `"`+name+`":{"type":"string"}`)
		req = append(req, `"`+name+`"`)
	}
	s, err := Parse([]byte(`{"type":"object","properties":{` + strings.Join(props, ",") + `},"required":[` + strings.Join(req, ",") + `]}`))
	if err != nil {
		t.Fatal(err)
	}
	_, errs := s.Validate(`{}`)
	if len(errs) != maxErrors+1 || !strings.Contains(errs[maxErrors], "20 more errors") {
		t.Errorf("errs = %v", errs)
	}
}
