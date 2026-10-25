package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// client is one running language server process.
type client struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	wmu   sync.Mutex

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan response
	diags   map[string]*fileDiags // by path
	seq     uint64                // publishes received
	signal  chan struct{}         // closed and replaced on each publish
	closing bool
	readErr error

	dead    chan struct{} // closed when the server's output ends
	exited  chan struct{} // closed once the process has been waited for
	waitErr error

	crashOnce sync.Once
}

type response struct {
	result json.RawMessage
	err    error
}

type fileDiags struct {
	seq     uint64
	version *int
	items   []Diagnostic
}

// errExited reports a server that stopped answering.
var errExited = errors.New("language server exited")

// startClient launches def's command in root and completes the initialize
// handshake within timeout. onExit runs (in its own goroutine) when the
// process ends without Close.
func startClient(ctx context.Context, command string, cmdArgs []string, root string, env []string, stderr io.Writer, initOpts map[string]any, timeout time.Duration, onExit func(*client)) (*client, error) {
	cmd := exec.Command(command, cmdArgs...)
	cmd.Dir = root
	cmd.Env = env
	cmd.Stderr = stderr
	setProcessGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &client{
		cmd: cmd, stdin: stdin,
		pending: map[int64]chan response{}, diags: map[string]*fileDiags{},
		signal: make(chan struct{}), dead: make(chan struct{}), exited: make(chan struct{}),
	}
	go c.readLoop(bufio.NewReaderSize(stdout, 64<<10), onExit)

	ictx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	params := map[string]any{
		"processId":  os.Getpid(),
		"clientInfo": map[string]any{"name": "tinyagent"},
		"rootUri":    PathToURI(root),
		"rootPath":   root,
		"workspaceFolders": []any{
			map[string]any{"uri": PathToURI(root), "name": "workspace"},
		},
		"capabilities": map[string]any{
			"general": map[string]any{"positionEncodings": []string{"utf-16"}},
			"workspace": map[string]any{
				"workspaceFolders": true,
				"configuration":    true,
			},
			"textDocument": map[string]any{
				"synchronization":    map[string]any{"didSave": false, "dynamicRegistration": false},
				"publishDiagnostics": map[string]any{"versionSupport": true, "relatedInformation": false},
				"definition":         map[string]any{"linkSupport": true},
				"references":         map[string]any{},
			},
			"window": map[string]any{"workDoneProgress": false},
		},
	}
	if len(initOpts) > 0 {
		params["initializationOptions"] = initOpts
	}
	if _, err := c.call(ictx, "initialize", params); err != nil {
		c.kill()
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("initialize did not finish within %s", timeout)
		}
		return nil, fmt.Errorf("initialize failed: %w", err)
	}
	// A write failure here surfaces as a dead server on the next call.
	c.notify("initialized", map[string]any{})
	return c, nil
}

func (c *client) readLoop(r *bufio.Reader, onExit func(*client)) {
	var err error
	for {
		var body []byte
		body, err = readMessage(r)
		if err != nil {
			break
		}
		var m message
		if json.Unmarshal(body, &m) != nil {
			continue
		}
		c.dispatch(&m)
	}
	c.mu.Lock()
	c.readErr = err
	for id, ch := range c.pending {
		ch <- response{err: errExited}
		delete(c.pending, id)
	}
	closing := c.closing
	c.mu.Unlock()
	close(c.dead)
	waitErr := c.cmd.Wait()
	c.mu.Lock()
	c.waitErr = waitErr
	closing = closing || c.closing
	c.mu.Unlock()
	close(c.exited)
	if !closing {
		// Stop anything the server left running in its group.
		killGroup(c.cmd)
		if onExit != nil {
			onExit(c)
		}
	}
}

// exitReason describes how the server ended.
func (c *client) exitReason() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.waitErr != nil {
		return "exited: " + c.waitErr.Error()
	}
	if c.readErr != nil && !errors.Is(c.readErr, io.EOF) {
		return c.readErr.Error()
	}
	return "exited"
}

func (c *client) dispatch(m *message) {
	switch {
	case m.Method == "" && m.ID != nil:
		var id int64
		if json.Unmarshal(*m.ID, &id) != nil {
			return
		}
		c.mu.Lock()
		ch := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if ch == nil {
			return
		}
		if m.Error != nil {
			ch <- response{err: m.Error}
		} else {
			ch <- response{result: m.Result}
		}
	case m.Method != "" && m.ID != nil:
		// Servers wait for answers to their requests; answer each with an
		// empty result (one null per item for workspace/configuration).
		result := json.RawMessage("null")
		if m.Method == "workspace/configuration" {
			var p struct {
				Items []json.RawMessage `json:"items"`
			}
			json.Unmarshal(m.Params, &p)
			nulls := make([]any, len(p.Items))
			result, _ = json.Marshal(nulls)
		}
		go c.write(reply{JSONRPC: "2.0", ID: *m.ID, Result: result})
	case m.Method == "textDocument/publishDiagnostics":
		var p struct {
			URI         string       `json:"uri"`
			Version     *int         `json:"version"`
			Diagnostics []Diagnostic `json:"diagnostics"`
		}
		if json.Unmarshal(m.Params, &p) != nil {
			return
		}
		path, err := URIToPath(p.URI)
		if err != nil {
			return
		}
		c.mu.Lock()
		c.seq++
		c.diags[path] = &fileDiags{seq: c.seq, version: p.Version, items: p.Diagnostics}
		close(c.signal)
		c.signal = make(chan struct{})
		c.mu.Unlock()
	}
}

func (c *client) write(v any) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return writeMessage(c.stdin, v)
}

func (c *client) notify(method string, params any) error {
	if c.isDead() {
		return errExited
	}
	return c.write(request{JSONRPC: "2.0", Method: method, Params: params})
}

// call sends a request and waits for its response, ctx, or the server
// exiting.
func (c *client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	ch := make(chan response, 1)
	c.mu.Lock()
	if c.isDead() {
		c.mu.Unlock()
		return nil, errExited
	}
	c.nextID++
	id := c.nextID
	c.pending[id] = ch
	c.mu.Unlock()
	if err := c.write(request{JSONRPC: "2.0", ID: &id, Method: method, Params: params}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		if c.isDead() {
			return nil, errExited
		}
		return nil, err
	}
	select {
	case r := <-ch:
		return r.result, r.err
	case <-c.dead:
		return nil, errExited
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		c.notify("$/cancelRequest", map[string]any{"id": id})
		return nil, ctx.Err()
	}
}

func (c *client) isDead() bool {
	select {
	case <-c.dead:
		return true
	default:
		return false
	}
}

// publishSeq is the number of publishes received so far.
func (c *client) publishSeq() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seq
}

// cached returns the latest diagnostics for path, if any were published.
func (c *client) cached(path string) ([]Diagnostic, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d := c.diags[path]
	if d == nil {
		return nil, false
	}
	return d.items, true
}

// waitDiagnostics waits until deadline for a publish for path newer than
// after (and, when the server reports versions, for at least version).
func (c *client) waitDiagnostics(ctx context.Context, path string, after uint64, version int, deadline time.Time) ([]Diagnostic, bool, error) {
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	for {
		c.mu.Lock()
		d, sig := c.diags[path], c.signal
		c.mu.Unlock()
		if d != nil && d.seq > after && (d.version == nil || *d.version >= version) {
			return d.items, true, nil
		}
		select {
		case <-sig:
		case <-c.dead:
			return nil, false, errExited
		case <-timer.C:
			return nil, false, nil
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}
}

// kill stops the process group at once.
func (c *client) kill() {
	c.mu.Lock()
	c.closing = true
	c.mu.Unlock()
	killGroup(c.cmd)
	c.stdin.Close()
	select {
	case <-c.exited:
	case <-time.After(2 * time.Second):
	}
}

// shutdown asks the server to stop, then kills its process group after
// grace whether or not it did.
func (c *client) shutdown(grace time.Duration) {
	c.mu.Lock()
	c.closing = true
	c.mu.Unlock()
	if !c.isDead() {
		ctx, cancel := context.WithTimeout(context.Background(), grace)
		c.call(ctx, "shutdown", nil)
		cancel()
		c.notify("exit", nil)
	}
	c.stdin.Close()
	select {
	case <-c.exited:
	case <-time.After(grace):
	}
	killGroup(c.cmd)
	select {
	case <-c.exited:
	case <-time.After(2 * time.Second):
	}
}

// minimalEnv is PATH, the Windows variables programs need, and the named
// variables; nothing else from the host is forwarded.
func minimalEnv(names []string) []string {
	var env []string
	if p := os.Getenv("PATH"); p != "" {
		env = append(env, "PATH="+p)
	}
	if isWindows {
		for _, k := range []string{"SystemRoot", "WINDIR", "USERPROFILE", "TEMP", "TMP"} {
			if v, ok := os.LookupEnv(k); ok {
				env = append(env, k+"="+v)
			}
		}
	}
	for _, k := range names {
		if v, ok := os.LookupEnv(k); ok && !strings.EqualFold(k, "PATH") {
			env = append(env, k+"="+v)
		}
	}
	return env
}
