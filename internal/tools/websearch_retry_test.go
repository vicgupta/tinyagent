package tools

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

// sleepLog records the waits a searcher asked for; tests never sleep.
type sleepLog struct {
	mu     sync.Mutex
	delays []time.Duration
}

func (l *sleepLog) get() []time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]time.Duration(nil), l.delays...)
}

// noSleep makes s's retries instant and jitter-free.
func noSleep(s *WebSearcher) *sleepLog {
	l := &sleepLog{}
	s.retry.sleep = func(ctx context.Context, d time.Duration) error {
		l.mu.Lock()
		l.delays = append(l.delays, d)
		l.mu.Unlock()
		return ctx.Err()
	}
	s.retry.jitter = func(time.Duration) time.Duration { return 0 }
	return l
}

func ddgThen(t *testing.T, fails ...http.HandlerFunc) (http.HandlerFunc, *atomic.Int32) {
	var n atomic.Int32
	ok := ddgOK(t)
	return func(w http.ResponseWriter, r *http.Request) {
		i := int(n.Add(1)) - 1
		if i < len(fails) {
			fails[i](w, r)
			return
		}
		ok(w, r)
	}, &n
}

func status(code int, hdr ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i+1 < len(hdr); i += 2 {
			w.Header().Set(hdr[i], hdr[i+1])
		}
		w.WriteHeader(code)
	}
}

// hangUp closes the connection without a reply (EOF).
func hangUp(w http.ResponseWriter, r *http.Request) {
	c, _, _ := w.(http.Hijacker).Hijack()
	c.Close()
}

// shortBody promises more bytes than it sends (unexpected EOF on read).
func shortBody(w http.ResponseWriter, r *http.Request) {
	c, buf, _ := w.(http.Hijacker).Hijack()
	buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 1000\r\n\r\n<html>")
	buf.Flush()
	c.Close()
}

func counting(n *atomic.Int32, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		if h != nil {
			h(w, r)
		}
	}
}

func searcherWithLog(t *testing.T, f fakeSearch, providers ...string) (*WebSearcher, *sleepLog) {
	s := newTestSearcher(t, f, providers...)
	return s, noSleep(s)
}

func TestWebSearchTransientRetried(t *testing.T) {
	for _, c := range []struct {
		name   string
		fails  []http.HandlerFunc
		delays []time.Duration
	}{
		{"502 then ok", []http.HandlerFunc{status(502)}, []time.Duration{200 * time.Millisecond}},
		{"503 503 then ok", []http.HandlerFunc{status(503), status(503)}, []time.Duration{200 * time.Millisecond, 400 * time.Millisecond}},
		{"EOF then ok", []http.HandlerFunc{hangUp}, []time.Duration{200 * time.Millisecond}},
		{"truncated body then ok", []http.HandlerFunc{shortBody}, []time.Duration{200 * time.Millisecond}},
		{"503 short Retry-After", []http.HandlerFunc{status(503, "Retry-After", "1")}, []time.Duration{time.Second}},
	} {
		t.Run(c.name, func(t *testing.T) {
			ddg, n := ddgThen(t, c.fails...)
			var exaHits atomic.Int32
			s, log := searcherWithLog(t, fakeSearch{ddg: ddg, exa: counting(&exaHits, nil)}, "duckduckgo", "exa")
			out, err := s.Search(context.Background(), "go context")
			if err != nil || !strings.HasPrefix(out, `[web_search "go context" → duckduckgo, 2 results after `) {
				t.Fatalf("out %q err %v", out, err)
			}
			want := len(c.fails) + 1
			if int(n.Load()) != want || exaHits.Load() != 0 {
				t.Errorf("ddg hits %d (want %d), exa hits %d", n.Load(), want, exaHits.Load())
			}
			if got := log.get(); len(got) != len(c.delays) || (len(got) > 0 && got[len(got)-1] != c.delays[len(c.delays)-1]) || got[0] != c.delays[0] {
				t.Errorf("delays %v, want %v", got, c.delays)
			}
			a := s.TakeRecords()[0].Attempts
			if len(a) != 1 || a[0].Tries != want || a[0].Error != "" || len(a[0].Delays) != len(c.delays) {
				t.Errorf("attempts %+v", a)
			}
		})
	}
}

func TestWebSearchTransientExhaustedFallsBack(t *testing.T) {
	var ddgHits atomic.Int32
	s, log := searcherWithLog(t, fakeSearch{ddg: counting(&ddgHits, status(503)), exa: exaOK(t)}, "duckduckgo", "exa")
	out, err := s.Search(context.Background(), "go context")
	if err != nil || !strings.HasPrefix(out, `[web_search "go context" → exa, 2 results; fell back after duckduckgo (3 attempts, transient: HTTP 503 Service Unavailable)]`) {
		t.Fatalf("out %q err %v", out, err)
	}
	if ddgHits.Load() != 3 || len(log.get()) != 2 {
		t.Errorf("ddg hits %d, delays %v", ddgHits.Load(), log.get())
	}
}

func TestWebSearchRateLimit(t *testing.T) {
	date := func(d time.Duration) string { return time.Now().Add(d).UTC().Format(http.TimeFormat) }
	for _, c := range []struct {
		name      string
		fail      http.HandlerFunc
		ddgHits   int32
		answer    string
		wantDelay bool
	}{
		{"short Retry-After seconds", status(429, "Retry-After", "1"), 2, "duckduckgo", true},
		{"short Retry-After date", status(429, "Retry-After", date(1500*time.Millisecond)), 2, "duckduckgo", true},
		{"no Retry-After", status(429), 2, "duckduckgo", true},
		{"long Retry-After seconds", status(429, "Retry-After", "120"), 1, "exa", false},
		{"long Retry-After date", status(429, "Retry-After", date(time.Hour)), 1, "exa", false},
		{"503 long Retry-After", status(503, "Retry-After", "30"), 1, "exa", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			ddg, n := ddgThen(t, c.fail)
			s, log := searcherWithLog(t, fakeSearch{ddg: ddg, exa: exaOK(t)}, "duckduckgo", "exa")
			out, err := s.Search(context.Background(), "go context")
			if err != nil || !strings.Contains(out, "→ "+c.answer+", 2 results") {
				t.Fatalf("out %q err %v", out, err)
			}
			if n.Load() != c.ddgHits || (len(log.get()) > 0) != c.wantDelay {
				t.Errorf("ddg hits %d, delays %v", n.Load(), log.get())
			}
			for _, d := range log.get() {
				if d > 2*time.Second {
					t.Errorf("waited %s, over the cap", d)
				}
			}
		})
	}
}

func TestWebSearchRetryAfterSeconds(t *testing.T) {
	ddg, _ := ddgThen(t, status(429, "Retry-After", "2"))
	s, log := searcherWithLog(t, fakeSearch{ddg: ddg}, "duckduckgo")
	s.Timeout = 10 * time.Second
	if _, err := s.Search(context.Background(), "go context"); err != nil {
		t.Fatal(err)
	}
	if got := log.get(); len(got) != 1 || got[0] != 2*time.Second {
		t.Errorf("delays %v, want [2s]", got)
	}
}

// A retry that wouldn't fit the provider's remaining budget isn't made.
func TestWebSearchRetryRespectsBudget(t *testing.T) {
	ddg, n := ddgThen(t, status(429, "Retry-After", "2"))
	s, _ := searcherWithLog(t, fakeSearch{ddg: ddg, exa: exaOK(t)}, "duckduckgo", "exa")
	s.Timeout = time.Second
	out, err := s.Search(context.Background(), "go context")
	if err != nil || !strings.Contains(out, "→ exa") || n.Load() != 1 {
		t.Fatalf("out %q err %v hits %d", out, err, n.Load())
	}
}

func TestWebSearchNonRetryableFailFast(t *testing.T) {
	for _, c := range []struct {
		name, cat string
		ddg       http.HandlerFunc
	}{
		{"202 challenge", "challenged", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(202)
			io.WriteString(w, "<div class=anomaly-modal>")
		}},
		{"200 captcha", "challenged", func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `<form id="challenge-form" action="/d.js?cc=botnet">`)
		}},
		{"403", "challenged", status(403)},
		{"400", "http_error", status(400)},
		{"404", "http_error", status(404)},
		{"410", "http_error", status(410)},
		{"422", "http_error", status(422)},
		{"redirect", "http_error", status(302, "Location", "http://10.0.0.1/")},
		{"malformed html", "malformed_response", func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, "<html><body><p>We changed everything</p></body></html>")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var hits atomic.Int32
			s, log := searcherWithLog(t, fakeSearch{ddg: counting(&hits, c.ddg), exa: exaOK(t)}, "duckduckgo", "exa")
			out, err := s.Search(context.Background(), "go context")
			if err != nil || !strings.Contains(out, "→ exa, 2 results; fell back after duckduckgo (1 attempt, "+c.cat+": ") {
				t.Fatalf("out %q err %v", out, err)
			}
			if hits.Load() != 1 || len(log.get()) != 0 {
				t.Errorf("ddg hits %d, delays %v", hits.Load(), log.get())
			}
			if a := s.TakeRecords()[0].Attempts[0]; a.Category != c.cat || a.Tries != 1 {
				t.Errorf("attempt %+v", a)
			}
		})
	}
}

func TestWebSearchCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var exaHits, ddgHits atomic.Int32
	s, log := searcherWithLog(t, fakeSearch{
		ddg: counting(&ddgHits, func(w http.ResponseWriter, r *http.Request) {
			cancel()
			select {
			case <-r.Context().Done():
			case <-time.After(time.Second):
			}
		}),
		exa: counting(&exaHits, nil),
	}, "duckduckgo", "exa")
	_, err := s.Search(ctx, "go context")
	if err == nil || !strings.Contains(err.Error(), "web_search canceled") {
		t.Fatalf("err = %v", err)
	}
	if ddgHits.Load() != 1 || exaHits.Load() != 0 || len(log.get()) != 0 {
		t.Errorf("ddg %d exa %d delays %v", ddgHits.Load(), exaHits.Load(), log.get())
	}
	r := s.TakeRecords()[0]
	if r.Error != "canceled" || len(r.Attempts) != 1 || r.Attempts[0].Category != "canceled" {
		t.Errorf("record %+v", r)
	}
	// Canceled before the call: nothing is sent.
	s2, _ := searcherWithLog(t, fakeSearch{ddg: counting(&ddgHits, nil), exa: counting(&exaHits, nil)}, "duckduckgo", "exa")
	if _, err := s2.Search(ctx, "go context"); err == nil || exaHits.Load() != 0 {
		t.Errorf("pre-canceled: err %v, exa hits %d", err, exaHits.Load())
	}
}

// Canceling during a backoff wait stops at once, without the backup.
func TestWebSearchCanceledDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var exaHits atomic.Int32
	s := newTestSearcher(t, fakeSearch{ddg: status(503), exa: counting(&exaHits, nil)}, "duckduckgo", "exa")
	s.retry.sleep = func(ctx context.Context, d time.Duration) error { cancel(); return context.Canceled }
	if _, err := s.Search(ctx, "go context"); err == nil || !strings.Contains(err.Error(), "canceled") || exaHits.Load() != 0 {
		t.Fatalf("err %v exa hits %d", err, exaHits.Load())
	}
}

func TestWebSearchAllProvidersFailedMessage(t *testing.T) {
	s, _ := searcherWithLog(t, fakeSearch{
		ddg: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(202); io.WriteString(w, "anomaly-modal") },
		exa: status(503),
	}, "duckduckgo", "exa")
	_, err := s.Search(context.Background(), "go context")
	want := "web_search: all providers failed: duckduckgo (1 attempt, challenged: bot challenge page (HTTP 202)); exa (3 attempts, transient: HTTP 503 Service Unavailable). " + webSearchHint
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant  %s", err, want)
	}
	r := s.TakeRecords()[0]
	if r.Error != "all providers failed" || r.Attempts[0].Tries != 1 || r.Attempts[1].Tries != 3 || r.Attempts[1].Category != "transient" {
		t.Errorf("record %+v", r)
	}
	f := r.Fields()["attempts"].([]map[string]any)
	if f[1]["category"] != "transient" || f[1]["tries"] != 3 || len(f[1]["retry_delays_ms"].([]int64)) != 2 {
		t.Errorf("fields %v", f)
	}
}

func TestWebSearchEmptyVersusFailed(t *testing.T) {
	emptyDDG := func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `<html><div id="links" class="results"><div class="no-results">No results.</div></div></html>`)
	}
	emptyExa := func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":""}]}}`)
	}
	s, _ := searcherWithLog(t, fakeSearch{ddg: emptyDDG, exa: emptyExa}, "duckduckgo", "exa")
	out, err := s.Search(context.Background(), "go context")
	if err != nil || out != "[web_search \"go context\" → 0 results (duckduckgo, exa found nothing)]\n" {
		t.Fatalf("both empty: out %q err %v", out, err)
	}
	// Primary empty, backup answers: the backup's results are used.
	s, _ = searcherWithLog(t, fakeSearch{ddg: emptyDDG, exa: exaOK(t)}, "duckduckgo", "exa")
	if out, err := s.Search(context.Background(), "go context"); err != nil || !strings.Contains(out, "→ exa, 2 results; fell back after duckduckgo (1 attempt, empty_results: no results)") {
		t.Fatalf("primary empty: out %q err %v", out, err)
	}
	// One empty, one failed: an error that says both.
	s, _ = searcherWithLog(t, fakeSearch{ddg: emptyDDG, exa: status(401)}, "duckduckgo", "exa")
	_, err = s.Search(context.Background(), "go context")
	if err == nil || !strings.HasPrefix(err.Error(), "web_search: no results, and some providers failed: duckduckgo (1 attempt, empty_results: no results); exa (1 attempt, http_error: HTTP 401 Unauthorized).") {
		t.Fatalf("mixed: %v", err)
	}
}

func TestWebSearchBlockedAndUnreachable(t *testing.T) {
	s, err := NewWebSearcher(WebSearchOptions{Providers: []string{"duckduckgo", "exa"}, DuckDuckGoURL: "http://127.0.0.1:1/html/", ExaURL: "http://10.255.255.1/mcp", UserAgent: "ua"})
	if err != nil {
		t.Fatal(err)
	}
	log := noSleep(s)
	_, err = s.Search(context.Background(), "go context")
	if err == nil || !strings.Contains(err.Error(), "duckduckgo (1 attempt, blocked_address: ") || !strings.Contains(err.Error(), "exa (1 attempt, blocked_address: ") || len(log.get()) != 0 {
		t.Fatalf("err %v delays %v", err, log.get())
	}
}

func TestParseDuckDuckGoMalformedEntries(t *testing.T) {
	block := func(href, title string) string {
		return `<div class="result results_links web-result"><h2><a rel="nofollow" class="result__a" href="` + href + `">` + title + `</a></h2><div class="clear"></div></div>`
	}
	page := `<div id="links">` + block("javascript:alert(1)", "Bad") + block("data:text/html,x", "Bad") + block("https://ok.example/a", "Good") +
		block("//duckduckgo.com/l/?uddg=javascript%3Ax", "Bad redirect") + block("https://ok.example/a", "Dup") + block("https://notitle.example/", "") +
		block("//duckduckgo.com/l/?rut=x", "No target") + block("http://exa mple.com/", "Space") + `</div>`
	d := parseDuckDuckGo(page)
	if len(d.results) != 1 || d.results[0].URL != "https://ok.example/a" || d.bad != 6 {
		t.Fatalf("parse %+v", d)
	}
	// Every entry corrupt: malformed, so the backup runs.
	s, _ := searcherWithLog(t, fakeSearch{ddg: func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, block("javascript:x", "A")+block("", "B"))
	}, exa: exaOK(t)}, "duckduckgo", "exa")
	if out, err := s.Search(context.Background(), "go context"); err != nil || !strings.Contains(out, "duckduckgo (1 attempt, malformed_response: all 2 result entries were unusable)") {
		t.Fatalf("out %q err %v", out, err)
	}
}

func TestParseExaMalformed(t *testing.T) {
	big := strings.Repeat("x", 200<<10)
	for _, c := range []struct {
		name, body, cat string
		results         int
	}{
		{"not json", "<html>oops</html>", "malformed_response", 0},
		{"broken json", `{"jsonrpc":"2.0","result":{`, "malformed_response", 0},
		{"truncated sse", "event: message\ndata: {\"jsonrpc\":\"2.0\",\"result\":{\"content\":[", "malformed_response", 0},
		{"rpc rate limit", `{"jsonrpc":"2.0","id":1,"error":{"code":429,"message":"Too many requests"}}`, "rate_limited", 0},
		{"rpc server error", `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"internal"}}`, "transient", 0},
		{"rpc protocol error", `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"bad params"}}`, "malformed_response", 0},
		{"isError", `{"jsonrpc":"2.0","id":1,"result":{"isError":true,"content":[{"type":"text","text":"search failed"}]}}`, "provider_error", 0},
		{"isError rate", `{"jsonrpc":"2.0","id":1,"result":{"isError":true,"content":[{"type":"text","text":"You've hit Exa's free rate limit"}]}}`, "rate_limited", 0},
		{"no content", `{"jsonrpc":"2.0","id":1,"result":{}}`, "malformed_response", 0},
		{"content wrong type", `{"jsonrpc":"2.0","id":1,"result":{"content":"text"}}`, "malformed_response", 0},
		{"unrecognized text", `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"Something new"}]}}`, "malformed_response", 0},
		{"all blocks bad", `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"Title: A\nURL: javascript:x\n---\nTitle: B\nURL: "}]}}`, "malformed_response", 0},
		{"some blocks bad", `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"Title: A\nURL: javascript:x\n---\nTitle: B\nURL: https://b.example/\n---\nTitle: B2\nURL: https://b.example/"}]}}`, "", 1},
		{"oversized sse line", "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"Title: Big\\nURL: https://big.example/\\nText: " + big + "\"}]}}\n\n", "", 1},
		{"crlf sse after ping", "event: ping\r\ndata: {}\r\n\r\nevent: message\r\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"Title: A\\nURL: https://a.example/\"}]}}\r\n\r\n", "", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			res, err := parseExa([]byte(c.body))
			if c.cat == "" {
				if err != nil || len(res) != c.results {
					t.Fatalf("res %+v err %v", res, err)
				}
				return
			}
			if err == nil || err.Category != c.cat {
				t.Fatalf("err %v, want %s", err, c.cat)
			}
		})
	}
	if res, _ := parseExa([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"Title: Big\\nURL: https://big.example/\\nText: " + big + "\"}]}}\n\n")); len(res) != 1 || utf8.RuneCountInString(res[0].Snippet) > maxWebSearchSnippet+1 {
		t.Errorf("big snippet not capped: %d", len(res[0].Snippet))
	}
}

func TestParseSearXNGMalformed(t *testing.T) {
	for _, c := range []struct {
		name, body string
		malformed  bool
		results    int
	}{
		{"html", "<html>", true, 0},
		{"no results key", `{"query":"x"}`, true, 0},
		{"results not array", `{"results":{"a":1}}`, true, 0},
		{"all bad", `{"results":[{"url":5},"str",{"url":"ftp://x"}]}`, true, 0},
		{"mixed", `{"results":[{"url":5,"title":"n"},{"title":3,"url":"https://a.example/"},{"url":"https://a.example/"},{"url":"https://b.example/","content":["x"]}]}`, false, 2},
		{"empty", `{"results":[]}`, false, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			res, err := parseSearXNG([]byte(c.body))
			if (err != nil) != c.malformed || len(res) != c.results || (err != nil && err.Category != WebSearchFailMalformed) {
				t.Fatalf("res %+v err %v", res, err)
			}
		})
	}
}

func TestCleanUTF8AndControl(t *testing.T) {
	s := clean("a\x00b\x1b[31m  "+strings.Repeat("é", 10)+"\u2028z", 5)
	if !utf8.ValidString(s) || s != "a b […" && utf8.RuneCountInString(s) != 6 {
		t.Errorf("clean = %q", s)
	}
	got := clean(strings.Repeat("日本", 200), 7)
	if !utf8.ValidString(got) || got != "日本日本日本日…" {
		t.Errorf("rune cap = %q", got)
	}
	if strings.ContainsAny(clean("x\ty\r\nz\x7f", 0), "\t\r\n\x7f") {
		t.Error("control characters kept")
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	for v, want := range map[string]time.Duration{"3": 3 * time.Second, "0": 0, now.Add(90 * time.Second).Format(http.TimeFormat): 90 * time.Second, now.Add(-time.Minute).Format(http.TimeFormat): 0} {
		if got, ok := parseRetryAfter(v, now); !ok || got != want {
			t.Errorf("%q = %s %v, want %s", v, got, ok, want)
		}
	}
	for _, v := range []string{"", "-1", "soon"} {
		if _, ok := parseRetryAfter(v, now); ok {
			t.Errorf("%q parsed", v)
		}
	}
}

func TestClassifyTransport(t *testing.T) {
	for _, c := range []struct {
		err error
		cat string
	}{
		{&net.DNSError{Err: "no such host", Name: "x.invalid", IsNotFound: true}, WebSearchFailUnreachable},
		{&net.OpError{Op: "read", Err: io.ErrUnexpectedEOF}, WebSearchFailTransient},
		{io.EOF, WebSearchFailTransient},
		{&webFetchDenied{WebFetchDeniedNonPublic, "private"}, WebSearchFailBlocked},
	} {
		if got := classifyTransport(c.err); got.Category != c.cat {
			t.Errorf("%v: %s, want %s", c.err, got.Category, c.cat)
		}
	}
}
