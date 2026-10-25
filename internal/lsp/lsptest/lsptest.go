// Package lsptest is a fake language server for tests: the test binary
// re-executes itself, so tests need no real language server.
package lsptest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/vicgupta/tinyagent/internal/config"
)

const marker = "tinyagent-fake-lsp"

// Run acts as the fake server and exits when the test binary was started
// by an entry from Server. Call it first in TestMain.
func Run() {
	if len(os.Args) < 2 || os.Args[1] != marker {
		return
	}
	if len(os.Args) > 2 && os.Args[2] == "sleep" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(serve(os.Args[2:]))
}

// Server returns a config entry running the fake server. Each opt is
// key=value or a bare flag:
//
//	log=FILE            append "start" and each received method (didOpen
//	                    and didChange add the text, quoted) to FILE
//	pidfile=FILE        append the server's pid to FILE
//	child               also start a child process (its pid goes to pidfile)
//	envfile=FILE        write the server's environment to FILE
//	nopublish           never publish diagnostics
//	crash               exit right after the first response
//	ignore-shutdown     never answer shutdown and ignore exit
//	hang-init           never answer initialize
//	refs=FILE           answer references with FILE's locations
//	def=FILE            answer definition with FILE's locations
//
// By default every didOpen and didChange publishes one diagnostic per
// line containing "ERR:" (an error) or "WARN:" (a warning): the message is
// the rest of the line and the column is where the marker starts.
func Server(t testing.TB, name string, exts []string, opts ...string) config.LSPServer {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return config.LSPServer{Name: name, Command: exe, Args: append([]string{marker}, opts...), Extensions: exts, StartupTimeoutSeconds: 10}
}

// Loc is one location in a refs or def file: an absolute path and a
// zero-based line and UTF-16 character.
type Loc struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Character int    `json:"character"`
}

// WriteLocs writes locs as a refs or def file.
func WriteLocs(t testing.TB, path string, locs []Loc) {
	t.Helper()
	b, _ := json.Marshal(locs)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

type msg struct {
	ID     *json.RawMessage `json:"id,omitempty"`
	Method string           `json:"method,omitempty"`
	Params json.RawMessage  `json:"params,omitempty"`
}

func serve(args []string) int {
	opt := map[string]string{}
	for _, a := range args {
		k, v, _ := strings.Cut(a, "=")
		opt[k] = v
	}
	_, nopublish := opt["nopublish"]
	_, crash := opt["crash"]
	_, ignoreShutdown := opt["ignore-shutdown"]
	_, hangInit := opt["hang-init"]
	logf := func(s string) {
		if p := opt["log"]; p != "" {
			f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
			if err == nil {
				fmt.Fprintln(f, s)
				f.Close()
			}
		}
	}
	appendPid := func(pid int) {
		if p := opt["pidfile"]; p != "" {
			f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
			if err == nil {
				fmt.Fprintln(f, pid)
				f.Close()
			}
		}
	}
	logf("start")
	appendPid(os.Getpid())
	if _, ok := opt["child"]; ok {
		exe, _ := os.Executable()
		c := exec.Command(exe, marker, "sleep")
		if c.Start() == nil {
			appendPid(c.Process.Pid)
		}
	}
	if p := opt["envfile"]; p != "" {
		os.WriteFile(p, []byte(strings.Join(os.Environ(), "\n")), 0o644)
	}
	fmt.Fprintln(os.Stderr, "fake lsp started")
	r := bufio.NewReader(os.Stdin)
	w := bufio.NewWriter(os.Stdout)
	send := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(b))
		w.Write(b)
		w.Flush()
	}
	responded := false
	respond := func(id *json.RawMessage, result any) {
		send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
		if crash && !responded {
			os.Exit(3)
		}
		responded = true
	}
	publish := func(uri, text string, version int) {
		if nopublish {
			return
		}
		diags := []any{}
		for i, line := range strings.Split(text, "\n") {
			for _, mk := range []struct {
				tag string
				sev int
			}{{"ERR:", 1}, {"WARN:", 2}} {
				if c := strings.Index(line, mk.tag); c >= 0 {
					// Columns are UTF-16 code units, as LSP requires.
					rng := map[string]any{"start": map[string]int{"line": i, "character": len(utf16.Encode([]rune(line[:c])))}, "end": map[string]int{"line": i, "character": len(utf16.Encode([]rune(line)))}}
					diags = append(diags, map[string]any{"range": rng, "severity": mk.sev, "source": "fake", "message": strings.TrimSpace(line[c+len(mk.tag):])})
				}
			}
		}
		send(map[string]any{"jsonrpc": "2.0", "method": "textDocument/publishDiagnostics", "params": map[string]any{"uri": uri, "version": version, "diagnostics": diags}})
	}
	locs := func(file string) any {
		if file == "" {
			return nil
		}
		b, err := os.ReadFile(file)
		if err != nil {
			return nil
		}
		var in []Loc
		json.Unmarshal(b, &in)
		out := []any{}
		for _, l := range in {
			pos := map[string]int{"line": l.Line, "character": l.Character}
			out = append(out, map[string]any{"uri": uri(l.Path), "range": map[string]any{"start": pos, "end": pos}})
		}
		return out
	}
	for {
		body, err := read(r)
		if err != nil {
			return 0
		}
		var m msg
		if json.Unmarshal(body, &m) != nil {
			continue
		}
		var p struct {
			TextDocument struct {
				URI     string `json:"uri"`
				Text    string `json:"text"`
				Version int    `json:"version"`
			} `json:"textDocument"`
			ContentChanges []struct {
				Text string `json:"text"`
			} `json:"contentChanges"`
		}
		json.Unmarshal(m.Params, &p)
		switch m.Method {
		case "initialize":
			logf("initialize")
			if hangInit {
				continue
			}
			respond(m.ID, map[string]any{"capabilities": map[string]any{"textDocumentSync": 1, "definitionProvider": true, "referencesProvider": true}})
		case "textDocument/didOpen":
			logf("didOpen " + strconv.Quote(p.TextDocument.Text))
			publish(p.TextDocument.URI, p.TextDocument.Text, p.TextDocument.Version)
		case "textDocument/didChange":
			text := ""
			if len(p.ContentChanges) > 0 {
				text = p.ContentChanges[len(p.ContentChanges)-1].Text
			}
			logf("didChange " + strconv.Quote(text))
			publish(p.TextDocument.URI, text, p.TextDocument.Version)
		case "textDocument/references":
			logf("references")
			respond(m.ID, locs(opt["refs"]))
		case "textDocument/definition":
			logf("definition")
			respond(m.ID, locs(opt["def"]))
		case "shutdown":
			logf("shutdown")
			if ignoreShutdown {
				continue
			}
			respond(m.ID, nil)
		case "exit":
			logf("exit")
			if ignoreShutdown {
				continue
			}
			return 0
		default:
			logf(m.Method)
			if m.ID != nil {
				respond(m.ID, nil)
			}
		}
	}
}

func uri(path string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

func read(r *bufio.Reader) ([]byte, error) {
	n := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(k, "Content-Length") {
			n, _ = strconv.Atoi(strings.TrimSpace(v))
		}
	}
	if n < 0 {
		return nil, fmt.Errorf("no length")
	}
	b := make([]byte, n)
	_, err := io.ReadFull(r, b)
	return b, err
}
