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
	MaxTokens int          // defaults to 8192
	OnText    func(string) // if set, stream and report text deltas
}

type anthropicBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
}

type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
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
	add := func(role string, blocks ...anthropicBlock) {
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
			if m.Content != "" {
				add("user", anthropicBlock{Type: "text", Text: m.Content})
			}
		case "assistant":
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
			add("user", anthropicBlock{Type: "tool_result", ToolUseID: m.ToolCallID, Content: m.Content})
		}
	}
	return strings.Join(system, "\n\n"), out
}

func (a *Anthropic) Query(ctx context.Context, messages []Message, tools []ToolSpec) (Message, Cost, error) {
	system, msgs := toAnthropic(messages)
	maxTokens := a.MaxTokens
	if maxTokens == 0 {
		maxTokens = 8192
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
	url := strings.TrimRight(a.BaseURL, "/") + "/v1/messages"

	var (
		resp anthropicResponse
		err  error
	)
	if a.OnText != nil {
		body["stream"] = true
		resp, err = a.stream(ctx, url, headers, body)
	} else {
		err = postJSON(ctx, url, headers, body, &resp)
	}
	if err != nil {
		return Message{}, Cost{}, err
	}

	msg := Message{Role: "assistant", Extra: map[string]any{"stop_reason": resp.StopReason}}
	var text []string
	for _, b := range resp.Content {
		switch b.Type {
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
	u := resp.Usage
	return msg, cachedPrice(a.Model, u.InputTokens, u.CacheCreationInputTokens, u.CacheReadInputTokens, u.OutputTokens), nil
}

// stream reads an SSE response into the same shape as a non-streamed
// response, passing text deltas to OnText as they arrive.
func (a *Anthropic) stream(ctx context.Context, url string, headers map[string]string, body any) (anthropicResponse, error) {
	resp, err := post(ctx, url, headers, body)
	if err != nil {
		return anthropicResponse{}, err
	}
	defer resp.Body.Close()

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
