package lsp

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

func TestFraming(t *testing.T) {
	var buf bytes.Buffer
	if err := writeMessage(&buf, map[string]any{"jsonrpc": "2.0", "method": "x", "params": "é"}); err != nil {
		t.Fatal(err)
	}
	if err := writeMessage(&buf, map[string]any{"jsonrpc": "2.0", "id": 1, "result": nil}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(buf.String(), "Content-Length: 44\r\n\r\n") {
		t.Fatalf("header: %q", buf.String())
	}
	r := bufio.NewReader(&buf)
	for _, want := range []string{`{"jsonrpc":"2.0","method":"x","params":"é"}`, `{"id":1,"jsonrpc":"2.0","result":null}`} {
		got, err := readMessage(r)
		if err != nil || string(got) != want {
			t.Fatalf("read = %q, %v; want %q", got, err, want)
		}
	}
	for _, bad := range []string{"\r\n{}", "Content-Length: x\r\n\r\n", "Content-Length: 999999999\r\n\r\n", "bogus\r\n\r\n"} {
		if _, err := readMessage(bufio.NewReader(strings.NewReader(bad))); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
	// Other headers are ignored.
	got, err := readMessage(bufio.NewReader(strings.NewReader("Content-Type: x\r\nContent-Length: 2\r\n\r\n{}")))
	if err != nil || string(got) != "{}" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestPositionsAndURIs(t *testing.T) {
	line := "a😀b"
	if got := RuneToUTF16(line, 2); got != 3 {
		t.Errorf("RuneToUTF16 = %d, want 3", got)
	}
	if got := UTF16ToRune(line, 3); got != 2 {
		t.Errorf("UTF16ToRune = %d, want 2", got)
	}
	if got := RuneToUTF16(line, 99); got != 4 {
		t.Errorf("clamped = %d", got)
	}
	p := "/tmp/a dir/x.go"
	u := PathToURI(p)
	if u != "file:///tmp/a%20dir/x.go" {
		t.Errorf("uri = %s", u)
	}
	if back, err := URIToPath(u); err != nil || back != p {
		t.Errorf("path = %s, %v", back, err)
	}
	if _, err := URIToPath("https://x/y"); err == nil {
		t.Error("non-file URI accepted")
	}
	locs, err := parseLocations([]byte(`[{"targetUri":"file:///a.go","targetRange":{"start":{"line":1,"character":0},"end":{"line":1,"character":0}},"targetSelectionRange":{"start":{"line":2,"character":3},"end":{"line":2,"character":4}}}]`))
	if err != nil || len(locs) != 1 || locs[0].Path != "/a.go" || locs[0].Range.Start.Line != 2 {
		t.Errorf("links = %+v, %v", locs, err)
	}
	locs, err = parseLocations([]byte(`{"uri":"file:///b.go","range":{"start":{"line":5,"character":1},"end":{"line":5,"character":2}}}`))
	if err != nil || len(locs) != 1 || locs[0].Path != "/b.go" {
		t.Errorf("single = %+v, %v", locs, err)
	}
	if locs, err := parseLocations([]byte("null")); err != nil || locs != nil {
		t.Errorf("null = %v, %v", locs, err)
	}
}
