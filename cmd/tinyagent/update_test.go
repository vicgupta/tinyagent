package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/update"
)

func fakeUpToDate(t *testing.T) *update.Updater {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tag_name":"v0.1.0","assets":[]}`))
	}))
	t.Cleanup(srv.Close)
	return &update.Updater{APIURL: srv.URL, Current: "v0.1.0", ExePath: "/x/tinyagent", Client: srv.Client(), GOOS: "linux", GOARCH: "amd64"}
}

func TestRunUpdateText(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runUpdate(fakeUpToDate(t), false, &stdout, &stderr, false); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if stdout.String() != "already up to date (v0.1.0)\n" {
		t.Errorf("stdout %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "update: current version: v0.1.0 at /x/tinyagent\n") || !strings.Contains(stderr.String(), "already up to date") {
		t.Errorf("stderr %q", stderr.String())
	}
}

func TestRunUpdateJSONKeepsStdoutClean(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runUpdate(fakeUpToDate(t), true, &stdout, &stderr, true); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr %q", stderr.String())
	}
	sc := bufio.NewScanner(&stdout)
	var types []string
	for sc.Scan() {
		var ev map[string]string
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("not JSON: %q", sc.Text())
		}
		types = append(types, ev["type"])
	}
	if len(types) < 3 || types[0] != "update.step" || types[len(types)-1] != "update.result" {
		t.Errorf("events %v", types)
	}
}

func TestRunUpdateErrorNamesStep(t *testing.T) {
	u := fakeUpToDate(t)
	u.APIURL += "/nope"
	u.Client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 503, Status: "503 Service Unavailable", Body: http.NoBody}, nil
	})}
	var stdout, stderr bytes.Buffer
	if code := runUpdate(u, false, &stdout, &stderr, false); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stderr.String(), "error: update failed at query latest release: ") {
		t.Errorf("stderr %q", stderr.String())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
