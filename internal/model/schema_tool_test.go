package model

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

// submitSchema is a nested --output-schema as tinyagent passes it through.
var submitSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"verdict":  map[string]any{"type": "string", "enum": []any{"pass", "fail"}},
		"findings": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"file": map[string]any{"type": "string"}}, "required": []any{"file"}, "additionalProperties": false}},
	},
	"required":             []any{"verdict"},
	"additionalProperties": false,
}

var submitTools = []ToolSpec{{Type: "function", Function: FunctionSpec{Name: "submit_result", Description: "Submit", Parameters: submitSchema}}}

// TestSubmitToolSchemaSerialization checks every adapter sends the output
// schema unchanged as the submit_result tool's parameters.
func TestSubmitToolSchemaSerialization(t *testing.T) {
	var got map[string]any
	capture := func(reply string) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = nil
			json.NewDecoder(r.Body).Decode(&got)
			io.WriteString(w, reply)
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	msgs := []Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "hi"}}
	openaiReply := `{"choices":[{"message":{"role":"assistant","content":"x"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`

	// openai, openrouter and ollama share the OpenAI chat adapter.
	srv := capture(openaiReply)
	if _, _, err := (&OpenAI{BaseURL: srv.URL, APIKey: "k", Model: "m"}).Query(context.Background(), msgs, submitTools); err != nil {
		t.Fatal(err)
	}
	fn := got["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "submit_result" || !reflect.DeepEqual(fn["parameters"], submitSchema) {
		t.Errorf("openai tool = %v", fn)
	}

	srv = capture(`{"stop_reason":"end_turn","content":[{"type":"text","text":"x"}],"usage":{"input_tokens":1,"output_tokens":1}}`)
	if _, _, err := (&Anthropic{BaseURL: srv.URL, APIKey: "k", Model: "m"}).Query(context.Background(), msgs, submitTools); err != nil {
		t.Fatal(err)
	}
	at := got["tools"].([]any)[0].(map[string]any)
	if at["name"] != "submit_result" || !reflect.DeepEqual(at["input_schema"], submitSchema) {
		t.Errorf("anthropic tool = %v", at)
	}

	access := fakeJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	srv = capture(codexEvents)
	c := &Codex{BaseURL: srv.URL, AuthFile: writeCodexAuth(t, access), Model: "gpt-5.5"}
	if _, _, err := c.Query(context.Background(), msgs, submitTools); err != nil {
		t.Fatal(err)
	}
	ct := got["tools"].([]any)[0].(map[string]any)
	if ct["name"] != "submit_result" || !reflect.DeepEqual(ct["parameters"], submitSchema) {
		t.Errorf("codex tool = %v", ct)
	}
}
