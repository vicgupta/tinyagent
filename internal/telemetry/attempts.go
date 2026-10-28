package telemetry

import (
	"encoding/hex"
	"time"
)

// NewAttempts starts the trace for a parent --attempts run: one root span,
// "tinyagent attempts", that every attempt's invoke_agent span is parented
// under through TraceParent.
func NewAttempts(o Options, id string, count int) (*Exporter, string) {
	e, warning := New(o)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.root.name = "tinyagent attempts"
	e.root.set(attrAttemptsID, id)
	e.root.set(attrAttempts, count)
	return e, warning
}

// TraceParent returns a W3C traceparent naming the root span, for child
// processes whose spans belong under it.
func (e *Exporter) TraceParent() string {
	return "00-" + hex.EncodeToString(e.root.traceID[:]) + "-" + hex.EncodeToString(e.root.id[:]) + "-01"
}

// FinishAttempts ends a NewAttempts root span with the group's outcome. The
// span is an error when no attempt passed.
func (e *Exporter) FinishAttempts(winner *int, attemptsRun int, stopReason string, costUSD float64, wallMS int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.rootEnded {
		return
	}
	r := e.root
	if winner != nil {
		r.set(attrAttemptsWin, *winner)
	}
	r.set(attrAttemptsRun, attemptsRun)
	r.set(attrAttemptsStop, stopReason)
	r.set(attrCostUSD, costUSD)
	r.set(attrDurationMS, wallMS)
	if winner == nil {
		r.status = statusError
		r.statusMsg = "no attempt passed: " + stopReason
	}
	e.endRoot(time.Now(), nil)
}

// attemptStarted tags a child attempt's root span from run.started.
func (e *Exporter) attemptStarted(fields map[string]any) {
	m, ok := fields["attempt"].(map[string]any)
	if !ok {
		return
	}
	e.root.set(attrAttemptsID, m["attempts_id"])
	e.root.set(attrAttempt, m["index"])
	if c, ok := m["count"].(int); ok && c > 0 {
		e.root.set(attrAttempts, c)
	}
}
