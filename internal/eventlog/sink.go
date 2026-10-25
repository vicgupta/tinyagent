// Package eventlog fans run events out to sinks: the --json JSONL stream and
// the opt-in SQLite event log. Every sink receives the same event types and
// payloads, so they cannot drift apart.
package eventlog

import (
	"encoding/json"
	"io"
)

// Sink receives run events. Emit is called from a single goroutine (the
// agent's), in event order.
type Sink interface {
	Emit(kind string, fields map[string]any)
}

// Fanout sends each event to every sink in order.
type Fanout []Sink

func (f Fanout) Emit(kind string, fields map[string]any) {
	for _, s := range f {
		s.Emit(kind, fields)
	}
}

// JSONL writes each event as one JSON object and newline, with its kind under
// "type". After the first failed write it stops and Err reports the error.
type JSONL struct {
	out io.Writer
	err error
}

func NewJSONL(out io.Writer) *JSONL { return &JSONL{out: out} }

func (e *JSONL) Err() error { return e.err }

func (e *JSONL) Emit(kind string, fields map[string]any) {
	if e.err != nil {
		return
	}
	event := make(map[string]any, len(fields)+1)
	event["type"] = kind
	for key, value := range fields {
		event[key] = value
	}
	line, err := json.Marshal(event)
	if err != nil {
		e.err = err
		return
	}
	line = append(line, '\n')
	n, err := e.out.Write(line)
	if err == nil && n != len(line) {
		err = io.ErrShortWrite
	}
	e.err = err
}
