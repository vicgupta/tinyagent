package attempts

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"strings"
)

// Environment variables a parent --attempts run sets on each child so the
// child's log DB row and OpenTelemetry spans can be tied back to the group.
const (
	EnvAttemptsID = "TINYAGENT_ATTEMPTS_ID"
	EnvAttempt    = "TINYAGENT_ATTEMPT"
	EnvAttempts   = "TINYAGENT_ATTEMPTS"
)

// Link identifies one attempt within an attempts group.
type Link struct {
	ID          string // attempts group id, shared by every attempt
	Index       int    // 1-based attempt number
	Count       int    // attempts requested
	TraceParent string // W3C traceparent for the child's root span, or ""
}

// NewID returns a random attempts group id (32 hex characters).
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

type linkKey struct{}

func withLink(ctx context.Context, l Link) context.Context {
	return context.WithValue(ctx, linkKey{}, l)
}

// LinkFromContext returns the attempt link runAttempt put on ctx, if any.
func LinkFromContext(ctx context.Context) (Link, bool) {
	l, ok := ctx.Value(linkKey{}).(Link)
	return l, ok
}

// LinkEnv returns base with l's variables set, replacing any inherited
// attempt variables (and TRACEPARENT/TRACESTATE when l carries one).
func LinkEnv(base []string, l Link) []string {
	drop := []string{EnvAttemptsID + "=", EnvAttempt + "=", EnvAttempts + "="}
	if l.TraceParent != "" {
		drop = append(drop, "TRACEPARENT=", "TRACESTATE=")
	}
	out := make([]string, 0, len(base)+4)
next:
	for _, kv := range base {
		for _, p := range drop {
			if strings.HasPrefix(kv, p) {
				continue next
			}
		}
		out = append(out, kv)
	}
	out = append(out, EnvAttemptsID+"="+l.ID, EnvAttempt+"="+strconv.Itoa(l.Index),
		EnvAttempts+"="+strconv.Itoa(l.Count))
	if l.TraceParent != "" {
		out = append(out, "TRACEPARENT="+l.TraceParent)
	}
	return out
}

// LinkFromEnv reads the link a parent --attempts run set; ok is false when
// this process is not an attempt.
func LinkFromEnv(getenv func(string) string) (Link, bool) {
	id := strings.TrimSpace(getenv(EnvAttemptsID))
	if id == "" {
		return Link{}, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(getenv(EnvAttempt)))
	if err != nil || n < 1 {
		return Link{}, false
	}
	c, err := strconv.Atoi(strings.TrimSpace(getenv(EnvAttempts)))
	if err != nil || c < n {
		c = 0
	}
	return Link{ID: id, Index: n, Count: c}, true
}
