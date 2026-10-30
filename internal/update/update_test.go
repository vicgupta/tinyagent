package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		tag, current string
		want         bool
	}{
		{"v0.2.0", "v0.1.0", true},
		{"v0.1.0", "v0.1.0", false},
		{"v0.1.0", "v0.2.0", false},
		{"v1.0.0", "v0.9.9", true},
		{"v0.1.10", "v0.1.9", true},
		{"v0.1.0", "dev", true},
		{"v0.1.0", "v0.1.0-3-gabc123-dirty", false},
		{"garbage", "v0.1.0", false},
	}
	for _, c := range cases {
		if got := newer(c.tag, c.current); got != c.want {
			t.Errorf("newer(%q, %q) = %v, want %v", c.tag, c.current, got, c.want)
		}
	}
}

// fakeRelease serves a latest-release document with one binary and SHA256SUMS.
func fakeRelease(t *testing.T, tag string, bin []byte, sum string) *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			fmt.Fprintf(w, `{"tag_name":%q,"assets":[
				{"name":"tinyagent-linux-arm64","browser_download_url":"%s/bin"},
				{"name":"SHA256SUMS","browser_download_url":"%s/sums"}]}`, tag, srv.URL, srv.URL)
		case "/bin":
			w.Header().Set("Content-Length", strconv.Itoa(len(bin)))
			w.Write(bin)
		case "/sums":
			fmt.Fprintf(w, "%s  tinyagent-linux-amd64\n%s  tinyagent-linux-arm64\n", strings.Repeat("0", 64), sum)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestUpdater(t *testing.T, srv *httptest.Server, current string) *Updater {
	exe := filepath.Join(t.TempDir(), "tinyagent")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Updater{APIURL: srv.URL + "/latest", Current: current, ExePath: exe, Client: srv.Client(), GOOS: "linux", GOARCH: "arm64"}
}

func TestRunInstallsNewerRelease(t *testing.T) {
	bin := []byte("new binary")
	sum := sha256.Sum256(bin)
	u := newTestUpdater(t, fakeRelease(t, "v0.2.0", bin, hex.EncodeToString(sum[:])), "v0.1.0")

	msg, err := u.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "v0.1.0 -> v0.2.0") {
		t.Errorf("msg = %q", msg)
	}
	got, _ := os.ReadFile(u.ExePath)
	if string(got) != "new binary" {
		t.Errorf("binary = %q", got)
	}
	if fi, _ := os.Stat(u.ExePath); fi.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v", fi.Mode())
	}
}

func TestRunUpToDate(t *testing.T) {
	u := newTestUpdater(t, fakeRelease(t, "v0.1.0", nil, ""), "v0.1.0")
	msg, err := u.Run(context.Background())
	if err != nil || !strings.Contains(msg, "up to date") {
		t.Errorf("msg = %q, err = %v", msg, err)
	}
}

func TestRunRejectsBadChecksum(t *testing.T) {
	u := newTestUpdater(t, fakeRelease(t, "v0.2.0", []byte("tampered"), strings.Repeat("a", 64)), "v0.1.0")
	if _, err := u.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v", err)
	}
	if got, _ := os.ReadFile(u.ExePath); string(got) != "old" {
		t.Errorf("binary replaced despite bad checksum: %q", got)
	}
}

func TestRunMissingPlatform(t *testing.T) {
	u := newTestUpdater(t, fakeRelease(t, "v0.2.0", nil, ""), "v0.1.0")
	u.GOOS = "plan9"
	if _, err := u.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "plan9") {
		t.Errorf("err = %v", err)
	}
}
