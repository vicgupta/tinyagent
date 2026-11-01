package telemetry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vicgupta/tinyagent/internal/eventlog"
)

const (
	// batchSize is how many ended spans are sent in one request during the
	// run; the rest go at Close.
	batchSize = 512
	// maxQueuedBatches bounds the spans waiting to be sent; batches that
	// don't fit are dropped and counted.
	maxQueuedBatches = 8
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
	// Client sends the exports; nil means a default client. It is used
	// only when Transport is nil.
	Client *http.Client
	// Transport delivers each export attempt; nil means OTLP/HTTP to
	// Settings.Endpoint over Client.
	Transport Transport
}

// Exporter turns one invocation's events into a trace: an invoke_agent
// root span with chat, execute_tool, hook and subagent spans under it. It
// is an eventlog.Sink. Spans are buffered and sent in the background in
// batches and at Close; export problems are only reported by Close.
type Exporter struct {
	s         Settings
	o         Options
	redact    *eventlog.Redactor
	transport Transport
	resource  []attr

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
	e := &Exporter{s: o.Settings, o: o, redact: o.Redactor, transport: o.Transport,
		agents: map[string]*agentSpans{}, queue: make(chan []*span, maxQueuedBatches), done: make(chan struct{})}
	if e.redact == nil {
		e.redact = eventlog.NewValueRedactor(nil, true)
	}
	e.redact.Add(o.Settings.HeaderValues()...)
	if e.transport == nil {
		e.transport = newHTTPTransport(o.Settings, o.Client, o.Version)
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
		e.attemptStarted(fields)
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

// ErrorStatus reports whether a run status marks the root span as an
// error: every status except Submitted, Completed and NeedsInput.
func ErrorStatus(status string) bool {
	switch status {
	case "Submitted", "Completed", "NeedsInput":
		return false
	}
	return true
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

func (e *Exporter) timeoutErr() error { return timeoutErr(e.s.Timeout) }

// Settings returns the exporter's settings.
func (e *Exporter) Settings() Settings { return e.s }
