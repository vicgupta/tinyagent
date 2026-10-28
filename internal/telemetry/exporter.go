package telemetry

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	mrand "math/rand/v2"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/vicgupta/tinyagent/internal/eventlog"
)

const (
	// batchSize is how many ended spans are sent in one request during the
	// run; the rest go at Close.
	batchSize = 512
	// maxQueuedBatches bounds the spans waiting to be sent; batches that
	// don't fit are dropped and counted.
	maxQueuedBatches = 8
	maxBackoff       = 8 * time.Second
)

// Options describes one invocation to trace.
type Options struct {
	Settings Settings
	// Model is the provider/model spec the run starts with.
	Model    string
	Task     string
	ReadOnly bool
	Resumed  bool
	Version  string
	// Redactor masks secrets in exported text; nil masks token shapes only.
	Redactor *eventlog.Redactor
	// TraceParent and TraceState are the inbound W3C trace context, if any.
	TraceParent, TraceState string
	// Client sends the exports; nil means a default client.
	Client *http.Client
}

// Exporter turns one invocation's events into a trace: an invoke_agent
// root span with chat, execute_tool, hook and subagent spans under it. It
// is an eventlog.Sink. Spans are buffered and sent in the background in
// batches and at Close; export problems are only reported by Close.
type Exporter struct {
	s        Settings
	o        Options
	redact   *eventlog.Redactor
	client   *http.Client
	resource []attr

	mu        sync.Mutex
	root      *span
	rootEnded bool
	closed    bool
	agents    map[string]*agentSpans // "" is the main agent, else subagent id
	pending   []*span                // ended, not yet queued
	total     int
	failed    int
	dropped   int
	err       error
	// tainted is set by the run's first taint event (#171).
	tainted bool

	queue  chan []*span
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// agentSpans tracks one agent's open spans.
type agentSpans struct {
	span     *span
	tools    map[string]*span // open execute_tool spans by call id
	current  *span            // the open tool span other events attach to
	waiting  []spanEvent      // retries and fallbacks for the next model request
	lastChat *span
}

// New starts the root span and the background sender. The returned warning
// is non-empty when TraceParent was set but malformed; the trace then
// starts fresh.
func New(o Options) (*Exporter, string) {
	e := &Exporter{s: o.Settings, o: o, redact: o.Redactor, client: o.Client,
		agents: map[string]*agentSpans{}, queue: make(chan []*span, maxQueuedBatches), done: make(chan struct{})}
	if e.redact == nil {
		e.redact = eventlog.NewValueRedactor(nil, true)
	}
	e.redact.Add(o.Settings.HeaderValues()...)
	if e.client == nil {
		e.client = &http.Client{}
	}
	e.ctx, e.cancel = context.WithCancel(context.Background())
	e.resource = []attr{{attrServiceName, e.s.ServiceName}}
	if o.Version != "" {
		e.resource = append(e.resource, attr{attrServiceVersion, o.Version})
	}
	keys := make([]string, 0, len(e.s.Resource))
	for k := range e.s.Resource {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		e.resource = append(e.resource, attr{k, e.s.Resource[k]})
	}

	root := &span{kind: kindInternal, name: opInvokeAgent + " tinyagent", start: time.Now()}
	warning := ""
	if o.TraceParent != "" {
		if trace, parent, ok := ParseTraceParent(o.TraceParent); ok {
			root.traceID, root.parent, root.traceState = trace, parent, strings.TrimSpace(o.TraceState)
		} else {
			warning = "otel: ignoring malformed TRACEPARENT; starting a new trace"
		}
	}
	if root.traceID == ([16]byte{}) {
		rand.Read(root.traceID[:])
	}
	rand.Read(root.id[:])
	root.set(attrOperation, opInvokeAgent)
	root.set(attrAgentName, "tinyagent")
	setModel(root, o.Model)
	root.set(attrResumed, o.Resumed)
	root.set(attrReadOnly, o.ReadOnly)
	if o.Version != "" {
		root.set(attrVersion, o.Version)
	}
	if e.s.Content && o.Task != "" {
		root.set(attrTask, e.content(o.Task))
	}
	e.root = root
	e.agents[""] = &agentSpans{span: root, tools: map[string]*span{}}
	go e.run()
	return e, warning
}

// TraceID returns the trace's ID in hex.
func (e *Exporter) TraceID() string { return hex.EncodeToString(e.root.traceID[:]) }

// ParseTraceParent parses a W3C traceparent header value.
func ParseTraceParent(v string) (trace [16]byte, parent [8]byte, ok bool) {
	parts := strings.Split(strings.TrimSpace(v), "-")
	if len(parts) < 4 || len(parts[0]) != 2 || len(parts[1]) != 32 || len(parts[2]) != 16 || len(parts[3]) != 2 {
		return trace, parent, false
	}
	if parts[0] == "ff" || (parts[0] == "00" && len(parts) != 4) {
		return trace, parent, false
	}
	for _, p := range parts[:4] {
		if strings.ToLower(p) != p {
			return trace, parent, false
		}
		if _, err := hex.DecodeString(p); err != nil {
			return trace, parent, false
		}
	}
	hex.Decode(trace[:], []byte(parts[1]))
	hex.Decode(parent[:], []byte(parts[2]))
	return trace, parent, trace != [16]byte{} && parent != [8]byte{}
}

// Emit records one run event.
func (e *Exporter) Emit(kind string, fields map[string]any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.rootEnded {
		return
	}
	now := time.Now()
	// assistant.message may still annotate the chat span just ended.
	if kind != "assistant.message" {
		e.flush(false)
	}
	ag := e.agents[""]
	if id, ok := fields["subagent"].(string); ok && e.agents[id] != nil {
		ag = e.agents[id]
	}
	e.lockdownEvent(kind, fields)
	switch kind {
	case "run.started":
	case "model.request":
		e.modelRequest(ag, now, fields)
	case "provider.retry", "model.fallback":
		ag.waiting = append(ag.waiting, e.event(now, kind, fields))
	case "assistant.message":
		if c := ag.lastChat; e.s.Content && c != nil && !c.queued {
			c.set(attrAssistant, e.content(str(fields["text"])))
		}
	case "tool.call":
		e.toolCall(ag, now, fields)
	case "tool.result":
		e.toolResult(ag, now, fields)
	case "hook":
		e.hook(ag, now, fields)
	case "subagent.started":
		e.subagentStarted(ag, now, fields)
	case "subagent.finished":
		e.subagentFinished(now, fields)
	case "result":
		e.endRoot(now, fields)
	case "error":
		if ag.span == e.root {
			e.root.statusMsg = e.content(str(fields["message"]))
		}
		fallthrough
	default:
		target := ag.span
		if ag.current != nil {
			target = ag.current
		}
		target.events = append(target.events, e.event(now, kind, fields))
	}
}

func (e *Exporter) modelRequest(ag *agentSpans, now time.Time, fields map[string]any) {
	dur := time.Duration(num(fields["duration_ms"])) * time.Millisecond
	spec := str(fields["model"])
	_, name := splitModel(spec)
	sp := e.newSpan(ag.span, opChat+" "+name, kindClient, now.Add(-dur))
	sp.set(attrOperation, opChat)
	setModel(sp, spec)
	sp.set(attrResponseModel, name)
	sp.set(attrStep, fields["step"])
	sp.set(attrInputTokens, fields["input_tokens"])
	sp.set(attrOutputTokens, fields["output_tokens"])
	sp.set(attrCacheReadTokens, fields["cache_read_tokens"])
	sp.set(attrCacheWriteTokens, fields["cache_write_tokens"])
	if msg := str(fields["error"]); msg != "" {
		sp.status, sp.statusMsg = statusError, e.content(msg)
		sp.set(attrErrorType, "model_error")
	}
	sp.events, ag.waiting = ag.waiting, nil
	e.end(sp, now)
	ag.lastChat = sp
}

func (e *Exporter) toolCall(ag *agentSpans, now time.Time, fields map[string]any) {
	sp := e.toolSpan(ag.span, now, fields)
	if e.s.Content {
		sp.set(attrToolArguments, e.content(text(fields["arguments"])))
	}
	ag.tools[str(fields["id"])] = sp
	ag.current = sp
}

func (e *Exporter) toolSpan(parent *span, now time.Time, fields map[string]any) *span {
	name := str(fields["name"])
	sp := e.newSpan(parent, opExecuteTool+" "+name, kindInternal, now)
	sp.set(attrOperation, opExecuteTool)
	sp.set(attrToolName, name)
	sp.set(attrToolCallID, str(fields["id"]))
	if e.tainted {
		sp.set(attrLockdownTainted, true)
	}
	return sp
}

func (e *Exporter) toolResult(ag *agentSpans, now time.Time, fields map[string]any) {
	id := str(fields["id"])
	sp := ag.tools[id]
	if sp == nil {
		// A result without a call this run: truncated or resumed calls.
		sp = e.toolSpan(ag.span, now, fields)
	}
	delete(ag.tools, id)
	if ag.current == sp {
		ag.current = nil
	}
	out := str(fields["output"])
	if b, ok := fields["is_error"].(bool); ok && b {
		sp.status = statusError
		sp.set(attrErrorType, "tool_error")
	}
	if n, ok := fields["output_bytes"]; ok {
		sp.set(attrOutputBytes, n)
	} else {
		sp.set(attrOutputBytes, len(out))
	}
	sp.set(attrRedactions, fields["redactions"])
	sp.set(attrTruncated, fields["truncated"])
	sp.set(attrFormatError, fields["format_error"])
	if e.s.Content {
		sp.set(attrToolResult, e.content(out))
	}
	e.end(sp, now)
}

func (e *Exporter) hook(ag *agentSpans, now time.Time, fields map[string]any) {
	parent := ag.span
	if p := ag.tools[str(fields["tool_call_id"])]; p != nil {
		parent = p
	}
	event := str(fields["event"])
	dur := time.Duration(num(fields["duration_ms"])) * time.Millisecond
	sp := e.newSpan(parent, "hook "+event, kindInternal, now.Add(-dur))
	sp.set(attrHookEvent, event)
	sp.set(attrHookDecision, str(fields["decision"]))
	if id := str(fields["tool_call_id"]); id != "" {
		sp.set(attrToolCallID, id)
		sp.set(attrToolName, str(fields["tool_name"]))
	}
	if fields["decision"] == "error" {
		sp.status = statusError
		sp.set(attrErrorType, "hook_error")
	}
	e.end(sp, now)
}

func (e *Exporter) subagentStarted(ag *agentSpans, now time.Time, fields map[string]any) {
	parent := ag.span
	if p := ag.tools[str(fields["tool_call_id"])]; p != nil {
		parent = p
	}
	id := str(fields["id"])
	name := "delegate"
	if str(fields["kind"]) == "worker" {
		name = "worker"
	}
	sp := e.newSpan(parent, opInvokeAgent+" "+name, kindInternal, now)
	sp.set(attrOperation, opInvokeAgent)
	sp.set(attrAgentName, name)
	sp.set(attrSubagentID, id)
	setModel(sp, str(fields["model"]))
	if name == "worker" {
		setWorkerStarted(sp, fields)
	}
	e.agents[id] = &agentSpans{span: sp, tools: map[string]*span{}}
}

func (e *Exporter) subagentFinished(now time.Time, fields map[string]any) {
	id := str(fields["id"])
	ag := e.agents[id]
	if ag == nil || id == "" {
		return
	}
	delete(e.agents, id)
	e.closeOpen(ag, now)
	sp := ag.span
	status := str(fields["status"])
	sp.set(attrStatus, status)
	sp.set(attrSteps, fields["steps"])
	sp.set(attrInputTokens, fields["input_tokens"])
	sp.set(attrOutputTokens, fields["output_tokens"])
	sp.set(attrCostUSD, fields["cost_usd"])
	if str(fields["kind"]) == "worker" {
		setWorkerFinished(sp, fields)
	}
	if status != "Submitted" && status != "Completed" {
		sp.status, sp.statusMsg = statusError, status
	}
	e.end(sp, now)
}

// endRoot ends the root span with the result event's fields (nil when the
// run ended without one) and every span still open.
func (e *Exporter) endRoot(now time.Time, fields map[string]any) {
	for id, ag := range e.agents {
		e.closeOpen(ag, now)
		if id != "" {
			e.end(ag.span, now)
		}
	}
	r := e.root
	if fields != nil {
		status := str(fields["status"])
		r.set(attrStatus, status)
		r.set(attrSteps, fields["steps"])
		r.set(attrInputTokens, fields["input_tokens"])
		r.set(attrOutputTokens, fields["output_tokens"])
		r.set(attrCacheReadTokens, fields["cache_read_tokens"])
		r.set(attrCacheWriteTokens, fields["cache_write_tokens"])
		r.set(attrCostUSD, fields["cost_usd"])
		r.set(attrDurationMS, fields["duration_ms"])
		r.set(attrLockdownTainted, fields["tainted"])
		if ErrorStatus(status) {
			r.status = statusError
			if r.statusMsg == "" {
				r.statusMsg = status
			}
		}
	}
	e.end(r, now)
	e.rootEnded = true
}

// ErrorStatus reports whether a run status marks the root span as an
// error: every status except Submitted, Completed and NeedsInput.
func ErrorStatus(status string) bool {
	switch status {
	case "Submitted", "Completed", "NeedsInput":
		return false
	}
	return true
}

func (e *Exporter) closeOpen(ag *agentSpans, now time.Time) {
	for id, sp := range ag.tools {
		e.end(sp, now)
		delete(ag.tools, id)
	}
	ag.current = nil
}

func (e *Exporter) newSpan(parent *span, name string, kind int, start time.Time) *span {
	sp := &span{traceID: parent.traceID, parent: parent.id, name: name, kind: kind, start: start}
	rand.Read(sp.id[:])
	return sp
}

func (e *Exporter) end(sp *span, now time.Time) {
	sp.end = now
	e.pending = append(e.pending, sp)
	e.total++
}

// flush queues the ended spans once a batch is full, or with force any.
// A full queue drops the batch.
func (e *Exporter) flush(force bool) {
	if len(e.pending) == 0 || (!force && len(e.pending) < batchSize) {
		return
	}
	b := e.pending
	e.pending = nil
	for _, s := range b {
		s.queued = true
	}
	select {
	case e.queue <- b:
	default:
		e.dropped += len(b)
	}
}

// event converts fields to a span event: bodies (eventlog.BodyKeys) are
// replaced by their length unless content is on, and strings are redacted
// and capped.
func (e *Exporter) event(now time.Time, kind string, fields map[string]any) spanEvent {
	ev := spanEvent{time: now, name: kind}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		if k != "subagent" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := attrValue(fields[k])
		if v == nil {
			continue
		}
		if slices.Contains(eventlog.BodyKeys, k) && !e.s.Content {
			ev.attrs = append(ev.attrs, attr{k + "_len", int64(len(text(fields[k])))})
			continue
		}
		if s, ok := v.(string); ok {
			v = e.content(s)
		}
		ev.attrs = append(ev.attrs, attr{k, v})
	}
	return ev
}

// content redacts s and cuts it to MaxAttributeBytes on a rune boundary.
func (e *Exporter) content(s string) string {
	s = e.redact.Redact(s)
	max := e.s.MaxAttributeBytes
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// Close ends any open spans, sends what is left and waits at most the
// timeout. It returns one warning describing every export failure, or "".
func (e *Exporter) Close() string {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return ""
	}
	if !e.rootEnded {
		e.endRoot(time.Now(), nil)
	}
	e.closed = true
	final := e.pending
	e.pending = nil
	e.mu.Unlock()

	deadline := time.NewTimer(e.s.Timeout)
	defer deadline.Stop()
	timedOut := false
	if len(final) > 0 {
		select {
		case e.queue <- final:
		case <-deadline.C:
			timedOut = true
			e.fail(len(final), e.timeoutErr())
		}
	}
	close(e.queue)
	if !timedOut {
		select {
		case <-e.done:
		case <-deadline.C:
			timedOut = true
		}
	}
	e.cancel()
	<-e.done
	return e.warning()
}

func (e *Exporter) warning() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.failed == 0 && e.dropped == 0 {
		return ""
	}
	msg := "otel: exporting traces to " + e.s.Host() + " failed"
	if e.err != nil {
		msg += ": " + e.err.Error()
	}
	msg += fmt.Sprintf("; %d of %d spans not delivered", e.failed+e.dropped, e.total)
	if e.dropped > 0 {
		msg += fmt.Sprintf(" (%d dropped because the export queue was full)", e.dropped)
	}
	return msg
}

func (e *Exporter) fail(n int, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.failed += n
	if e.err == nil {
		e.err = err
	}
}

func (e *Exporter) timeoutErr() error {
	return fmt.Errorf("no response within the %s timeout", e.s.Timeout)
}

// run sends queued batches until the queue is closed.
func (e *Exporter) run() {
	defer close(e.done)
	for spans := range e.queue {
		err := e.timeoutErr()
		if e.ctx.Err() == nil {
			ctx, cancel := context.WithTimeout(e.ctx, e.s.Timeout)
			err = e.send(ctx, spans)
			cancel()
		}
		if err != nil {
			e.fail(len(spans), err)
		}
	}
}

// send POSTs spans, retrying 429, 502, 503, 504 and network errors with
// jittered exponential back-off (or Retry-After) until ctx ends.
func (e *Exporter) send(ctx context.Context, spans []*span) error {
	b := batch{resource: e.resource, scope: scopeName, version: e.o.Version, spans: spans}
	body, ctype := encodeProto(b), "application/x-protobuf"
	if e.s.Protocol == "http/json" {
		body, ctype = encodeJSON(b), "application/json"
	}
	backoff := 250 * time.Millisecond
	for {
		retry, wait, err := e.post(ctx, body, ctype)
		if err == nil || !retry {
			return err
		}
		if wait <= 0 {
			wait = backoff/2 + mrand.N(backoff/2)
			backoff = min(2*backoff, maxBackoff)
		}
		if dl, ok := ctx.Deadline(); ok && time.Until(dl) < wait {
			return err
		}
		t := time.NewTimer(wait)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return err
		}
	}
}

func (e *Exporter) post(ctx context.Context, body []byte, ctype string) (retry bool, wait time.Duration, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.s.Endpoint, bytes.NewReader(body))
	if err != nil {
		return false, 0, err
	}
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("User-Agent", "tinyagent/"+e.o.Version+" (OTLP/HTTP trace exporter)")
	for k, v := range e.s.Headers {
		req.Header.Set(k, v)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return false, 0, e.timeoutErr()
		}
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // without the URL
		}
		return true, 0, err
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		return false, 0, nil
	}
	err = fmt.Errorf("HTTP %s", resp.Status)
	switch resp.StatusCode {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true, retryAfter(resp.Header.Get("Retry-After")), err
	}
	return false, 0, err
}

// retryAfter parses a Retry-After value in seconds or as an HTTP date.
func retryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return time.Until(t)
	}
	return 0
}

// setModel sets the model attributes for a provider/model spec.
func setModel(sp *span, spec string) {
	if spec == "" {
		return
	}
	provider, name := splitModel(spec)
	sp.set(attrRequestModel, name)
	sp.set(attrProvider, provider)
	sp.set(attrModel, spec)
}

// splitModel splits a provider/model spec; a bare model is openai's.
func splitModel(spec string) (provider, name string) {
	if p, n, ok := strings.Cut(spec, "/"); ok {
		return p, n
	}
	return "openai", spec
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// text returns v as text: strings as they are, anything else as JSON.
func text(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	a := attrValue(v)
	if s, ok := a.(string); ok {
		return s
	}
	return fmt.Sprint(a)
}

func num(v any) int64 {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	}
	return 0
}

// Settings returns the exporter's settings.
func (e *Exporter) Settings() Settings { return e.s }
