package model

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDRfakepixels")

func imageHistory() []Message {
	im := NewImage("image/png", "docs/ui.png", pngBytes)
	return []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "look"},
		{Role: "assistant", ToolCalls: []ToolCall{
			{ID: "c1", Type: "function", Function: FunctionCall{Name: "view_image", Arguments: `{"path":"docs/ui.png"}`}},
			{ID: "c2", Type: "function", Function: FunctionCall{Name: "read_file", Arguments: `{"path":"a"}`}},
		}},
		{Role: "tool", ToolCallID: "c1", Content: "image docs/ui.png (image/png, 26 bytes)\n" + im.Placeholder(), Images: []Image{im}},
		{Role: "tool", ToolCallID: "c2", Content: "data"},
	}
}

// capture serves reply and records the decoded request body.
func capture(t *testing.T, reply string) (*httptest.Server, *map[string]any) {
	t.Helper()
	body := map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(data, &body); err != nil {
			t.Errorf("request body: %v", err)
		}
		io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv, &body
}

func TestImagePlaceholderHasNoData(t *testing.T) {
	im := NewImage("image/png", "x.png", pngBytes)
	ph := im.Placeholder()
	if strings.Contains(ph, im.Data) || !strings.HasPrefix(ph, "[image: image/png, 26 bytes, sha256:") {
		t.Errorf("placeholder = %q", ph)
	}
	if im.Size() != len(pngBytes) {
		t.Errorf("Size = %d, want %d", im.Size(), len(pngBytes))
	}
}

func TestAnthropicSendsImageInToolResult(t *testing.T) {
	srv, body := capture(t, `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{}}`)
	a := &Anthropic{BaseURL: srv.URL, Model: "claude-sonnet-4"}
	if _, _, err := a.Query(context.Background(), imageHistory(), nil); err != nil {
		t.Fatal(err)
	}
	msgs := (*body)["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	if last["role"] != "user" {
		t.Fatalf("last message = %v", last)
	}
	blocks := last["content"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("tool_result blocks = %v", blocks)
	}
	first := blocks[0].(map[string]any)
	content := first["content"].([]any)
	if first["type"] != "tool_result" || first["tool_use_id"] != "c1" || len(content) != 2 {
		t.Fatalf("image tool_result = %v", first)
	}
	if text := content[0].(map[string]any); text["type"] != "text" || !strings.Contains(text["text"].(string), "docs/ui.png") {
		t.Errorf("text block = %v", text)
	}
	img := content[1].(map[string]any)
	src := img["source"].(map[string]any)
	if img["type"] != "image" || src["type"] != "base64" || src["media_type"] != "image/png" || src["data"] != NewImage("image/png", "", pngBytes).Data {
		t.Errorf("image block = %v", img)
	}
	if second := blocks[1].(map[string]any); second["content"] != "data" {
		t.Errorf("plain tool_result changed: %v", second)
	}
}

func TestCodexSendsImageInFunctionCallOutput(t *testing.T) {
	access := fakeJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	srv, body := capture(t, codexEvents)
	c := &Codex{BaseURL: srv.URL, AuthFile: writeCodexAuth(t, access), Model: "gpt-5.5"}
	if _, _, err := c.Query(context.Background(), imageHistory(), nil); err != nil {
		t.Fatal(err)
	}
	var outputs []map[string]any
	for _, it := range (*body)["input"].([]any) {
		if m := it.(map[string]any); m["type"] == "function_call_output" {
			outputs = append(outputs, m)
		}
	}
	if len(outputs) != 2 {
		t.Fatalf("outputs = %v", outputs)
	}
	parts, ok := outputs[0]["output"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("image output = %v", outputs[0]["output"])
	}
	if p := parts[0].(map[string]any); p["type"] != "input_text" || !strings.Contains(p["text"].(string), "docs/ui.png") {
		t.Errorf("text part = %v", p)
	}
	want := "data:image/png;base64," + NewImage("image/png", "", pngBytes).Data
	if p := parts[1].(map[string]any); p["type"] != "input_image" || p["image_url"] != want {
		t.Errorf("image part = %v", p)
	}
	if outputs[1]["output"] != "data" {
		t.Errorf("plain output changed: %v", outputs[1]["output"])
	}
}

// Chat Completions tool messages carry text only. The image is replaced by a
// note in the tool message; no synthetic user message is added.
func checkChatPlaceholder(t *testing.T, body map[string]any) {
	t.Helper()
	msgs := body["messages"].([]any)
	if len(msgs) != 5 {
		t.Fatalf("messages = %d, want 5 (no synthetic user message)", len(msgs))
	}
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), NewImage("image/png", "", pngBytes).Data) || strings.Contains(string(raw), "image_url") {
		t.Errorf("image data sent over Chat Completions: %s", raw)
	}
	tool := msgs[3].(map[string]any)
	if tool["role"] != "tool" || !strings.Contains(tool["content"].(string), "[image: image/png") || !strings.Contains(tool["content"].(string), chatImageNote) {
		t.Errorf("tool message = %v", tool)
	}
	if msgs[4].(map[string]any)["content"] != "data" {
		t.Errorf("plain tool message changed: %v", msgs[4])
	}
}

const chatReply = `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`

func TestOpenAIAndOpenRouterSendImagePlaceholder(t *testing.T) {
	for _, nested := range []bool{false, true} { // openai, openrouter
		srv, body := capture(t, chatReply)
		o := &OpenAI{BaseURL: srv.URL, Model: "gpt-4o", NestedReasoning: nested}
		if _, _, err := o.Query(context.Background(), imageHistory(), nil); err != nil {
			t.Fatal(err)
		}
		checkChatPlaceholder(t, *body)
	}
}

func TestOllamaSendsImagePlaceholder(t *testing.T) {
	srv, body := capture(t, chatReply)
	t.Setenv("OLLAMA_HOST", srv.URL)
	if _, _, err := NewOllama("llava").Query(context.Background(), imageHistory(), nil); err != nil {
		t.Fatal(err)
	}
	checkChatPlaceholder(t, *body)
}

func TestNoImagesRequestUnchanged(t *testing.T) {
	h := imageHistory()
	plain := append([]Message(nil), h...)
	plain[3].Images = nil
	srv, body := capture(t, chatReply)
	o := &OpenAI{BaseURL: srv.URL, Model: "gpt-4o"}
	if _, _, err := o.Query(context.Background(), plain, nil); err != nil {
		t.Fatal(err)
	}
	if c := (*body)["messages"].([]any)[3].(map[string]any)["content"].(string); strings.Contains(c, chatImageNote) {
		t.Errorf("note added without images: %q", c)
	}
	_, msgs := toAnthropic(plain)
	raw, _ := json.Marshal(msgs)
	if strings.Contains(string(raw), `"type":"image"`) || !strings.Contains(string(raw), `"content":"image docs/ui.png`) {
		t.Errorf("anthropic history = %s", raw)
	}
}
