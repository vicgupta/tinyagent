package telemetry

// Export policy and transport. The exporter batches ended spans (flush),
// a background goroutine sends each batch (run), and send encodes it and
// retries transient failures with jittered back-off. A single delivery
// attempt goes through the Transport seam; the default is OTLP/HTTP.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	mrand "math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	initialBackoff = 250 * time.Millisecond
	maxBackoff     = 8 * time.Second
)

// Transport makes one attempt to deliver an encoded export request. It
// does not retry: it reports whether the failure is worth retrying and,
// when the receiver said so (Retry-After), how long to wait first. The
// exporter owns batching, back-off and the overall timeout.
type Transport interface {
	Post(ctx context.Context, body []byte, contentType string) Attempt
}

// Attempt is the outcome of one Transport.Post.
type Attempt struct {
	// Err is nil when the receiver accepted the request.
	Err error
	// Retry reports whether Err is transient (429, 502, 503, 504 or a
	// network error).
	Retry bool
	// Wait is the receiver's requested delay before a retry; zero means
	// use the exporter's back-off.
	Wait time.Duration
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

// send encodes spans and posts them, retrying transient failures with
// jittered exponential back-off (or the receiver's Retry-After) until ctx
// ends.
func (e *Exporter) send(ctx context.Context, spans []*span) error {
	b := batch{resource: e.resource, scope: scopeName, version: e.o.Version, spans: spans}
	body, ctype := encodeProto(b), "application/x-protobuf"
	if e.s.Protocol == "http/json" {
		body, ctype = encodeJSON(b), "application/json"
	}
	backoff := initialBackoff
	for {
		a := e.transport.Post(ctx, body, ctype)
		if a.Err == nil || !a.Retry {
			return a.Err
		}
		wait := a.Wait
		if wait <= 0 {
			wait = backoff/2 + mrand.N(backoff/2)
			backoff = min(2*backoff, maxBackoff)
		}
		if dl, ok := ctx.Deadline(); ok && time.Until(dl) < wait {
			return a.Err
		}
		t := time.NewTimer(wait)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return a.Err
		}
	}
}

// httpTransport is the OTLP/HTTP Transport.
type httpTransport struct {
	client   *http.Client
	endpoint string
	headers  map[string]string
	version  string
	timeout  time.Duration
}

func newHTTPTransport(s Settings, client *http.Client, version string) *httpTransport {
	if client == nil {
		client = &http.Client{}
	}
	return &httpTransport{client: client, endpoint: s.Endpoint, headers: s.Headers, version: version, timeout: s.Timeout}
}

func (t *httpTransport) Post(ctx context.Context, body []byte, ctype string) Attempt {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(body))
	if err != nil {
		return Attempt{Err: err}
	}
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("User-Agent", "tinyagent/"+t.version+" (OTLP/HTTP trace exporter)")
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Attempt{Err: timeoutErr(t.timeout)}
		}
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // without the URL
		}
		return Attempt{Err: err, Retry: true}
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		return Attempt{}
	}
	err = fmt.Errorf("HTTP %s", resp.Status)
	switch resp.StatusCode {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return Attempt{Err: err, Retry: true, Wait: retryAfter(resp.Header.Get("Retry-After"))}
	}
	return Attempt{Err: err}
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

func timeoutErr(d time.Duration) error {
	return fmt.Errorf("no response within the %s timeout", d)
}
