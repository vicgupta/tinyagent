package tools

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// wfServer starts a loopback server and a fetcher that allows only it.
func wfServer(t *testing.T, h http.HandlerFunc, extra ...string) (*httptest.Server, *WebFetcher) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	f, err := NewWebFetcher(append([]string{srv.Listener.Addr().String()}, extra...), 0, 5*time.Second, "tinyagent/test")
	if err != nil {
		t.Fatal(err)
	}
	return srv, f
}

func port(srv *httptest.Server) int { return srv.Listener.Addr().(*net.TCPAddr).Port }

// named makes the fetcher resolve every name to addrs and dial srv, so
// tests can use host names without DNS.
func named(f *WebFetcher, srv *httptest.Server, addrs ...string) *[]string {
	var dialed []string
	f.Resolve = func(ctx context.Context, host string) ([]netip.Addr, error) {
		var out []netip.Addr
		for _, a := range addrs {
			out = append(out, netip.MustParseAddr(a))
		}
		return out, nil
	}
	f.Dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialed = append(dialed, addr)
		return net.Dial("tcp", srv.Listener.Addr().String())
	}
	return &dialed
}

const testPage = `<!doctype html><html><head><title>Doc</title><style>body{color:red}</style>
<script>alert("x")</script></head><body><nav><a href="/home">Home</a></nav>
<h1>Package  fmt</h1><p>Package fmt implements <b>formatted</b> I/O &amp; more.
See <a href="/pkg/io">the io package</a>.</p>
<ul><li>Println</li><li>Printf</li></ul>
<pre>func main() {
	fmt.Println("hi")
}</pre><!-- comment --></body></html>`

func TestWebFetchHTMLToText(t *testing.T) { // AC4
	srv, f := wfServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "tinyagent/test" || r.Method != http.MethodGet {
			t.Errorf("request %s, User-Agent %q", r.Method, r.Header.Get("User-Agent"))
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Errorf("credentials sent: %v", r.Header)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, testPage)
	})
	out, err := f.Fetch(context.Background(), srv.URL+"/pkg/fmt")
	if err != nil {
		t.Fatal(err)
	}
	head := fmt.Sprintf("[web_fetch %s/pkg/fmt → 200 text/html, %d bytes, ", srv.URL, len(testPage))
	if !strings.HasPrefix(out, head) {
		t.Fatalf("header: %q", out)
	}
	for _, want := range []string{
		"# Package fmt\n", "Package fmt implements formatted I/O & more. See [the io package](" + srv.URL + "/pkg/io).",
		"- Println\n- Printf", "```\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n```",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, bad := range []string{"alert", "color:red", "Home", "comment", "<"} {
		if strings.Contains(out, bad) {
			t.Errorf("output contains %q:\n%s", bad, out)
		}
	}
	recs := f.TakeRecords()
	if len(recs) != 1 || recs[0].Status != 200 || recs[0].ContentType != "text/html" || recs[0].Bytes != len(testPage) || recs[0].FinalURL != srv.URL+"/pkg/fmt" {
		t.Errorf("records = %+v", recs)
	}
	if len(f.TakeRecords()) != 0 {
		t.Error("records not drained")
	}
}

func TestWebFetchContentTypes(t *testing.T) { // AC4
	bodies := map[string][2]string{
		"/txt":  {"text/plain", "  plain\n\ntext  "},
		"/json": {"application/json", `{"a": [1, 2]}`},
		"/xml":  {"application/atom+xml", "<feed><title>x</title></feed>"},
		"/png":  {"image/png", "\x89PNG"},
		"/none": {"", "no type given"},
	}
	srv, f := wfServer(t, func(w http.ResponseWriter, r *http.Request) {
		b := bodies[r.URL.Path]
		w.Header()["Content-Type"] = []string{b[0]}
		if b[0] == "" {
			w.Header()["Content-Type"] = nil
		}
		io.WriteString(w, b[1])
	})
	for _, p := range []string{"/txt", "/json", "/xml", "/none"} {
		out, err := f.Fetch(context.Background(), srv.URL+p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if _, body, _ := strings.Cut(out, "]\n"); body != bodies[p][1] {
			t.Errorf("%s: body %q", p, body)
		}
	}
	if _, err := f.Fetch(context.Background(), srv.URL+"/png"); err == nil || !strings.Contains(err.Error(), `unsupported content type "image/png"`) {
		t.Errorf("png: %v", err)
	}
}

func TestWebFetchAllowlist(t *testing.T) { // AC5
	var hits atomic.Int32
	srv, f := wfServer(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1); io.WriteString(w, "ok") })
	other := fmt.Sprintf("http://127.0.0.2:%d/", port(srv))
	_, err := f.Fetch(context.Background(), other)
	if err == nil || !strings.Contains(err.Error(), "127.0.0.2") || !strings.Contains(err.Error(), "web_fetch.allowed_domains") {
		t.Fatalf("unlisted host: %v", err)
	}
	if hits.Load() != 0 {
		t.Error("request reached the server")
	}
	if r := f.TakeRecords(); len(r) != 1 || r[0].DeniedReason != WebFetchDeniedHost {
		t.Errorf("records = %+v", r)
	}
	for _, u := range []string{"ftp://example.com/x", "file:///etc/passwd", "http://u:p@" + srv.Listener.Addr().String() + "/"} {
		if _, err := f.Fetch(context.Background(), u); err == nil {
			t.Errorf("%s fetched", u)
		}
	}
	if hits.Load() != 0 {
		t.Error("request reached the server")
	}

	// Wildcards match subdomains only, like sandbox_allowed_domains.
	p := port(srv)
	wf, err := NewWebFetcher([]string{fmt.Sprintf("*.example.com:%d", p)}, 0, 5*time.Second, "ua")
	if err != nil {
		t.Fatal(err)
	}
	dialed := named(wf, srv, "93.184.216.34")
	if _, err := wf.Fetch(context.Background(), fmt.Sprintf("http://docs.example.com:%d/", p)); err != nil {
		t.Errorf("subdomain: %v", err)
	}
	if len(*dialed) != 1 || (*dialed)[0] != fmt.Sprintf("93.184.216.34:%d", p) {
		t.Errorf("dialed %v, want the checked address", *dialed)
	}
	if _, err := wf.Fetch(context.Background(), fmt.Sprintf("http://example.com:%d/", p)); err == nil {
		t.Error("apex matched a wildcard")
	}
	if _, err := NewWebFetcher([]string{"*"}, 0, 0, "ua"); err == nil {
		t.Error("bare * accepted")
	}
	if _, err := NewWebFetcher(nil, 0, 0, "ua"); err == nil {
		t.Error("empty allowlist accepted")
	}
}

func TestWebFetchNonPublicAddresses(t *testing.T) { // AC5
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer srv.Close()
	p := port(srv)
	for _, a := range []string{"127.0.0.1", "10.1.2.3", "169.254.169.254", "192.168.0.1", "::1"} {
		f, _ := NewWebFetcher([]string{fmt.Sprintf("internal.test:%d", p)}, 0, 5*time.Second, "ua")
		dialed := named(f, srv, a)
		_, err := f.Fetch(context.Background(), fmt.Sprintf("http://internal.test:%d/", p))
		if err == nil || !strings.Contains(err.Error(), WebFetchDeniedNonPublic) || len(*dialed) != 0 {
			t.Errorf("%s: err %v, dialed %v", a, err, *dialed)
		}
	}
	// Listing the literal address allows it.
	f, _ := NewWebFetcher([]string{fmt.Sprintf("internal.test:%d", p), fmt.Sprintf("10.1.2.3:%d", p)}, 0, 5*time.Second, "ua")
	named(f, srv, "10.1.2.3")
	if _, err := f.Fetch(context.Background(), fmt.Sprintf("http://internal.test:%d/", p)); err != nil {
		t.Errorf("listed literal: %v", err)
	}
}

func TestWebFetchRedirects(t *testing.T) { // AC5
	var srv *httptest.Server
	srv, f := wfServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/hop/"):
			var n int
			fmt.Sscanf(r.URL.Path, "/hop/%d", &n)
			if n == 0 {
				io.WriteString(w, "landed")
				return
			}
			http.Redirect(w, r, fmt.Sprintf("/hop/%d", n-1), http.StatusFound)
		case r.URL.Path == "/away":
			http.Redirect(w, r, fmt.Sprintf("http://127.0.0.3:%d/", port(srv)), http.StatusMovedPermanently)
		case r.URL.Path == "/meta":
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusTemporaryRedirect)
		}
	})
	out, err := f.Fetch(context.Background(), srv.URL+"/hop/5")
	if err != nil || !strings.HasSuffix(out, "\nlanded") || !strings.Contains(out, "final URL "+srv.URL+"/hop/0]") {
		t.Fatalf("5 redirects: %q, %v", out, err)
	}
	if _, err := f.Fetch(context.Background(), srv.URL+"/hop/6"); err == nil || !strings.Contains(err.Error(), "more than 5 redirects") {
		t.Errorf("6 redirects: %v", err)
	}
	if _, err := f.Fetch(context.Background(), srv.URL+"/away"); err == nil || !strings.Contains(err.Error(), "127.0.0.3") {
		t.Errorf("redirect off the allowlist: %v", err)
	}
	if _, err := f.Fetch(context.Background(), srv.URL+"/meta"); err == nil || !strings.Contains(err.Error(), "web_fetch.allowed_domains") {
		t.Errorf("redirect to metadata: %v", err)
	}
	recs := f.TakeRecords()
	if len(recs) != 4 || recs[1].DeniedReason != WebFetchDeniedRedirects || recs[2].DeniedReason != WebFetchDeniedHost {
		t.Errorf("records = %+v", recs)
	}
}

func TestWebFetchLimits(t *testing.T) { // AC6
	release := make(chan struct{})
	srv, f := wfServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/big":
			w.Header().Set("Content-Type", "text/plain")
			io.WriteString(w, strings.Repeat("a", 5000))
		case "/slow":
			w.Header().Set("Content-Type", "text/plain")
			w.(http.Flusher).Flush()
			<-release
		case "/missing":
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, "<h1>Not here</h1>")
		}
	})
	defer close(release)
	f.MaxBytes = 1000
	out, err := f.Fetch(context.Background(), srv.URL+"/big")
	if err != nil || !strings.Contains(out, "1,000 bytes, 1 lines, truncated at max_bytes 1,000]") || len(out) > 1200 {
		t.Fatalf("big: %q, %v", out, err)
	}
	if r := f.TakeRecords(); len(r) != 1 || !r[0].Truncated || r[0].Bytes != 1000 {
		t.Errorf("records = %+v", r)
	}
	f.Timeout = 200 * time.Millisecond
	start := time.Now()
	if _, err := f.Fetch(context.Background(), srv.URL+"/slow"); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("slow: %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Error("timeout not enforced")
	}
	f.Timeout = 5 * time.Second
	if _, err := f.Fetch(context.Background(), srv.URL+"/missing"); err == nil || !strings.Contains(err.Error(), "HTTP 404 Not Found") || !strings.Contains(err.Error(), "Not here") {
		t.Errorf("404: %v", err)
	}
}

func TestWebFetchIgnoresProxyEnv(t *testing.T) { // AC6
	var proxied atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxied.Add(1) }))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	t.Setenv("NO_PROXY", "")
	srv, f := wfServer(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "direct") })
	if out, err := f.Fetch(context.Background(), srv.URL); err != nil || !strings.HasSuffix(out, "direct") || proxied.Load() != 0 {
		t.Errorf("out %q, err %v, proxied %d", out, err, proxied.Load())
	}
}

func TestWebFetchExecutor(t *testing.T) { // AC3, AC7
	srv, f := wfServer(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "hello") })
	e := &Executor{Root: t.TempDir(), ReadOnly: true, WebFetch: f, MaxOutputChars: 10000}
	if !e.Permits(WebFetchToolName) {
		t.Fatal("web_fetch not offered in read-only mode")
	}
	out, err := e.run(context.Background(), WebFetchToolName, fmt.Sprintf(`{"url":%q}`, srv.URL))
	if err != nil || !strings.HasSuffix(out, "\nhello") {
		t.Errorf("out %q, err %v", out, err)
	}
	off := &Executor{Root: t.TempDir()}
	if off.Permits(WebFetchToolName) {
		t.Error("web_fetch offered while off")
	}
	if _, err := off.run(context.Background(), WebFetchToolName, `{"url":"http://x"}`); err == nil {
		t.Error("web_fetch ran while off")
	}
}

func TestHTMLToTextEdgeCases(t *testing.T) {
	base, _ := url.Parse("https://example.com/a/b")
	for _, c := range []struct{ in, want string }{
		{"a  b\n\n c", "a b c"},
		{"<p>x</p><p>y</p>", "x\n\ny"},
		{`<a href="#top">top</a> <a href="javascript:x()">js</a> <a href="c">rel</a>`, "top js [rel](https://example.com/a/c)"},
		{`<a href="/x"><img src=y></a>after`, "after"},
		{"1 < 2 &lt; 3", "1 < 2 < 3"},
		{"<h2>T</h2>text<br>more", "## T\n\ntext\nmore"},
		{"<code>x()</code> call", "`x()` call"},
		{"<div title='a>b'>q</div>", "q"},
		{"<nav><nav>a</nav>b</nav>c", "c"},
		{"<script>if (a<b) {}</script>ok", "ok"},
		{"unterminated <b", "unterminated"},
	} {
		if got := htmlToText(c.in, base); got != c.want {
			t.Errorf("htmlToText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
