package model

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	codexBaseURL  = "https://chatgpt.com/backend-api/codex"
	codexTokenURL = "https://auth.openai.com/oauth/token"
	codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann" // the Codex CLI's OAuth client
)

// Codex uses a ChatGPT subscription instead of an API key. It signs in with
// the tokens `codex login` saves in $CODEX_HOME/auth.json, refreshes them when
// they expire (writing them back so the Codex CLI stays signed in), and speaks
// the Responses API that the Codex backend serves.
type Codex struct {
	BaseURL  string
	TokenURL string
	AuthFile string
	Model    string
	OnText   func(string)
	// ReasoningEffort, if set, is sent as reasoning.effort.
	ReasoningEffort string

	session string // prompt cache key, one per run
}

func NewCodex(model string, onText func(string)) (*Codex, error) {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		dir, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		home = filepath.Join(dir, ".codex")
	}
	c := &Codex{
		BaseURL:  codexBaseURL,
		TokenURL: codexTokenURL,
		AuthFile: filepath.Join(home, "auth.json"),
		Model:    model,
		OnText:   onText,
	}
	if _, err := c.loadAuth(); err != nil {
		return nil, err
	}
	return c, nil
}

// codexAuth holds the parts of auth.json we use. raw keeps the whole file so
// a rewrite preserves fields we don't know about.
type codexAuth struct {
	raw          map[string]any
	accessToken  string
	refreshToken string
	idToken      string
	accountID    string
}

func (c *Codex) loadAuth() (*codexAuth, error) {
	data, err := os.ReadFile(c.AuthFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("codex: no login at %s; run `codex login` and sign in with ChatGPT", c.AuthFile)
	}
	if err != nil {
		return nil, err
	}
	a := &codexAuth{}
	if err := json.Unmarshal(data, &a.raw); err != nil {
		return nil, fmt.Errorf("codex: parse %s: %w", c.AuthFile, err)
	}
	tokens, _ := a.raw["tokens"].(map[string]any)
	str := func(k string) string { s, _ := tokens[k].(string); return s }
	a.accessToken, a.refreshToken, a.idToken, a.accountID = str("access_token"), str("refresh_token"), str("id_token"), str("account_id")
	if a.accessToken == "" || a.refreshToken == "" {
		return nil, fmt.Errorf("codex: %s has no ChatGPT login; run `codex login` and sign in with ChatGPT", c.AuthFile)
	}
	if a.accountID == "" {
		var claims struct {
			Auth struct {
				AccountID string `json:"chatgpt_account_id"`
			} `json:"https://api.openai.com/auth"`
		}
		jwtClaims(a.idToken, &claims)
		a.accountID = claims.Auth.AccountID
	}
	return a, nil
}

// jwtClaims decodes a JWT's payload into out without verifying it.
func jwtClaims(token string, out any) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	return err == nil && json.Unmarshal(data, out) == nil
}

// expiresSoon reports whether a JWT expires within a minute. Tokens without a
// readable expiry are assumed valid; a 401 still triggers a refresh.
func expiresSoon(token string) bool {
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if !jwtClaims(token, &claims) || claims.Exp == 0 {
		return false
	}
	return time.Until(time.Unix(claims.Exp, 0)) < time.Minute
}

// refresh trades the refresh token for new tokens and saves them.
func (c *Codex) refresh(ctx context.Context, a *codexAuth) error {
	body := map[string]string{
		"client_id":     codexClientID,
		"grant_type":    "refresh_token",
		"refresh_token": a.refreshToken,
		"scope":         "openid profile email",
	}
	var resp struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := postJSON(ctx, c.TokenURL, nil, body, &resp); err != nil {
		var he *httpError
		if errors.As(err, &he) && he.status < 500 {
			return fmt.Errorf("codex: login expired; run `codex login` again (%w)", err)
		}
		return fmt.Errorf("codex: refresh token: %w", err)
	}
	if resp.AccessToken == "" {
		return fmt.Errorf("codex: refresh response has no access token")
	}
	a.accessToken = resp.AccessToken
	if resp.RefreshToken != "" {
		a.refreshToken = resp.RefreshToken
	}
	if resp.IDToken != "" {
		a.idToken = resp.IDToken
	}

	tokens, _ := a.raw["tokens"].(map[string]any)
	if tokens == nil {
		tokens = map[string]any{}
		a.raw["tokens"] = tokens
	}
	tokens["access_token"], tokens["refresh_token"], tokens["id_token"] = a.accessToken, a.refreshToken, a.idToken
	a.raw["last_refresh"] = time.Now().UTC().Format(time.RFC3339Nano)
	return c.saveAuth(a)
}

// saveAuth rewrites auth.json atomically, readable only by the owner.
func (c *Codex) saveAuth(a *codexAuth) error {
	data, err := json.MarshalIndent(a.raw, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(c.AuthFile), ".auth-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), c.AuthFile)
}

// codexEvent covers the Responses API stream events we read.
type codexEvent struct {
	Type  string `json:"type"`
	Delta string `json:"delta"`
	Item  struct {
		Type             string `json:"type"`
		EncryptedContent string `json:"encrypted_content"`
		CallID           string `json:"call_id"`
		Name             string `json:"name"`
		Arguments        string `json:"arguments"`
		Content          []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"item"`
	Response struct {
		Status string `json:"status"`
		Usage  struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
	} `json:"response"`
	Message string `json:"message"`
	Error   *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Codex) Query(ctx context.Context, messages []Message, tools []ToolSpec) (Message, Cost, error) {
	a, err := c.loadAuth() // reread: the Codex CLI may have refreshed it
	if err != nil {
		return Message{}, Cost{}, err
	}
	if expiresSoon(a.accessToken) {
		if err := c.refresh(ctx, a); err != nil {
			return Message{}, Cost{}, err
		}
	}
	if c.session == "" {
		c.session = newSessionID()
	}

	instructions, input := toResponses(messages)
	wireTools := make([]map[string]any, len(tools))
	for i, t := range tools {
		wireTools[i] = map[string]any{
			"type":        "function",
			"name":        t.Function.Name,
			"description": t.Function.Description,
			"parameters":  t.Function.Parameters,
			"strict":      false,
		}
	}
	body := map[string]any{
		"model":            c.Model,
		"instructions":     instructions,
		"input":            input,
		"tools":            wireTools,
		"tool_choice":      "auto",
		"store":            false,
		"stream":           true, // the Codex backend only streams
		"prompt_cache_key": c.session,
	}
	if c.ReasoningEffort != "" {
		body["reasoning"] = map[string]string{"effort": c.ReasoningEffort}
	}
	url := strings.TrimRight(c.BaseURL, "/") + "/responses"

	resp, err := post(ctx, url, c.headers(a), body)
	var he *httpError
	if errors.As(err, &he) && he.status == http.StatusUnauthorized {
		if err := c.refresh(ctx, a); err != nil {
			return Message{}, Cost{}, err
		}
		resp, err = post(ctx, url, c.headers(a), body)
	}
	if err != nil {
		return Message{}, Cost{}, err
	}
	defer resp.Body.Close()

	var (
		msg   = Message{Role: "assistant"}
		text  strings.Builder
		cost  Cost
		done  bool
		state string
		// reasoning items, kept only when they carry encrypted_content:
		// with store=false an item without it cannot be sent back.
		reasoning []json.RawMessage
	)
	err = readSSE(resp.Body, func(_, data string) error {
		var ev codexEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return fmt.Errorf("decode stream event: %w", err)
		}
		switch ev.Type {
		case "response.output_text.delta":
			if c.OnText != nil {
				c.OnText(ev.Delta)
			}
		case "response.output_item.done":
			switch ev.Item.Type {
			case "message":
				for _, p := range ev.Item.Content {
					if p.Type == "output_text" {
						text.WriteString(p.Text)
					}
				}
			case "reasoning":
				if ev.Item.EncryptedContent != "" {
					var raw struct {
						Item json.RawMessage `json:"item"`
					}
					if json.Unmarshal([]byte(data), &raw) == nil {
						reasoning = append(reasoning, raw.Item)
					}
				}
			case "function_call":
				msg.ToolCalls = append(msg.ToolCalls, ToolCall{
					ID:       ev.Item.CallID,
					Type:     "function",
					Function: FunctionCall{Name: ev.Item.Name, Arguments: ev.Item.Arguments},
				})
			}
		case "response.completed", "response.incomplete":
			done = true
			state = ev.Response.Status
			if d := ev.Response.IncompleteDetails; d != nil && d.Reason != "" {
				state = "incomplete: " + d.Reason
			}
			// Subscription usage isn't billed per token.
			cost = Cost{InputTokens: ev.Response.Usage.InputTokens, OutputTokens: ev.Response.Usage.OutputTokens}
		case "response.failed":
			if e := ev.Response.Error; e != nil {
				return fmt.Errorf("codex: %s", e.Message)
			}
			return fmt.Errorf("codex: response failed")
		case "error":
			if ev.Error != nil {
				return fmt.Errorf("codex: %s", ev.Error.Message)
			}
			return fmt.Errorf("codex: %s", ev.Message)
		}
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return Message{}, Cost{}, err
	}
	if !done {
		return Message{}, Cost{}, fmt.Errorf("codex: stream ended before the response completed")
	}
	msg.Content = text.String()
	if state != "" {
		msg.Extra = map[string]any{"status": state}
	}
	if len(reasoning) > 0 {
		if msg.Extra == nil {
			msg.Extra = map[string]any{}
		}
		msg.Extra[ExtraCodexReasoning] = reasoning
	}
	return msg, cost, nil
}

func (c *Codex) headers(a *codexAuth) map[string]string {
	h := map[string]string{
		"Authorization": "Bearer " + a.accessToken,
		"Accept":        "text/event-stream",
		"OpenAI-Beta":   "responses=experimental",
		"originator":    "tinyagent",
		"session_id":    c.session,
	}
	if a.accountID != "" {
		h["chatgpt-account-id"] = a.accountID
	}
	return h
}

// toResponses converts the history into Responses API instructions and input
// items. System messages become the instructions.
func toResponses(messages []Message) (string, []map[string]any) {
	var system []string
	var input []map[string]any
	for _, m := range messages {
		switch m.Role {
		case "system":
			system = append(system, m.Content)
		case "user":
			input = append(input, map[string]any{
				"type": "message", "role": "user",
				"content": []map[string]any{{"type": "input_text", "text": m.Content}},
			})
		case "assistant":
			// Reasoning items go back right before the items of the same
			// response, as the Responses API expects during function calling.
			if raw, ok := rawItems(m.Extra[ExtraCodexReasoning]); ok {
				for _, r := range raw {
					var item map[string]any
					if json.Unmarshal(r, &item) == nil && item["type"] == "reasoning" {
						input = append(input, item)
					}
				}
			}
			if m.Content != "" {
				input = append(input, map[string]any{
					"type": "message", "role": "assistant",
					"content": []map[string]any{{"type": "output_text", "text": m.Content}},
				})
			}
			for _, tc := range m.ToolCalls {
				input = append(input, map[string]any{
					"type": "function_call", "call_id": tc.ID,
					"name": tc.Function.Name, "arguments": tc.Function.Arguments,
				})
			}
		case "tool":
			var output any = m.Content
			if len(m.Images) > 0 {
				parts := []map[string]any{{"type": "input_text", "text": m.Content}}
				for _, im := range m.Images {
					parts = append(parts, map[string]any{"type": "input_image", "image_url": im.DataURL()})
				}
				output = parts
			}
			input = append(input, map[string]any{
				"type": "function_call_output", "call_id": m.ToolCallID, "output": output,
			})
		}
	}
	return strings.Join(system, "\n\n"), input
}

func newSessionID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
