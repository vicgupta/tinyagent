package model

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// OpenAI speaks the Chat Completions API, which also covers OpenRouter,
// Ollama, and other OpenAI-compatible endpoints.
type OpenAI struct {
	BaseURL string
	APIKey  string
	Model   string
	OnText  func(string) // if set, stream and report text deltas
	// ReasoningEffort, if set, is sent as reasoning_effort, or as
	// reasoning.effort when NestedReasoning is set (OpenRouter).
	ReasoningEffort string
	NestedReasoning bool
	// MaxOutputTokens, if > 0, is sent under MaxOutputKey
	// (max_completion_tokens, or max_tokens for Ollama).
	MaxOutputTokens int
	MaxOutputKey    string
	// Headers, if set, are added to every request after the defaults
	// (configured providers).
	Headers map[string]string
	// Price, if set, replaces the Runtime's price table entry for this
	// model (configured providers); the Runtime's PriceOverride still wins.
	Price *[3]float64
	// Runtime supplies the HTTP client, retry policy and prices. nil uses
	// DefaultRuntime.
	Runtime *Runtime
}

// openAIMessage is the wire form of Message; it omits Extra and always sends
// content. Content is a string, or a parts array for a user message with
// task attachments.
type openAIMessage struct {
	Role       string     `json:"role"`
	Content    any        `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type openAIResponse struct {
	Choices []struct {
		Message struct {
			Content   string     `json:"content"`
			ToolCalls []ToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage openAIUsage `json:"usage"`
}

type openAIChunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    *int   `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *openAIUsage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// chatUserParts is a user message's content with task attachments: one
// image_url part (a data: URL) per image, then the text.
func chatUserParts(m Message) []map[string]any {
	var parts []map[string]any
	for _, im := range m.Images {
		parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": im.DataURL()}})
	}
	return append(parts, map[string]any{"type": "text", "text": m.Content})
}

func (o *OpenAI) Query(ctx context.Context, messages []Message, tools []ToolSpec) (Message, Cost, error) {
	wire := make([]openAIMessage, len(messages))
	for i, m := range messages {
		var content any = m.Content
		if len(m.Images) > 0 && m.Role == "user" {
			content = chatUserParts(m)
		} else if len(m.Images) > 0 {
			content = m.Content + "\n" + chatImageNote
		}
		calls := m.ToolCalls
		if cut, _ := Truncated(m); cut && len(calls) > 0 {
			calls = append([]ToolCall(nil), calls...)
			for j := range calls {
				calls[j].Function.Arguments = replayArguments(m, calls[j])
			}
		}
		wire[i] = openAIMessage{Role: m.Role, Content: content, ToolCalls: calls, ToolCallID: m.ToolCallID}
	}
	body := map[string]any{"model": o.Model, "messages": wire}
	if o.ReasoningEffort != "" {
		if o.NestedReasoning {
			body["reasoning"] = map[string]string{"effort": o.ReasoningEffort}
		} else {
			body["reasoning_effort"] = o.ReasoningEffort
		}
	}
	if o.MaxOutputTokens > 0 {
		key := o.MaxOutputKey
		if key == "" {
			key = "max_completion_tokens"
		}
		body[key] = o.MaxOutputTokens
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	headers := map[string]string{}
	if o.APIKey != "" {
		headers["Authorization"] = "Bearer " + o.APIKey
	}
	for k, v := range o.Headers {
		headers[k] = v
	}
	url := strings.TrimRight(o.BaseURL, "/") + "/chat/completions"

	var (
		msg    Message
		usage  openAIUsage
		finish string
		err    error
	)
	if o.OnText != nil {
		body["stream"] = true
		body["stream_options"] = map[string]any{"include_usage": true}
		msg, usage, finish, err = o.stream(ctx, url, headers, body)
	} else {
		msg, usage, finish, err = o.complete(ctx, url, headers, body)
	}
	if err != nil {
		return Message{}, Cost{}, err
	}

	msg.Role = "assistant"
	for i := range msg.ToolCalls {
		tc := &msg.ToolCalls[i]
		if tc.ID == "" {
			tc.ID = fmt.Sprintf("call_%d", i)
		}
		if tc.Type == "" {
			tc.Type = "function"
		}
	}
	if finish != "" {
		msg.Extra = map[string]any{"finish_reason": finish}
	}
	if finish == "length" {
		markTruncated(&msg, "finish_reason: length")
	}
	return msg, o.Runtime.cachedPriceWith(o.Model, o.Price, usage.PromptTokens, 0, 0, usage.CompletionTokens), nil
}

func (o *OpenAI) complete(ctx context.Context, url string, headers map[string]string, body any) (Message, openAIUsage, string, error) {
	var resp openAIResponse
	if err := o.Runtime.postJSON(ctx, url, headers, body, &resp); err != nil {
		return Message{}, openAIUsage{}, "", err
	}
	if len(resp.Choices) == 0 {
		return Message{}, openAIUsage{}, "", fmt.Errorf("openai: response has no choices")
	}
	c := resp.Choices[0]
	return Message{Content: c.Message.Content, ToolCalls: c.Message.ToolCalls}, resp.Usage, c.FinishReason, nil
}

// stream reads an SSE response, passing text deltas to OnText and assembling
// tool calls from their fragments.
func (o *OpenAI) stream(ctx context.Context, url string, headers map[string]string, body any) (Message, openAIUsage, string, error) {
	resp, err := o.Runtime.post(ctx, url, headers, body)
	if err != nil {
		return Message{}, openAIUsage{}, "", err
	}
	defer resp.Body.Close()
	if jsonInsteadOfStream(resp) {
		var r openAIResponse
		if err := decodeBody(resp.Body, &r); err != nil {
			return Message{}, openAIUsage{}, "", err
		}
		if len(r.Choices) == 0 {
			return Message{}, openAIUsage{}, "", fmt.Errorf("openai: response has no choices")
		}
		c := r.Choices[0]
		if c.Message.Content != "" {
			o.OnText(c.Message.Content)
		}
		return Message{Content: c.Message.Content, ToolCalls: c.Message.ToolCalls}, r.Usage, c.FinishReason, nil
	}

	var (
		text   strings.Builder
		calls  []ToolCall
		usage  openAIUsage
		finish string
	)
	err = readSSE(resp.Body, func(_, data string) error {
		var chunk openAIChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return fmt.Errorf("decode stream chunk: %w", err)
		}
		if chunk.Error != nil {
			return fmt.Errorf("stream error: %s", chunk.Error.Message)
		}
		if chunk.Usage != nil {
			usage = *chunk.Usage
		}
		for _, c := range chunk.Choices {
			if c.Delta.Content != "" {
				text.WriteString(c.Delta.Content)
				o.OnText(c.Delta.Content)
			}
			for _, d := range c.Delta.ToolCalls {
				// Fragments of one call share an index. Servers that omit the
				// index send each call whole, so append those.
				i := len(calls)
				if d.Index != nil {
					i = *d.Index
				}
				for len(calls) <= i {
					calls = append(calls, ToolCall{})
				}
				tc := &calls[i]
				if d.ID != "" {
					tc.ID = d.ID
				}
				if d.Type != "" {
					tc.Type = d.Type
				}
				tc.Function.Name += d.Function.Name
				tc.Function.Arguments += d.Function.Arguments
			}
			if c.FinishReason != "" {
				finish = c.FinishReason
			}
		}
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return Message{}, openAIUsage{}, "", err
	}
	return Message{Content: text.String(), ToolCalls: calls}, usage, finish, nil
}
