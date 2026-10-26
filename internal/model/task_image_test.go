package model

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var jpegBytes = []byte("\xff\xd8\xff\xe0\x00\x10JFIFfake")

// taskImageHistory is a task message with two --image attachments.
func taskImageHistory() ([]Message, []Image) {
	ims := []Image{NewImage("image/png", "a.png", pngBytes), NewImage("image/jpeg", "b.jpg", jpegBytes)}
	return []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "fix it" + AttachmentLabels(ims), Images: ims},
	}, ims
}

func TestAttachmentLabels(t *testing.T) {
	_, ims := taskImageHistory()
	want := "\n\n[attached image 1: a.png, image/png, 26 bytes]\n[attached image 2: b.jpg, image/jpeg, 14 bytes]"
	if got := AttachmentLabels(ims); got != want {
		t.Errorf("labels = %q, want %q", got, want)
	}
	if AttachmentLabels(nil) != "" {
		t.Error("labels without images should be empty")
	}
	info := AttachmentInfo(ims)
	raw, _ := json.Marshal(info)
	if len(info) != 2 || info[0]["name"] != "a.png" || info[1]["bytes"] != 14 || strings.Contains(string(raw), ims[0].Data) || len(info[0]["sha256"].(string)) != 64 {
		t.Errorf("info = %s", raw)
	}
}

func TestAnthropicSendsTaskImagesBeforeText(t *testing.T) {
	msgs, ims := taskImageHistory()
	srv, body := capture(t, `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{}}`)
	if _, _, err := (&Anthropic{BaseURL: srv.URL, Model: "claude-sonnet-4"}).Query(context.Background(), msgs, nil); err != nil {
		t.Fatal(err)
	}
	blocks := (*body)["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if len(blocks) != 3 {
		t.Fatalf("blocks = %v", blocks)
	}
	for i, im := range ims {
		b := blocks[i].(map[string]any)
		src := b["source"].(map[string]any)
		if b["type"] != "image" || src["type"] != "base64" || src["media_type"] != im.MIMEType || src["data"] != im.Data {
			t.Errorf("block %d = %v", i, b)
		}
	}
	if text := blocks[2].(map[string]any); text["type"] != "text" || text["text"] != msgs[1].Content {
		t.Errorf("text block = %v", text)
	}
}

func checkResponsesTaskImages(t *testing.T, body map[string]any) {
	t.Helper()
	msgs, ims := taskImageHistory()
	user := body["input"].([]any)[0].(map[string]any)
	parts := user["content"].([]any)
	if user["role"] != "user" || len(parts) != 3 {
		t.Fatalf("user message = %v", user)
	}
	for i, im := range ims {
		if p := parts[i].(map[string]any); p["type"] != "input_image" || p["image_url"] != im.DataURL() {
			t.Errorf("part %d = %v", i, p)
		}
	}
	if p := parts[2].(map[string]any); p["type"] != "input_text" || p["text"] != msgs[1].Content {
		t.Errorf("text part = %v", p)
	}
}

func TestResponsesAndCodexSendTaskImages(t *testing.T) {
	msgs, _ := taskImageHistory()
	srv, body := capture(t, `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`)
	if _, _, err := (&OpenAIResponses{BaseURL: srv.URL, Model: "gpt-5"}).Query(context.Background(), msgs, nil); err != nil {
		t.Fatal(err)
	}
	checkResponsesTaskImages(t, *body)

	access := fakeJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	srv, body = capture(t, codexEvents)
	if _, _, err := (&Codex{BaseURL: srv.URL, AuthFile: writeCodexAuth(t, access), Model: "gpt-5.5"}).Query(context.Background(), msgs, nil); err != nil {
		t.Fatal(err)
	}
	checkResponsesTaskImages(t, *body)
}

func checkChatTaskImages(t *testing.T, body map[string]any) {
	t.Helper()
	msgs, ims := taskImageHistory()
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), "image not shown") {
		t.Errorf("chatImageNote in a user message: %s", raw)
	}
	parts := body["messages"].([]any)[1].(map[string]any)["content"].([]any)
	if len(parts) != 3 {
		t.Fatalf("parts = %v", parts)
	}
	for i, im := range ims {
		p := parts[i].(map[string]any)
		if p["type"] != "image_url" || p["image_url"].(map[string]any)["url"] != im.DataURL() {
			t.Errorf("part %d = %v", i, p)
		}
	}
	if p := parts[2].(map[string]any); p["type"] != "text" || p["text"] != msgs[1].Content {
		t.Errorf("text part = %v", p)
	}
}

func TestChatSendsTaskImagesAsParts(t *testing.T) {
	msgs, _ := taskImageHistory()
	for _, nested := range []bool{false, true} { // openai, openrouter
		srv, body := capture(t, chatReply)
		if _, _, err := (&OpenAI{BaseURL: srv.URL, Model: "gpt-4o", NestedReasoning: nested}).Query(context.Background(), msgs, nil); err != nil {
			t.Fatal(err)
		}
		checkChatTaskImages(t, *body)
	}
	srv, body := capture(t, chatReply)
	t.Setenv("OLLAMA_HOST", srv.URL)
	if _, _, err := NewOllama("llava").Query(context.Background(), msgs, nil); err != nil {
		t.Fatal(err)
	}
	checkChatTaskImages(t, *body)
}

// Without attachments, user content stays a plain string on every transport.
func TestUserMessageWithoutImagesUnchanged(t *testing.T) {
	msgs := []Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "fix it"}}
	srv, body := capture(t, chatReply)
	if _, _, err := (&OpenAI{BaseURL: srv.URL, Model: "gpt-4o"}).Query(context.Background(), msgs, nil); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal((*body)["messages"])
	if string(raw) != `[{"content":"sys","role":"system"},{"content":"fix it","role":"user"}]` {
		t.Errorf("chat messages = %s", raw)
	}
	_, am := toAnthropic(msgs)
	raw, _ = json.Marshal(am)
	if string(raw) != `[{"role":"user","content":[{"type":"text","text":"fix it"}]}]` {
		t.Errorf("anthropic messages = %s", raw)
	}
	_, in := toResponses(msgs)
	raw, _ = json.Marshal(in)
	if string(raw) != `[{"content":[{"text":"fix it","type":"input_text"}],"role":"user","type":"message"}]` {
		t.Errorf("responses input = %s", raw)
	}
}
