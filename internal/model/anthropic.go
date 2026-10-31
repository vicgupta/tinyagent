package model

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Anthropic speaks the native Messages API.
type Anthropic struct {
	BaseURL   string
	APIKey    string
	Model     string
	MaxTokens int          // defaults to DefaultAnthropicMaxTokens
	OnText    func(string) // if set, stream and report text deltas
	// ReasoningEffort, if set, is sent as output_config.effort.
	ReasoningEffort string
	// Headers, if set, are added to every request after the defaults
	// (configured providers).
	Headers map[string]string
	// Price, if set, replaces the Runtime's price table entry for this
	// model (configured providers); the Runtime's PriceOverride still wins.
	Price *[3]float64
	// Runtime supplies the HTTP client, retry policy and prices. nil uses
	// DefaultRuntime.
	Runtime *Runtime
	// OmitEmptyKey sends no x-api-key header when APIKey is empty
	// (configured providers without api_key_env).
	OmitEmptyKey bool
}

type anthropicBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
	Data      string          `json:"data,omitempty"`
}

type anthropicMessage struct {
	Role    string            `json:"role"`
	Content []json.RawMessage `json:"content"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

// anthropicUsage is a response's usage. With caching, InputTokens counts only
// the uncached input; cached tokens are reported separately.
type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

type anthropicResponse struct {
	Content    []anthropicBlock `json:"content"`
	StopReason string           `json:"stop_reason"`
	Usage      anthropicUsage   `json:"usage"`
}

// anthropicEvent covers the fields used from every streaming event type.
type anthropicEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		Usage anthropicUsage `json:"usage"`
	} `json:"message"`
	ContentBlock *anthropicBlock `json:"content_block"`
	Delta        struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *anthropicUsage `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// toAnthropic splits out the system prompt and converts OpenAI-shaped history
// into content blocks. Tool results become user tool_result blocks, and
// consecutive same-role messages are merged as the API requires.
func toAnthropic(messages []Message) (string, []anthropicMessage) {
	var system []string
	var out []anthropicMessage
	var addRaw func(role string, blocks []json.RawMessage)
	add := func(role string, typed ...anthropicBlock) {
		blocks := make([]json.RawMessage, len(typed))
		for i, b := range typed {
			blocks[i], _ = json.Marshal(b)
		}
		addRaw(role, blocks)
	}
	addRaw = func(role string, blocks []json.RawMessage) {
		if len(blocks) == 0 {
			return
		}
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Content = append(out[n-1].Content, blocks...)
			return
		}
		out = append(out, anthropicMessage{Role: role, Content: blocks})
	}
	for _, m := range messages {
		switch m.Role {
		case "system":
			system = append(system, m.Content)
		case "user":
			if len(m.Images) > 0 {
				addRaw("user", anthropicUserImages(m))
				continue
			}
			if m.Content != "" {
				add("user", anthropicBlock{Type: "text", Text: m.Content})
			}
		case "assistant":
			// Replay a response that carried thinking exactly as received,
			// as the API requires during tool use.
			if raw, ok := anthropicReplay(m); ok {
				addRaw("assistant", raw)
				continue
			}
			var blocks []anthropicBlock
			if m.Content != "" {
				blocks = append(blocks, anthropicBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				input := json.RawMessage(tc.Function.Arguments)
				if !json.Valid(input) {
					input = json.RawMessage("{}")
				}
				blocks = append(blocks, anthropicBlock{Type: "tool_use", ID: tc.ID, Name: tc.Function.Name, Input: input})
			}
			add("assistant", blocks...)
		case "tool":
			if len(m.Images) > 0 {
				addRaw("user", []json.RawMessage{anthropicImageResult(m)})
				continue
			}
			add("user", anthropicBlock{Type: "tool_result", ToolUseID: m.ToolCallID, Content: m.Content})
		}
	}
	return strings.Join(system, "\n\n"), out
}

func (a *Anthropic) Query(ctx context.Context, messages []Message, tools []ToolSpec) (Message, Cost, error) {
	system, msgs := toAnthropic(messages)
	maxTokens := a.MaxTokens
	if maxTokens == 0 {
		maxTokens = DefaultAnthropicMaxTokens
	}
	// Automatic prompt caching: the API puts the cache breakpoint on the last
	// cacheable block, so each step reads the history the previous one wrote.
	body := map[string]any{
		"model":         a.Model,
		"max_tokens":    maxTokens,
		"messages":      msgs,
		"cache_control": map[string]string{"type": "ephemeral"},
	}
	if system != "" {
		body["system"] = system
	}
	if a.ReasoningEffort != "" {
		body["output_config"] = map[string]string{"effort": a.ReasoningEffort}
	}
	if len(tools) > 0 {
		at := make([]anthropicTool, len(tools))
		for i, t := range tools {
			at[i] = anthropicTool{Name: t.Function.Name, Description: t.Function.Description, InputSchema: t.Function.Parameters}
		}
		body["tools"] = at
	}
	headers := map[string]string{
		"x-api-key":         a.APIKey,
		"anthropic-version": "2023-06-01",
	}
	if a.OmitEmptyKey && a.APIKey == "" {
		delete(headers, "x-api-key")
	}
	for k, v := range a.Headers {
		headers[k] = v
	}
	url := strings.TrimRight(a.BaseURL, "/") + "/v1/messages"

	var (
		resp anthropicResponse
		err  error
	)
	if a.OnText != nil {
		body["stream"] = true
		resp, err = a.stream(ctx, url, headers, body)
	} else {
		err = a.Runtime.postJSON(ctx, url, headers, body, &resp)
	}
	if err != nil {
		return Message{}, Cost{}, err
	}

	msg := Message{Role: "assistant", Extra: map[string]any{"stop_reason": resp.StopReason}}
	truncated := anthropicTruncated(resp.StopReason)
	if truncated {
		markTruncated(&msg, "stop_reason: "+resp.StopReason)
	}
	var text []string
	thinking := false
	for _, b := range resp.Content {
		switch b.Type {
		case "thinking", "redacted_thinking":
			thinking = true
		case "text":
			text = append(text, b.Text)
		case "tool_use":
			args := string(b.Input)
			if args == "" {
				args = "{}"
			}
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{
				ID: b.ID, Type: "function",
				Function: FunctionCall{Name: b.Name, Arguments: args},
			})
		}
	}
	msg.Content = strings.Join(text, "\n")
	if thinking {
		var raw []json.RawMessage
		for _, b := range resp.Content {
			// A cut-off response may end in a thinking block that never got
			// its signature; the API rejects it on replay, so drop it.
			if truncated && b.Type == "thinking" && b.Signature == "" {
				continue
			}
			// Cut-off tool input is not valid JSON and must not be replayed.
			if b.Type == "tool_use" && !json.Valid(b.Input) {
				b.Input = json.RawMessage("{}")
			}
			r, _ := json.Marshal(b)
			raw = append(raw, r)
		}
		msg.Extra[ExtraAnthropicContent] = raw
	}
	u := resp.Usage
	return msg, a.Runtime.cachedPriceWith(a.Model, a.Price, u.InputTokens, u.CacheCreationInputTokens, u.CacheReadInputTokens, u.OutputTokens), nil
}

// stream reads an SSE response into the same shape as a non-streamed
// response, passing text deltas to OnText as they arrive.
func (a *Anthropic) stream(ctx context.Context, url string, headers map[string]string, body any) (anthropicResponse, error) {
	resp, err := a.Runtime.post(ctx, url, headers, body)
	if err != nil {
		return anthropicResponse{}, err
	}
	defer resp.Body.Close()
	if jsonInsteadOfStream(resp) {
		var out anthropicResponse
		if err := decodeBody(resp.Body, &out); err != nil {
			return anthropicResponse{}, err
		}
		for _, b := range out.Content {
			if b.Type == "text" && b.Text != "" {
				a.OnText(b.Text)
			}
		}
		return out, nil
	}

	var out anthropicResponse
	var partial []strings.Builder // tool_use input JSON, by block index
	err = readSSE(resp.Body, func(_, data string) error {
		var ev anthropicEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return fmt.Errorf("decode stream event: %w", err)
		}
		switch ev.Type {
		case "message_start":
			if ev.Message != nil {
				out.Usage = ev.Message.Usage
				out.Usage.OutputTokens = 0 // the final count arrives in message_delta
			}
		case "content_block_start":
			for len(out.Content) <= ev.Index {
				out.Content = append(out.Content, anthropicBlock{})
				partial = append(partial, strings.Builder{})
			}
			if ev.ContentBlock != nil {
				out.Content[ev.Index] = *ev.ContentBlock
				out.Content[ev.Index].Input = nil // filled from input_json_delta
			}
		case "content_block_delta":
			if ev.Index >= len(out.Content) {
				return fmt.Errorf("stream delta for unknown block %d", ev.Index)
			}
			switch ev.Delta.Type {
			case "text_delta":
				out.Content[ev.Index].Text += ev.Delta.Text
				a.OnText(ev.Delta.Text)
			case "thinking_delta":
				out.Content[ev.Index].Thinking += ev.Delta.Thinking
			case "signature_delta":
				out.Content[ev.Index].Signature += ev.Delta.Signature
			case "input_json_delta":
				partial[ev.Index].WriteString(ev.Delta.PartialJSON)
			}
		case "content_block_stop":
			if ev.Index < len(out.Content) && out.Content[ev.Index].Type == "tool_use" {
				out.Content[ev.Index].Input = json.RawMessage(partial[ev.Index].String())
			}
		case "message_delta":
			if ev.Delta.StopReason != "" {
				out.StopReason = ev.Delta.StopReason
			}
			if ev.Usage != nil {
				out.Usage.OutputTokens = ev.Usage.OutputTokens
			}
		case "error":
			if ev.Error != nil {
				return fmt.Errorf("stream error: %s: %s", ev.Error.Type, ev.Error.Message)
			}
			return fmt.Errorf("stream error: %s", data)
		}
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return anthropicResponse{}, err
	}
	return out, nil
}

// anthropicReplay returns the stored content blocks of an assistant message
// that carried thinking, provided they still match its tool calls.
func anthropicReplay(m Message) ([]json.RawMessage, bool) {
	raw, ok := rawItems(m.Extra[ExtraAnthropicContent])
	if !ok {
		return nil, false
	}
	var ids []string
	for _, r := range raw {
		var b struct{ Type, ID string }
		if json.Unmarshal(r, &b) != nil {
			return nil, false
		}
		if b.Type == "tool_use" {
			ids = append(ids, b.ID)
		}
	}
	if len(ids) != len(m.ToolCalls) {
		return nil, false
	}
	for i, tc := range m.ToolCalls {
		if ids[i] != tc.ID {
			return nil, false
		}
	}
	return raw, true
}

// anthropicUserImages is a user message with task attachments: one base64
// image block per image, then the text, as Anthropic recommends.
func anthropicUserImages(m Message) []json.RawMessage {
	var blocks []json.RawMessage
	for _, im := range m.Images {
		raw, _ := json.Marshal(anthropicImageBlock(im))
		blocks = append(blocks, raw)
	}
	if m.Content != "" {
		raw, _ := json.Marshal(anthropicBlock{Type: "text", Text: m.Content})
		blocks = append(blocks, raw)
	}
	return blocks
}

func anthropicImageBlock(im Image) map[string]any {
	return map[string]any{
		"type":   "image",
		"source": map[string]any{"type": "base64", "media_type": im.MIMEType, "data": im.Data},
	}
}

// anthropicImageResult is a tool_result whose content is a text block
// followed by one base64 image block per image.
func anthropicImageResult(m Message) json.RawMessage {
	content := []map[string]any{{"type": "text", "text": m.Content}}
	for _, im := range m.Images {
		content = append(content, anthropicImageBlock(im))
	}
	raw, _ := json.Marshal(map[string]any{"type": "tool_result", "tool_use_id": m.ToolCallID, "content": content})
	return raw
}
