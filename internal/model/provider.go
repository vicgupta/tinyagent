// Package model talks to LLM providers using a single OpenAI-shaped message format.
package model

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type Message struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	ToolCalls  []ToolCall     `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Extra      map[string]any `json:"extra,omitempty"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Content    string `json:"content"`
}

// ToolSpec is an OpenAI function-calling tool definition.
type ToolSpec struct {
	Type     string       `json:"type"`
	Function FunctionSpec `json:"function"`
}

type FunctionSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// Cost is one response's usage. InputTokens counts every input token,
// including the cache reads and writes broken out after it.
type Cost struct {
	USD              float64 `json:"usd"`
	InputTokens      int     `json:"input_tokens"`
	OutputTokens     int     `json:"output_tokens"`
	CacheReadTokens  int     `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int     `json:"cache_write_tokens,omitempty"`
}

type Model interface {
	Query(ctx context.Context, messages []Message, tools []ToolSpec) (Message, Cost, error)
}

// Prices maps a model name prefix to USD per million input/output tokens and,
// optionally, the cache-read price as a multiple of the input price (0 means
// the standard 0.1). The longest matching prefix wins. Unknown models cost 0
// unless overridden.
var Prices = map[string][3]float64{
	"gpt-4o":            {2.50, 10.00},
	"gpt-4o-mini":       {0.15, 0.60},
	"gpt-4.1":           {2.00, 8.00},
	"gpt-4.1-mini":      {0.40, 1.60},
	"gpt-4.1-nano":      {0.10, 0.40},
	"claude-sonnet-4":   {3.00, 15.00},
	"claude-opus-4":     {15.00, 75.00},
	"claude-haiku-4-5":  {1.00, 5.00},
	"claude-3-5-haiku":  {0.80, 4.00},
	"claude-3-7-sonnet": {3.00, 15.00},
	"claude-sonnet-5-5": {2.00, 10.00, 0.05},
	"claude-opus-5-5":   {4.00, 20.00, 0.05},
	"claude-fable-5-1":  {10.00, 50.00, 0.025},
}

// Anthropic prices cache writes (5-minute TTL) and, by default, cache reads as
// these multiples of the input price.
const (
	cacheWriteMultiplier = 1.25
	cacheReadMultiplier  = 0.1
)

// PriceOverride, when non-zero, replaces the price table's input and output
// prices. The table's cache-read multiplier still applies.
var PriceOverride [2]float64

func price(model string, in, out int) Cost {
	return cachedPrice(model, in, 0, 0, out)
}

// cachedPrice prices a response whose input was in uncached tokens plus
// cacheWrite tokens written to and cacheRead tokens read from the cache.
func cachedPrice(model string, in, cacheWrite, cacheRead, out int) Cost {
	p, _ := lookupPrice(model)
	if PriceOverride != [2]float64{} {
		p[0], p[1] = PriceOverride[0], PriceOverride[1]
	}
	read := p[2]
	if read == 0 {
		read = cacheReadMultiplier
	}
	input := float64(in) + float64(cacheWrite)*cacheWriteMultiplier + float64(cacheRead)*read
	return Cost{
		USD:              (input*p[0] + float64(out)*p[1]) / 1e6,
		InputTokens:      in + cacheWrite + cacheRead,
		OutputTokens:     out,
		CacheReadTokens:  cacheRead,
		CacheWriteTokens: cacheWrite,
	}
}

// lookupPrice returns the Prices entry with the longest prefix of model.
func lookupPrice(model string) (p [3]float64, ok bool) {
	best := ""
	for prefix, v := range Prices {
		if strings.HasPrefix(model, prefix) && len(prefix) > len(best) {
			best, p, ok = prefix, v, true
		}
	}
	return p, ok
}

// Unpriced reports whether spec, as passed to New, names a model billed per
// token whose cost tinyagent can't estimate: no Prices entry and no
// PriceOverride, so its cost always reads 0. Ollama and Codex are free by
// design and never unpriced.
func Unpriced(spec string) bool {
	provider, name := splitSpec(spec)
	switch provider {
	case "openai", "openrouter", "anthropic":
	default:
		return false
	}
	if PriceOverride != [2]float64{} {
		return false
	}
	_, ok := lookupPrice(name)
	return !ok
}

// splitSpec splits "<provider>/<model>"; without a "/", the provider is openai.
func splitSpec(spec string) (provider, name string) {
	provider, name, ok := strings.Cut(spec, "/")
	if !ok {
		return "openai", spec
	}
	return provider, name
}

// New resolves "<provider>/<model>" to a Model. Without a "/", the provider is
// openai. If onText is non-nil, responses are streamed and each text delta is
// passed to it as it arrives.
func New(spec string, onText func(string)) (Model, error) {
	provider, name := splitSpec(spec)
	if name == "" {
		return nil, fmt.Errorf("model %q: missing model name", spec)
	}
	switch provider {
	case "openai":
		base := envOr("OPENAI_BASE_URL", "https://api.openai.com/v1")
		return &OpenAI{BaseURL: base, APIKey: os.Getenv("OPENAI_API_KEY"), Model: name, OnText: onText}, nil
	case "openrouter":
		return &OpenAI{BaseURL: "https://openrouter.ai/api/v1", APIKey: os.Getenv("OPENROUTER_API_KEY"), Model: name, OnText: onText}, nil
	case "anthropic":
		base := envOr("ANTHROPIC_BASE_URL", "https://api.anthropic.com")
		return &Anthropic{BaseURL: base, APIKey: os.Getenv("ANTHROPIC_API_KEY"), Model: name, OnText: onText}, nil
	case "ollama":
		m := NewOllama(name)
		m.OnText = onText
		return m, nil
	case "codex":
		return NewCodex(name, onText)
	default:
		return nil, fmt.Errorf("unknown provider %q (want openai, openrouter, anthropic, ollama, or codex)", provider)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

var (
	httpClient  = &http.Client{Timeout: 10 * time.Minute}
	retryBase   = time.Second
	maxAttempts = 6
)

type httpError struct {
	status     int
	body       string
	retryAfter time.Duration
}

func (e *httpError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.status, strings.TrimSpace(e.body))
}

// postJSON sends body to url and decodes the response into out.
func postJSON(ctx context.Context, url string, headers map[string]string, body, out any) error {
	resp, err := post(ctx, url, headers, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// post sends body to url, retrying with exponential backoff on network errors,
// 429, and 5xx. It returns a 2xx response whose body the caller must close.
func post(ctx context.Context, url string, headers map[string]string, body any) (*http.Response, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for attempt := range maxAttempts {
		if attempt > 0 {
			wait := retryBase << (attempt - 1)
			wait += time.Duration(rand.Int64N(int64(wait)/2 + 1))
			if he, ok := lastErr.(*httpError); ok && he.retryAfter > 0 {
				wait = he.retryAfter
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = err
			continue
		}
		if resp.StatusCode < 300 {
			return resp, nil
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			he := &httpError{status: resp.StatusCode, body: string(data)}
			if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil {
				he.retryAfter = time.Duration(s) * time.Second
			}
			lastErr = he
			continue
		}
		return nil, &httpError{status: resp.StatusCode, body: string(data)}
	}
	return nil, fmt.Errorf("giving up after %d attempts: %w", maxAttempts, lastErr)
}

// readSSE calls fn for each data line of a server-sent event stream, with the
// most recent event name. It stops at end of stream or an OpenAI "[DONE]".
func readSSE(r io.Reader, fn func(event, data string) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16<<20)
	event := ""
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			event = ""
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(line[len("event:"):])
		case strings.HasPrefix(line, "data:"):
			data := strings.TrimSpace(line[len("data:"):])
			if data == "[DONE]" {
				return nil
			}
			if err := fn(event, data); err != nil {
				return err
			}
		}
	}
	return sc.Err()
}
