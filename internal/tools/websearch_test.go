package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const ddgPage = `<html><body>
<div class="result results_links results_links_deep result--ad">
 <div class="links_main"><a rel="nofollow" class="result__a" href="https://duckduckgo.com/y.js?ad=1">Ad</a></div><div class="clear"></div></div>
<div class="result results_links results_links_deep web-result ">
 <div class="links_main links_deep result__body">
  <h2 class="result__title"><a rel="nofollow" class="result__a" href="https://pkg.go.dev/context">context package - <b>context</b> - Go Packages</a></h2>
  <a class="result__snippet" href="https://pkg.go.dev/context">Package <b>context</b> defines the Context type &amp; more.</a>
 </div><div class="clear"></div></div>
<div class="result results_links results_links_deep web-result ">
 <div class="links_main links_deep result__body">
  <h2 class="result__title"><a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2Fblog%2Fcontext&amp;rut=abc">Go Concurrency Patterns: Context</a></h2>
  <a class="result__snippet" href="x">Blog post.</a>
 </div><div class="clear"></div></div>
</body></html>`

const exaSSE = "event: message\ndata: " + `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"Title: Context docs\nURL: https://pkg.go.dev/context\nPublished: 2026-01-01\nAuthor: N/A\nHighlights:\nPackage context defines\nthe Context type.\n\n---\n\nTitle: Blog\nURL: https://go.dev/blog/context\nText: # Heading\nbody"}]}}` + "\n\n"

func TestParseDuckDuckGo(t *testing.T) {
	got := parseDuckDuckGo(ddgPage).results
	if len(got) != 2 {
		t.Fatalf("got %d results: %+v", len(got), got)
	}
	if got[0].URL != "https://pkg.go.dev/context" || got[0].Title != "context package - context - Go Packages" || got[0].Snippet != "Package context defines the Context type & more." {
		t.Errorf("first = %+v", got[0])
	}
	if got[1].URL != "https://go.dev/blog/context" {
		t.Errorf("uddg not unwrapped: %+v", got[1])
	}
}

func TestParseExa(t *testing.T) {
	for _, body := range []string{exaSSE, strings.TrimSuffix(strings.TrimPrefix(exaSSE, "event: message\ndata: "), "\n\n")} {
		got, err := parseExa([]byte(body))
		if err != nil || len(got) != 2 {
			t.Fatalf("got %+v, %v", got, err)
		}
		if got[0].Title != "Context docs" || got[0].URL != "https://pkg.go.dev/context" || got[0].Snippet != "Package context defines the Context type." {
			t.Errorf("first = %+v", got[0])
		}
		if got[1].Snippet != "Heading body" {
			t.Errorf("second = %+v", got[1])
		}
	}
	if _, err := parseExa([]byte(`{"jsonrpc":"2.0","id":1,"result":{"isError":true,"content":[{"type":"text","text":"You've hit Exa's free rate limit"}]}}`)); err == nil || !strings.Contains(err.Error(), "rate_limited") {
		t.Errorf("isError reply: %v", err)
	}
}

type fakeSearch struct {
	ddg, exa, searx http.HandlerFunc
}

func (f fakeSearch) start(t *testing.T) (ddg, exa, searx string) {
	t.Helper()
	srv := func(h http.HandlerFunc) string {
		if h == nil {
			h = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }
		}
		s := httptest.NewServer(h)
		t.Cleanup(s.Close)
		return s.URL
	}
	return srv(f.ddg), srv(f.exa), srv(f.searx)
}

func newTestSearcher(t *testing.T, f fakeSearch, providers ...string) *WebSearcher {
	t.Helper()
	ddg, exa, searx := f.start(t)
	s, err := NewWebSearcher(WebSearchOptions{
		Providers: providers, SearXNGURL: searx, MaxResults: 6, Timeout: 2 * time.Second, UserAgent: "tinyagent/test",
		DuckDuckGoURL: ddg + "/html/", ExaURL: exa + "/mcp", AllowNonPublicEndpoints: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	noSleep(s)
	return s
}

func ddgOK(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.UserAgent() != "tinyagent/test" || r.Header.Get("Cookie") != "" {
			t.Errorf("ddg request %s UA %q cookie %q", r.Method, r.UserAgent(), r.Header.Get("Cookie"))
		}
		r.ParseForm()
		if r.PostForm.Get("q") != "go context" {
			t.Errorf("q = %q", r.PostForm.Get("q"))
		}
		io.WriteString(w, ddgPage)
	}
}

func exaOK(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.Method != "tools/call" || req.Params.Name != "web_search_exa" || req.Params.Arguments["query"] != "go context" {
			t.Errorf("exa request %+v", req)
		}
		if r.URL.RawQuery != "" || r.Header.Get("Authorization") != "" {
			t.Errorf("exa request carries a key: %q %q", r.URL.RawQuery, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, exaSSE)
	}
}

func TestWebSearchPrimaryAnswers(t *testing.T) {
	s := newTestSearcher(t, fakeSearch{ddg: ddgOK(t), exa: func(w http.ResponseWriter, r *http.Request) { t.Error("backup called") }}, "duckduckgo", "exa")
	out, err := s.Search(context.Background(), "  go   context ")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, `[web_search "go context" → duckduckgo, 2 results]`+"\n1. context package") || !strings.Contains(out, "   https://go.dev/blog/context\n") {
		t.Errorf("out:\n%s", out)
	}
	recs := s.TakeRecords()
	if len(recs) != 1 || recs[0].Provider != "duckduckgo" || recs[0].Results != 2 || len(recs[0].Attempts) != 1 {
		t.Errorf("records %+v", recs)
	}
}

func TestWebSearchFallsBack(t *testing.T) {
	for _, c := range []struct {
		name string
		ddg  http.HandlerFunc
		why  string
	}{
		{"429", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429) }, "rate_limited"},
		{"202 challenge", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(202)
			io.WriteString(w, "<div class=anomaly-modal>")
		}, "challenged"},
		{"5xx", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }, "HTTP 503"},
		{"empty", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "<html>No results.</html>") }, "empty_results"},
		{"timeout", func(w http.ResponseWriter, r *http.Request) { time.Sleep(3 * time.Second) }, "timed out"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newTestSearcher(t, fakeSearch{ddg: c.ddg, exa: exaOK(t)}, "duckduckgo", "exa")
			out, err := s.Search(context.Background(), "go context")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(out, `[web_search "go context" → exa, 2 results; fell back after duckduckgo (`) || !strings.Contains(out, c.why) {
				t.Errorf("out:\n%s", out)
			}
			r := s.TakeRecords()[0]
			if r.Provider != "exa" || len(r.Attempts) != 2 || !strings.Contains(r.Attempts[0].Category+": "+r.Attempts[0].Error, c.why) {
				t.Errorf("record %+v", r)
			}
		})
	}
}

func TestWebSearchAllFail(t *testing.T) {
	s := newTestSearcher(t, fakeSearch{
		ddg: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429) },
		exa: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429) },
	}, "duckduckgo", "exa")
	_, err := s.Search(context.Background(), "go context")
	if err == nil || !strings.Contains(err.Error(), "duckduckgo (2 attempts, rate_limited: HTTP 429") || !strings.Contains(err.Error(), "exa (2 attempts, rate_limited: HTTP 429") {
		t.Fatalf("err = %v", err)
	}
	if _, err := s.Search(context.Background(), " "); err == nil {
		t.Error("empty query accepted")
	}
	if _, err := s.Search(context.Background(), strings.Repeat("a", 401)); err == nil {
		t.Error("long query accepted")
	}
}

func TestWebSearchSearXNG(t *testing.T) {
	s := newTestSearcher(t, fakeSearch{searx: func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" || r.URL.Query().Get("format") != "json" || r.URL.Query().Get("q") != "go context" {
			t.Errorf("searxng request %s", r.URL)
		}
		io.WriteString(w, `{"results":[{"title":"A","url":"https://a.example/","content":"aa"},{"title":"bad","url":"javascript:x"},{"title":"B","url":"https://b.example/","content":"bb"}]}`)
	}}, "searxng", "duckduckgo")
	out, err := s.Search(context.Background(), "go context")
	if err != nil || !strings.HasPrefix(out, `[web_search "go context" → searxng, 2 results]`) {
		t.Fatalf("out %q err %v", out, err)
	}
}

func TestWebSearchMaxResults(t *testing.T) {
	ddg, _, _ := fakeSearch{ddg: ddgOK(t)}.start(t)
	s, _ := NewWebSearcher(WebSearchOptions{Providers: []string{"duckduckgo"}, MaxResults: 1, DuckDuckGoURL: ddg, AllowNonPublicEndpoints: true, UserAgent: "tinyagent/test"})
	out, err := s.Search(context.Background(), "go context")
	if err != nil || !strings.Contains(out, "1 results") || strings.Contains(out, "\n2. ") {
		t.Fatalf("out %q err %v", out, err)
	}
}

// Only the configured SearXNG host may be private; the public providers
// keep web_fetch's address rules.
func TestWebSearchBlocksNonPublic(t *testing.T) {
	ddg, _, searx := fakeSearch{ddg: ddgOK(t), searx: func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"results":[{"title":"A","url":"https://a.example/"}]}`)
	}}.start(t)
	s, err := NewWebSearcher(WebSearchOptions{Providers: []string{"duckduckgo", "searxng"}, DuckDuckGoURL: ddg, SearXNGURL: searx, UserAgent: "tinyagent/test"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.Search(context.Background(), "go context")
	if err != nil || !strings.Contains(out, "→ searxng") || !strings.Contains(out, "loopback, link-local or private") {
		t.Fatalf("out %q err %v", out, err)
	}
}

func TestNewWebSearcherErrors(t *testing.T) {
	for _, o := range []WebSearchOptions{
		{},
		{Providers: []string{"brave"}},
		{Providers: []string{"searxng"}},
		{Providers: []string{"searxng"}, SearXNGURL: "ftp://x"},
		{Providers: []string{"searxng"}, SearXNGURL: "http://u:p@x"},
	} {
		if _, err := NewWebSearcher(o); err == nil {
			t.Errorf("%+v accepted", o)
		}
	}
}

func TestWebSearchProxyIgnored(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	s := newTestSearcher(t, fakeSearch{ddg: ddgOK(t)}, "duckduckgo")
	if _, err := s.Search(context.Background(), "go context"); err != nil {
		t.Fatal(err)
	}
	_ = url.Values{}
}
