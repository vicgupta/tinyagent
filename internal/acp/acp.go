// Package acp is the agent side of the Agent Client Protocol
// (https://agentclientprotocol.com), protocol version 1: newline-delimited
// JSON-RPC 2.0 over stdio. It implements the baseline methods (initialize,
// authenticate, session/new, session/prompt, session/cancel) and the
// session/update notifications tinyagent sends; the turns themselves run in
// a Session the caller provides.
package acp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"sync"
)

// ProtocolVersion is the ACP version spoken.
const ProtocolVersion = 1

// JSON-RPC 2.0 error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// Stop reasons of a prompt turn.
const (
	StopEndTurn         = "end_turn"
	StopMaxTokens       = "max_tokens"
	StopMaxTurnRequests = "max_turn_requests"
	StopCancelled       = "cancelled"
)

// Error is a JSON-RPC error. A Session returns one to pick the code; any
// other error is reported as an internal error with its message.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

// InvalidParams returns an invalid-params error with a formatted message.
func InvalidParams(format string, args ...any) *Error {
	return &Error{Code: CodeInvalidParams, Message: fmt.Sprintf(format, args...)}
}

// MCPServer is one MCP server a client sends with session/new. Type is ""
// or "stdio" for a local command, "http" or "sse" for a remote one.
type MCPServer struct {
	Type    string        `json:"type,omitempty"`
	Name    string        `json:"name"`
	Command string        `json:"command,omitempty"`
	Args    []string      `json:"args,omitempty"`
	Env     []EnvVariable `json:"env,omitempty"`
	URL     string        `json:"url,omitempty"`
}

// EnvVariable is one environment variable of a client's MCP server.
type EnvVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Session runs one ACP session's prompt turns, one at a time.
type Session interface {
	// Prompt runs one turn, streaming progress through u, and returns its
	// stop reason. When ctx is cancelled (session/cancel) the reply is
	// "cancelled" whatever Prompt returns.
	Prompt(ctx context.Context, p Prompt, u *Updates) (string, error)
	// Close releases the session's resources; it runs after its last turn.
	Close()
}

// NewSessionFunc creates a session for session/new. cwd is absolute; id is
// the new session's id.
type NewSessionFunc func(ctx context.Context, id, cwd string, mcpServers []MCPServer) (Session, error)

// Server answers ACP requests read from a client.
type Server struct {
	Name, Version string
	NewSession    NewSessionFunc
	// Log receives protocol problems; nil discards them.
	Log io.Writer

	wmu sync.Mutex
	out io.Writer

	mu       sync.Mutex
	sessions map[string]*session
	turns    sync.WaitGroup

	// reqMu guards pending and reqSeq, which track agent-to-client requests
	// (session/request_permission) awaiting a response.
	reqMu   sync.Mutex
	reqSeq  int
	pending map[string]chan pendingReply
}

// pendingReply is the response to one agent-to-client request.
type pendingReply struct {
	result json.RawMessage
	err    *Error
}

type session struct {
	impl      Session
	busy      bool
	cancel    context.CancelFunc
	cancelled bool
}

// message is an incoming JSON-RPC message.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  any             `json:"params,omitempty"`
}

type notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

// Serve reads requests from in until it ends or ctx is cancelled, writing
// responses and notifications to out, one JSON object per line. It then
// cancels any running turns, waits for them and closes every session. A
// read error other than EOF is returned.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	s.out = out
	s.sessions = map[string]*session{}
	ctx, cancel := context.WithCancel(ctx)
	lines := make(chan []byte)
	readErr := make(chan error, 1)
	go func() {
		r := bufio.NewReader(in)
		for {
			line, err := r.ReadBytes('\n')
			if len(bytes.TrimSpace(line)) > 0 {
				select {
				case lines <- line:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					err = nil
				}
				readErr <- err
				return
			}
		}
	}()
	var err error
loop:
	for {
		select {
		case line := <-lines:
			s.handle(ctx, line)
		case err = <-readErr:
			break loop
		case <-ctx.Done():
			break loop
		}
	}
	cancel()
	s.turns.Wait()
	s.mu.Lock()
	for _, sess := range s.sessions {
		sess.impl.Close()
	}
	s.sessions = nil
	s.mu.Unlock()
	return err
}

func (s *Server) logf(format string, args ...any) {
	if s.Log != nil {
		fmt.Fprintf(s.Log, "acp: "+format+"\n", args...)
	}
}

// write sends one message as a line. Write errors are logged; the read
// side notices a closed client.
func (s *Server) write(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		s.logf("encoding a message: %v", err)
		return
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if _, err := s.out.Write(append(b, '\n')); err != nil {
		s.logf("writing a message: %v", err)
	}
}

func (s *Server) reply(id json.RawMessage, result any) {
	if result == nil {
		result = struct{}{}
	}
	s.write(response{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *Server) fail(id json.RawMessage, err error) {
	var e *Error
	if !errors.As(err, &e) {
		e = &Error{Code: CodeInternalError, Message: err.Error()}
	}
	if id == nil {
		id = json.RawMessage("null")
	}
	s.write(response{JSONRPC: "2.0", ID: id, Error: e})
}

// Request sends a JSON-RPC request to the client and waits for its response.
// It is called from a turn goroutine (for session/request_permission); Serve's
// read loop routes the response back through deliverResponse. A cancelled ctx
// returns ctx's error.
func (s *Server) Request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	s.reqMu.Lock()
	s.reqSeq++
	id := json.RawMessage(strconv.Itoa(s.reqSeq))
	if s.pending == nil {
		s.pending = map[string]chan pendingReply{}
	}
	ch := make(chan pendingReply, 1)
	s.pending[string(id)] = ch
	s.reqMu.Unlock()
	defer func() {
		s.reqMu.Lock()
		delete(s.pending, string(id))
		s.reqMu.Unlock()
	}()
	s.write(request{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		return r.result, nil
	}
}

// deliverResponse routes a response to its pending request, reporting whether
// one was waiting.
func (s *Server) deliverResponse(m message) bool {
	s.reqMu.Lock()
	ch, ok := s.pending[string(m.ID)]
	s.reqMu.Unlock()
	if !ok {
		return false
	}
	ch <- pendingReply{result: m.Result, err: m.Error}
	return true
}

func (s *Server) handle(ctx context.Context, line []byte) {
	var m message
	if err := json.Unmarshal(line, &m); err != nil {
		var syntax *json.SyntaxError
		if errors.As(err, &syntax) || !json.Valid(line) {
			s.fail(nil, &Error{Code: CodeParseError, Message: "parse error: " + err.Error()})
		} else {
			s.fail(nil, &Error{Code: CodeInvalidRequest, Message: "invalid request: " + err.Error()})
		}
		return
	}
	isRequest := len(m.ID) > 0 && string(m.ID) != "null"
	if m.Method == "" && m.JSONRPC == "2.0" && isRequest {
		// A response to one of our requests (session/request_permission).
		if !s.deliverResponse(m) {
			s.logf("response to unknown request id %s", m.ID)
		}
		return
	}
	if m.JSONRPC != "2.0" || m.Method == "" {
		s.fail(m.ID, &Error{Code: CodeInvalidRequest, Message: `invalid request: want "jsonrpc": "2.0" and a method`})
		return
	}
	if !isRequest {
		s.notify(m)
		return
	}
	switch m.Method {
	case "initialize":
		s.initialize(m)
	case "authenticate":
		s.reply(m.ID, nil)
	case "session/new":
		s.newSession(ctx, m)
	case "session/prompt":
		s.prompt(ctx, m)
	default:
		s.fail(m.ID, &Error{Code: CodeMethodNotFound, Message: "method not found: " + m.Method})
	}
}

// notify handles a notification; unknown ones are ignored, as the
// protocol asks.
func (s *Server) notify(m message) {
	if m.Method != "session/cancel" {
		return
	}
	var p struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(m.Params, &p) != nil {
		s.logf("session/cancel: invalid params")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess := s.sessions[p.SessionID]; sess != nil && sess.busy {
		sess.cancelled = true
		sess.cancel()
	}
}

func decodeParams(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return InvalidParams("invalid params: missing")
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return InvalidParams("invalid params: %v", err)
	}
	return nil
}

func (s *Server) initialize(m message) {
	var p struct {
		ProtocolVersion *int `json:"protocolVersion"`
	}
	if err := decodeParams(m.Params, &p); err != nil {
		s.fail(m.ID, err)
		return
	}
	if p.ProtocolVersion == nil || *p.ProtocolVersion < ProtocolVersion {
		s.fail(m.ID, InvalidParams("unsupported protocolVersion; tinyagent speaks ACP version %d", ProtocolVersion))
		return
	}
	s.reply(m.ID, map[string]any{
		"protocolVersion": ProtocolVersion,
		"agentCapabilities": map[string]any{
			"loadSession":        false,
			"promptCapabilities": map[string]bool{"image": true, "audio": false, "embeddedContext": true},
			"mcpCapabilities":    map[string]bool{"http": false, "sse": false},
		},
		"agentInfo":   map[string]string{"name": s.Name, "version": s.Version},
		"authMethods": []any{},
	})
}

func (s *Server) newSession(ctx context.Context, m message) {
	var p struct {
		Cwd        string      `json:"cwd"`
		MCPServers []MCPServer `json:"mcpServers"`
	}
	if err := decodeParams(m.Params, &p); err != nil {
		s.fail(m.ID, err)
		return
	}
	if !filepath.IsAbs(p.Cwd) {
		s.fail(m.ID, InvalidParams("cwd %q must be an absolute path", p.Cwd))
		return
	}
	id := newID()
	impl, err := s.NewSession(ctx, id, p.Cwd, p.MCPServers)
	if err != nil {
		s.fail(m.ID, err)
		return
	}
	s.mu.Lock()
	s.sessions[id] = &session{impl: impl}
	s.mu.Unlock()
	s.reply(m.ID, map[string]string{"sessionId": id})
}

func newID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return "sess_" + hex.EncodeToString(b)
}

func (s *Server) prompt(ctx context.Context, m message) {
	var p struct {
		SessionID string         `json:"sessionId"`
		Prompt    []ContentBlock `json:"prompt"`
	}
	if err := decodeParams(m.Params, &p); err != nil {
		s.fail(m.ID, err)
		return
	}
	prompt, err := ParsePrompt(p.Prompt)
	if err != nil {
		s.fail(m.ID, err)
		return
	}
	s.mu.Lock()
	sess := s.sessions[p.SessionID]
	switch {
	case sess == nil:
		s.mu.Unlock()
		s.fail(m.ID, InvalidParams("unknown sessionId %q", p.SessionID))
		return
	case sess.busy:
		s.mu.Unlock()
		s.fail(m.ID, &Error{Code: CodeInvalidRequest, Message: fmt.Sprintf("session %s already has a prompt turn running; wait for it or send session/cancel", p.SessionID)})
		return
	}
	turnCtx, cancel := context.WithCancel(ctx)
	sess.busy, sess.cancel, sess.cancelled = true, cancel, false
	s.turns.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.turns.Done()
		stop, err := sess.impl.Prompt(turnCtx, prompt, &Updates{s: s, id: p.SessionID})
		s.mu.Lock()
		cancelled := sess.cancelled || turnCtx.Err() != nil
		sess.busy, sess.cancel = false, nil
		s.mu.Unlock()
		cancel()
		switch {
		case cancelled:
			s.reply(m.ID, map[string]string{"stopReason": StopCancelled})
		case err != nil:
			s.fail(m.ID, err)
		default:
			s.reply(m.ID, map[string]string{"stopReason": stop})
		}
	}()
}

// Updates sends one session's session/update notifications.
type Updates struct {
	s  *Server
	id string
}

// Send sends update as a session/update notification.
func (u *Updates) Send(update any) {
	u.s.write(notification{JSONRPC: "2.0", Method: "session/update", Params: map[string]any{"sessionId": u.id, "update": update}})
}

// Request sends a request to the client and waits for its response (used for
// session/request_permission).
func (u *Updates) Request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return u.s.Request(ctx, method, params)
}

// Message sends a chunk of the agent's reply.
func (u *Updates) Message(text string) {
	if text == "" {
		return
	}
	u.Send(map[string]any{"sessionUpdate": "agent_message_chunk", "content": TextBlock(text)})
}

// ToolCall announces a tool call as pending. locations are absolute paths.
func (u *Updates) ToolCall(id, title, kind string, locations []string, rawInput any) {
	update := map[string]any{"sessionUpdate": "tool_call", "toolCallId": id, "title": title, "kind": kind, "status": "pending"}
	if len(locations) > 0 {
		locs := make([]map[string]string, len(locations))
		for i, l := range locations {
			locs[i] = map[string]string{"path": l}
		}
		update["locations"] = locs
	}
	if rawInput != nil {
		update["rawInput"] = rawInput
	}
	u.Send(update)
}

// ToolCallStatus updates a tool call's status, with optional result text.
func (u *Updates) ToolCallStatus(id, status, text string) {
	update := map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": id, "status": status}
	if text != "" {
		update["content"] = []any{map[string]any{"type": "content", "content": TextBlock(text)}}
	}
	u.Send(update)
}

// PlanEntry is one entry of a plan update; Status is pending, in_progress
// or completed.
type PlanEntry struct {
	Content  string `json:"content"`
	Priority string `json:"priority"`
	Status   string `json:"status"`
}

// Plan sends the agent's current plan.
func (u *Updates) Plan(entries []PlanEntry) {
	if entries == nil {
		entries = []PlanEntry{}
	}
	u.Send(map[string]any{"sessionUpdate": "plan", "entries": entries})
}

// Usage sends the context usage: used of size tokens, and the session's
// cost so far in USD when known (> 0).
func (u *Updates) Usage(used, size int, costUSD float64) {
	update := map[string]any{"sessionUpdate": "usage_update", "used": used, "size": size}
	if costUSD > 0 {
		update["cost"] = map[string]any{"amount": costUSD, "currency": "USD"}
	}
	u.Send(update)
}
