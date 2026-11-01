package telemetry

// Span building: mapping run events onto chat, execute_tool, hook and
// subagent spans, and redacting the text they carry. Nothing here does I/O;
// ended spans are appended to the exporter's pending list for flush.

import (
	"crypto/rand"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vicgupta/tinyagent/internal/eventlog"
)

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
