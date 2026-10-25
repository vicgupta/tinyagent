package model

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// The openai provider's transports, chosen by the openai_api setting.
const (
	OpenAIAPIAuto      = "auto"
	OpenAIAPIResponses = "responses"
	OpenAIAPIChat      = "chat"
)

// OpenAIAPIs are the accepted openai_api values.
var OpenAIAPIs = []string{OpenAIAPIAuto, OpenAIAPIResponses, OpenAIAPIChat}

// openAIDefaultBaseURL is OpenAI's own API, used when OPENAI_BASE_URL is unset.
const openAIDefaultBaseURL = "https://api.openai.com/v1"

// ValidOpenAIAPI reports whether v is an accepted openai_api value. "" means
// auto.
func ValidOpenAIAPI(v string) bool {
	if v == "" {
		return true
	}
	for _, a := range OpenAIAPIs {
		if v == a {
			return true
		}
	}
	return false
}

// ResolveOpenAIAPI returns the transport, "responses" or "chat", that the
// openai provider uses for setting (an openai_api value; "" means auto) with
// base, the value of OPENAI_BASE_URL ("" when unset). auto picks Responses
// only for OpenAI's own API, so OpenAI-compatible servers keep Chat
// Completions.
func ResolveOpenAIAPI(setting, base string) string {
	switch setting {
	case OpenAIAPIResponses, OpenAIAPIChat:
		return setting
	}
	if base == "" || strings.TrimRight(base, "/") == openAIDefaultBaseURL {
		return OpenAIAPIResponses
	}
	return OpenAIAPIChat
}

// OpenAITransport returns the transport New would use for spec with setting,
// reading OPENAI_BASE_URL, or "" when spec's provider isn't openai (the
// setting only applies to it).
func OpenAITransport(spec, setting string) string {
	if provider, _ := splitSpec(spec); provider != "openai" {
		return ""
	}
	return ResolveOpenAIAPI(setting, os.Getenv("OPENAI_BASE_URL"))
}

// OpenAIResponses speaks OpenAI's Responses API (POST /responses) with an API
// key. It is stateless (store: false) and keeps reasoning items that carry
// encrypted_content for replay, so tool calls and reasoning work together.
type OpenAIResponses struct {
	BaseURL string
	APIKey  string
	Model   string
	OnText  func(string) // if set, stream and report text deltas
	// ReasoningEffort, if set, is sent as reasoning.effort.
	ReasoningEffort string
	// MaxOutputTokens, if > 0, is sent as max_output_tokens.
	MaxOutputTokens int

	session string // prompt cache key, one per run
	// Headers, if set, are added to every request after the defaults
	// (configured providers).
	Headers map[string]string
	// Price, if set, replaces the Prices table entry for this model
	// (configured providers); PriceOverride still wins.
	Price *[3]float64
}

func (o *OpenAIResponses) Query(ctx context.Context, messages []Message, tools []ToolSpec) (Message, Cost, error) {
	if o.session == "" {
		o.session = newSessionID()
	}
	instructions, input := toResponses(messages)
	if input == nil {
		input = []map[string]any{}
	}
	body := map[string]any{
		"model":            o.Model,
		"input":            input,
		"store":            false,
		"prompt_cache_key": o.session,
	}
	if instructions != "" {
		body["instructions"] = instructions
	}
	if len(tools) > 0 {
		body["tools"] = responsesTools(tools)
		body["tool_choice"] = "auto"
	}
	if o.ReasoningEffort != "" {
		body["reasoning"] = map[string]string{"effort": o.ReasoningEffort}
	}
	if o.MaxOutputTokens > 0 {
		body["max_output_tokens"] = o.MaxOutputTokens
	}
	headers := map[string]string{}
	if o.APIKey != "" {
		headers["Authorization"] = "Bearer " + o.APIKey
	}
	for k, v := range o.Headers {
		headers[k] = v
	}
	url := strings.TrimRight(o.BaseURL, "/") + "/responses"

	var (
		msg   Message
		usage responsesUsage
		err   error
	)
	if o.OnText != nil {
		body["stream"] = true
		headers["Accept"] = "text/event-stream"
		resp, perr := post(ctx, url, headers, body)
		if perr != nil {
			return Message{}, Cost{}, perr
		}
		defer resp.Body.Close()
		msg, usage, err = readResponsesStream(resp.Body, o.OnText, "openai")
		if err != nil && ctx.Err() != nil {
			err = ctx.Err()
		}
	} else {
		var obj responsesObject
		if err := postJSON(ctx, url, headers, body, &obj); err != nil {
			return Message{}, Cost{}, err
		}
		msg, usage, err = parseResponsesObject(obj, "openai")
	}
	if err != nil {
		return Message{}, Cost{}, err
	}
	cached := usage.cached()
	if cached > usage.InputTokens {
		cached = usage.InputTokens
	}
	return msg, cachedPriceWith(o.Model, o.Price, usage.InputTokens-cached, 0, cached, usage.OutputTokens), nil
}

// responsesTools converts tool specs to the Responses API's flat function
// tools.
func responsesTools(tools []ToolSpec) []map[string]any {
	wire := make([]map[string]any, len(tools))
	for i, t := range tools {
		wire[i] = map[string]any{
			"type":        "function",
			"name":        t.Function.Name,
			"description": t.Function.Description,
			"parameters":  t.Function.Parameters,
			"strict":      false,
		}
	}
	return wire
}

type responsesError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *responsesError) text() string {
	if e.Code != "" && !strings.Contains(e.Message, e.Code) {
		return fmt.Sprintf("%s (%s)", e.Message, e.Code)
	}
	return e.Message
}

type responsesUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	InputTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

func (u responsesUsage) cached() int {
	if u.InputTokensDetails == nil {
		return 0
	}
	return u.InputTokensDetails.CachedTokens
}

// responsesObject is a Responses API response, as returned by a
// non-streaming request and inside the final stream events.
type responsesObject struct {
	Status            string            `json:"status"`
	Output            []json.RawMessage `json:"output"`
	Usage             responsesUsage    `json:"usage"`
	Error             *responsesError   `json:"error"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
}

// state is the status recorded in Message.Extra: the response status, with
// the reason when it is incomplete.
func (r responsesObject) state() string {
	if d := r.IncompleteDetails; d != nil && d.Reason != "" {
		return "incomplete: " + d.Reason
	}
	return r.Status
}

// responsesEvent covers the Responses API stream events we read.
type responsesEvent struct {
	Type     string          `json:"type"`
	Delta    string          `json:"delta"`
	Item     json.RawMessage `json:"item"`
	Response responsesObject `json:"response"`
	Message  string          `json:"message"`
	Code     string          `json:"code"`
	Error    *responsesError `json:"error"`
}

// responsesOutput assembles a Message from Responses API output items.
type responsesOutput struct {
	text  strings.Builder
	calls []ToolCall
	// reasoning items, kept only when they carry encrypted_content: with
	// store=false an item without it cannot be sent back.
	reasoning []json.RawMessage
}

func (o *responsesOutput) add(raw json.RawMessage) {
	var item struct {
		Type             string `json:"type"`
		EncryptedContent string `json:"encrypted_content"`
		CallID           string `json:"call_id"`
		Name             string `json:"name"`
		Arguments        string `json:"arguments"`
		Content          []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &item) != nil {
		return
	}
	switch item.Type {
	case "message":
		for _, p := range item.Content {
			if p.Type == "output_text" {
				o.text.WriteString(p.Text)
			}
		}
	case "reasoning":
		if item.EncryptedContent != "" {
			o.reasoning = append(o.reasoning, append(json.RawMessage(nil), raw...))
		}
	case "function_call":
		o.calls = append(o.calls, ToolCall{
			ID:       item.CallID,
			Type:     "function",
			Function: FunctionCall{Name: item.Name, Arguments: item.Arguments},
		})
	}
}

func (o *responsesOutput) message(state string) Message {
	msg := Message{Role: "assistant", Content: o.text.String(), ToolCalls: o.calls}
	for i := range msg.ToolCalls {
		if msg.ToolCalls[i].ID == "" {
			msg.ToolCalls[i].ID = fmt.Sprintf("call_%d", i)
		}
	}
	if state != "" {
		msg.Extra = map[string]any{"status": state}
	}
	if responsesTruncated(state) {
		markTruncated(&msg, state)
	}
	if len(o.reasoning) > 0 {
		if msg.Extra == nil {
			msg.Extra = map[string]any{}
		}
		msg.Extra[ExtraCodexReasoning] = o.reasoning
	}
	return msg
}

// parseResponsesObject reads a non-streaming Responses API reply. label
// prefixes errors.
func parseResponsesObject(r responsesObject, label string) (Message, responsesUsage, error) {
	if r.Status == "failed" || (r.Error != nil && r.Error.Message != "") {
		if r.Error != nil && r.Error.Message != "" {
			return Message{}, responsesUsage{}, fmt.Errorf("%s: %s", label, r.Error.text())
		}
		return Message{}, responsesUsage{}, fmt.Errorf("%s: response failed", label)
	}
	var out responsesOutput
	for _, item := range r.Output {
		out.add(item)
	}
	return out.message(r.state()), r.Usage, nil
}

// readResponsesStream reads a Responses API event stream, passing text deltas
// to onText (if set). label prefixes errors.
func readResponsesStream(r io.Reader, onText func(string), label string) (Message, responsesUsage, error) {
	var (
		out   responsesOutput
		usage responsesUsage
		done  bool
		state string
	)
	err := readSSE(r, func(_, data string) error {
		var ev responsesEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return fmt.Errorf("decode stream event: %w", err)
		}
		switch ev.Type {
		case "response.output_text.delta":
			if onText != nil {
				onText(ev.Delta)
			}
		case "response.output_item.done":
			out.add(ev.Item)
		case "response.completed", "response.incomplete":
			done = true
			state = ev.Response.state()
			usage = ev.Response.Usage
		case "response.failed":
			if e := ev.Response.Error; e != nil {
				return fmt.Errorf("%s: %s", label, e.text())
			}
			return fmt.Errorf("%s: response failed", label)
		case "error":
			if ev.Error != nil {
				return fmt.Errorf("%s: %s", label, ev.Error.text())
			}
			e := responsesError{Code: ev.Code, Message: ev.Message}
			return fmt.Errorf("%s: %s", label, e.text())
		}
		return nil
	})
	if err != nil {
		return Message{}, responsesUsage{}, err
	}
	if !done {
		return Message{}, responsesUsage{}, fmt.Errorf("%s: stream ended before the response completed", label)
	}
	return out.message(state), usage, nil
}
