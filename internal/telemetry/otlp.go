package telemetry

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math"
	"strconv"
	"time"
)

// Span kinds and status codes, as numbered by OTLP.
const (
	kindInternal = 1
	kindClient   = 3

	statusUnset = 0
	statusError = 2
)

type attr struct {
	key string
	val any // string, bool, int64 or float64
}

type spanEvent struct {
	time  time.Time
	name  string
	attrs []attr
}

type span struct {
	traceID    [16]byte
	id, parent [8]byte
	traceState string
	name       string
	kind       int
	start, end time.Time
	attrs      []attr
	events     []spanEvent
	status     int
	statusMsg  string
	queued     bool
}

func (s *span) set(key string, val any) {
	if val = attrValue(val); val == nil {
		return
	}
	for i := range s.attrs {
		if s.attrs[i].key == key {
			s.attrs[i].val = val
			return
		}
	}
	s.attrs = append(s.attrs, attr{key, val})
}

// attrValue converts an event field to an attribute value, or nil to skip.
func attrValue(v any) any {
	switch v := v.(type) {
	case nil:
		return nil
	case string, bool, int64, float64:
		return v
	case int:
		return int64(v)
	case int32:
		return int64(v)
	case uint64:
		return int64(v)
	case float32:
		return float64(v)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return string(b)
}

// batch is one export request's content.
type batch struct {
	resource []attr
	scope    string
	version  string
	spans    []*span
}

// encodeProto encodes b as an ExportTraceServiceRequest in the protobuf
// wire format.
func encodeProto(b batch) []byte {
	var spans pb
	for _, s := range b.spans {
		spans.msg(2, func(m *pb) {
			m.bytes(1, s.traceID[:])
			m.bytes(2, s.id[:])
			m.str(3, s.traceState)
			if s.parent != ([8]byte{}) {
				m.bytes(4, s.parent[:])
			}
			m.str(5, s.name)
			m.varint(6, uint64(s.kind))
			m.fixed64(7, uint64(s.start.UnixNano()))
			m.fixed64(8, uint64(s.end.UnixNano()))
			protoAttrs(m, 9, s.attrs)
			for _, e := range s.events {
				m.msg(11, func(m *pb) {
					m.fixed64(1, uint64(e.time.UnixNano()))
					m.str(2, e.name)
					protoAttrs(m, 3, e.attrs)
				})
			}
			if s.status != statusUnset {
				m.msg(15, func(m *pb) {
					m.str(2, s.statusMsg)
					m.varint(3, uint64(s.status))
				})
			}
		})
	}
	var out pb
	out.msg(1, func(rs *pb) {
		rs.msg(1, func(r *pb) { protoAttrs(r, 1, b.resource) })
		rs.msg(2, func(ss *pb) {
			ss.msg(1, func(sc *pb) {
				sc.str(1, b.scope)
				sc.str(2, b.version)
			})
			*ss = append(*ss, spans...)
		})
	})
	return out
}

func protoAttrs(m *pb, field int, attrs []attr) {
	for _, a := range attrs {
		m.msg(field, func(kv *pb) {
			kv.str(1, a.key)
			kv.msg(2, func(v *pb) {
				switch x := a.val.(type) {
				case string:
					v.tag(1, 2)
					v.uvarint(uint64(len(x)))
					*v = append(*v, x...)
				case bool:
					v.tag(2, 0)
					if x {
						v.uvarint(1)
					} else {
						v.uvarint(0)
					}
				case int64:
					v.tag(3, 0)
					v.uvarint(uint64(x))
				case float64:
					v.tag(4, 1)
					*v = binary.LittleEndian.AppendUint64(*v, math.Float64bits(x))
				}
			})
		})
	}
}

// pb is a protobuf wire-format writer. Scalar fields with zero values are
// omitted, as proto3 does.
type pb []byte

func (b *pb) uvarint(v uint64)    { *b = binary.AppendUvarint(*b, v) }
func (b *pb) tag(field, wire int) { b.uvarint(uint64(field)<<3 | uint64(wire)) }

func (b *pb) bytes(field int, v []byte) {
	if len(v) == 0 {
		return
	}
	b.tag(field, 2)
	b.uvarint(uint64(len(v)))
	*b = append(*b, v...)
}

func (b *pb) str(field int, s string) { b.bytes(field, []byte(s)) }

func (b *pb) varint(field int, v uint64) {
	if v == 0 {
		return
	}
	b.tag(field, 0)
	b.uvarint(v)
}

func (b *pb) fixed64(field int, v uint64) {
	if v == 0 {
		return
	}
	b.tag(field, 1)
	*b = binary.LittleEndian.AppendUint64(*b, v)
}

// msg writes an embedded message, even an empty one.
func (b *pb) msg(field int, fill func(*pb)) {
	var m pb
	fill(&m)
	b.tag(field, 2)
	b.uvarint(uint64(len(m)))
	*b = append(*b, m...)
}

// encodeJSON encodes b in the OTLP/JSON encoding: lowerCamelCase field
// names, hex trace and span IDs, integer enums, and 64-bit integers as
// decimal strings.
func encodeJSON(b batch) []byte {
	spans := make([]map[string]any, 0, len(b.spans))
	for _, s := range b.spans {
		m := map[string]any{
			"traceId":           hex.EncodeToString(s.traceID[:]),
			"spanId":            hex.EncodeToString(s.id[:]),
			"name":              s.name,
			"kind":              s.kind,
			"startTimeUnixNano": nanos(s.start),
			"endTimeUnixNano":   nanos(s.end),
			"attributes":        jsonAttrs(s.attrs),
		}
		if s.traceState != "" {
			m["traceState"] = s.traceState
		}
		if s.parent != ([8]byte{}) {
			m["parentSpanId"] = hex.EncodeToString(s.parent[:])
		}
		if len(s.events) > 0 {
			events := make([]map[string]any, len(s.events))
			for i, e := range s.events {
				events[i] = map[string]any{"timeUnixNano": nanos(e.time), "name": e.name, "attributes": jsonAttrs(e.attrs)}
			}
			m["events"] = events
		}
		if s.status != statusUnset {
			st := map[string]any{"code": s.status}
			if s.statusMsg != "" {
				st["message"] = s.statusMsg
			}
			m["status"] = st
		}
		spans = append(spans, m)
	}
	out, _ := json.Marshal(map[string]any{"resourceSpans": []any{map[string]any{
		"resource": map[string]any{"attributes": jsonAttrs(b.resource)},
		"scopeSpans": []any{map[string]any{
			"scope": map[string]any{"name": b.scope, "version": b.version},
			"spans": spans,
		}},
	}}})
	return out
}

func nanos(t time.Time) string { return strconv.FormatInt(t.UnixNano(), 10) }

func jsonAttrs(attrs []attr) []any {
	out := make([]any, 0, len(attrs))
	for _, a := range attrs {
		var v map[string]any
		switch x := a.val.(type) {
		case string:
			v = map[string]any{"stringValue": x}
		case bool:
			v = map[string]any{"boolValue": x}
		case int64:
			v = map[string]any{"intValue": strconv.FormatInt(x, 10)}
		case float64:
			v = map[string]any{"doubleValue": x}
		}
		out = append(out, map[string]any{"key": a.key, "value": v})
	}
	return out
}
