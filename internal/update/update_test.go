package update

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
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

func zipEntry(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// serveRelease serves a latest-release document. sums is the SHA256SUMS body;
// an empty sums is built from the asset bytes.
func serveRelease(t *testing.T, tag string, assets map[string][]byte, sums string) *httptest.Server {
	t.Helper()
	if sums == "" {
		var b strings.Builder
		names := make([]string, 0, len(assets))
		for name := range assets {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			sum := sha256.Sum256(assets[name])
			fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(sum[:]), name)
		}
		sums = b.String()
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/latest":
			names := make([]string, 0, len(assets))
			for name := range assets {
				names = append(names, name)
			}
			sort.Strings(names)
			doc := struct {
				TagName string `json:"tag_name"`
				Assets  []struct {
					Name string `json:"name"`
					URL  string `json:"browser_download_url"`
				} `json:"assets"`
			}{TagName: tag}
			for _, name := range names {
				doc.Assets = append(doc.Assets, struct {
					Name string `json:"name"`
					URL  string `json:"browser_download_url"`
				}{name, srv.URL + "/asset/" + name})
			}
			doc.Assets = append(doc.Assets, struct {
				Name string `json:"name"`
				URL  string `json:"browser_download_url"`
			}{"SHA256SUMS", srv.URL + "/sums"})
			json.NewEncoder(w).Encode(doc)
		case r.URL.Path == "/sums":
			ioWrite(w, sums)
		default:
			name := strings.TrimPrefix(r.URL.Path, "/asset/")
			body, ok := assets[name]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.Write(body)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func ioWrite(w http.ResponseWriter, s string) { w.Write([]byte(s)) }

func TestRunWindowsZipInstallsExe(t *testing.T) {
	exe := []byte("windows-binary")
	archive := zipEntry(t, "tinyagent.exe", exe)
	assets := map[string][]byte{"tinyagent-windows-amd64.zip": archive}
	u := newTestUpdater(t, serveRelease(t, "v0.2.0", assets, ""), "v0.1.0")
	u.GOOS, u.GOARCH = "windows", "amd64"
	var out bytes.Buffer
	u.Report = &TextReporter{W: &out, Bar: true}

	msg, err := u.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "v0.1.0 -> v0.2.0") {
		t.Errorf("msg = %q", msg)
	}
	got, _ := os.ReadFile(u.ExePath)
	if !bytes.Equal(got, exe) {
		t.Errorf("installed %q, want exe bytes", got)
	}
	s := out.String()
	sum := sha256.Sum256(archive)
	for _, want := range []string{
		"update: select asset: tinyagent-windows-amd64.zip for windows/amd64\n",
		"update: download checksums: SHA256SUMS",
		"update: download binary: http",
		fmt.Sprintf("update: download binary: received %s (%d bytes)", Size(int64(len(archive))), len(archive)),
		"update: verify checksum: sha256 " + hex.EncodeToString(sum[:]) + " matches SHA256SUMS",
		"update: install: extracted tinyagent.exe; replacing ",
		"#",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, "legacy exe") {
		t.Errorf("zip release reported a legacy exe:\n%s", s)
	}
}

func TestRunWindowsLegacyExeWhenZipMissing(t *testing.T) {
	exe := []byte("legacy-exe")
	assets := map[string][]byte{"tinyagent-windows-amd64.exe": exe}
	u := newTestUpdater(t, serveRelease(t, "v0.2.0", assets, ""), "v0.1.0")
	u.GOOS, u.GOARCH = "windows", "amd64"
	var out bytes.Buffer
	u.Report = &TextReporter{W: &out}

	if _, err := u.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(u.ExePath)
	if !bytes.Equal(got, exe) {
		t.Errorf("installed %q", got)
	}
	if !strings.Contains(out.String(), "update: select asset: tinyagent-windows-amd64.exe for windows/amd64 (legacy exe; no zip in this release)") {
		t.Errorf("got:\n%s", out.String())
	}
	if strings.Contains(out.String(), "extracted tinyagent.exe") {
		t.Errorf("legacy path extracted a zip:\n%s", out.String())
	}
}

func TestRunWindowsPrefersZipWhenBothPresent(t *testing.T) {
	exe := []byte("from-zip")
	assets := map[string][]byte{
		"tinyagent-windows-arm64.zip": zipEntry(t, "tinyagent.exe", exe),
		"tinyagent-windows-arm64.exe": []byte("from-exe"),
	}
	u := newTestUpdater(t, serveRelease(t, "v0.2.0", assets, ""), "v0.1.0")
	u.GOOS, u.GOARCH = "windows", "arm64"
	if _, err := u.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(u.ExePath)
	if !bytes.Equal(got, exe) {
		t.Errorf("installed %q, want zip contents", got)
	}
}

func TestRunWindowsZipRejectsBadChecksum(t *testing.T) {
	archive := zipEntry(t, "tinyagent.exe", []byte("windows-binary"))
	sums := strings.Repeat("ab", 32) + "  tinyagent-windows-amd64.zip\n"
	assets := map[string][]byte{"tinyagent-windows-amd64.zip": archive}
	u := newTestUpdater(t, serveRelease(t, "v0.2.0", assets, sums), "v0.1.0")
	u.GOOS, u.GOARCH = "windows", "amd64"
	_, err := u.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch for tinyagent-windows-amd64.zip") {
		t.Fatalf("err = %v", err)
	}
	var se *StepError
	if !errors.As(err, &se) || se.Step != StepVerify {
		t.Fatalf("err = %v", err)
	}
	if got, _ := os.ReadFile(u.ExePath); string(got) != "old" {
		t.Errorf("binary replaced despite bad checksum: %q", got)
	}
}

func TestRunWindowsZipRejectsBadLayout(t *testing.T) {
	for _, name := range []string{"readme.txt", "dir/tinyagent.exe", "../tinyagent.exe", `..\tinyagent.exe`} {
		archive := zipEntry(t, name, []byte("nope"))
		assets := map[string][]byte{"tinyagent-windows-amd64.zip": archive}
		u := newTestUpdater(t, serveRelease(t, "v0.2.0", assets, ""), "v0.1.0")
		u.GOOS, u.GOARCH = "windows", "amd64"
		_, err := u.Run(context.Background())
		if err == nil || !strings.Contains(err.Error(), "update failed at install:") || !strings.Contains(err.Error(), "tinyagent.exe") {
			t.Errorf("%s: err = %v", name, err)
		}
		if got, _ := os.ReadFile(u.ExePath); string(got) != "old" {
			t.Errorf("%s: binary replaced: %q", name, got)
		}
	}
}
