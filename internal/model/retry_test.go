package model

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestRetryObserverSeesRetries(t *testing.T) {
	defer func(b time.Duration) { retryBase = b }(retryBase)
	retryBase = time.Millisecond
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, "secret body")
			return
		}
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	var got []Retry
	ctx := WithRetryObserver(context.Background(), func(r Retry) { got = append(got, r) })
	var out map[string]any
	if err := postJSON(ctx, srv.URL, nil, map[string]any{}, &out); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Attempt != 2 || got[0].Reason != "http_503" || got[0].Backoff <= 0 {
		t.Errorf("retries = %+v", got)
	}
}
