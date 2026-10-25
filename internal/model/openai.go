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
}

// openAIMessage is the wire form of Message; it omits Extra and always sends content.
type openAIMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type openAIResponse struct {
	Choices []struct {
		Message      openAIMessage `json:"message"`
		FinishReason string        `json:"finish_reason"`
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

func (o *OpenAI) Query(ctx context.Context, messages []Message, tools []ToolSpec) (Message, Cost, error) {
	wire := make([]openAIMessage, len(messages))
	for i, m := range messages {
		content := m.Content
		if len(m.Images) > 0 {
			content += "\n" + chatImageNote
		}
		wire[i] = openAIMessage{Role: m.Role, Content: content, ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID}
	}
	body := map[string]any{"model": o.Model, "messages": wire}
	if o.ReasoningEffort != "" {
		if o.NestedReasoning {
			body["reasoning"] = map[string]string{"effort": o.ReasoningEffort}
		} else {
			body["reasoning_effort"] = o.ReasoningEffort
		}
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	headers := map[string]string{}
	if o.APIKey != "" {
		headers["Authorization"] = "Bearer " + o.APIKey
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
	return msg, price(o.Model, usage.PromptTokens, usage.CompletionTokens), nil
}

func (o *OpenAI) complete(ctx context.Context, url string, headers map[string]string, body any) (Message, openAIUsage, string, error) {
	var resp openAIResponse
	if err := postJSON(ctx, url, headers, body, &resp); err != nil {
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
	resp, err := post(ctx, url, headers, body)
	if err != nil {
		return Message{}, openAIUsage{}, "", err
	}
	defer resp.Body.Close()

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
