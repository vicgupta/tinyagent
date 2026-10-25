// Package lsp is a small Language Server Protocol client: it starts the
// configured servers lazily over stdio, keeps the files it has shown them
// in sync with disk, and collects diagnostics, definitions and references.
package lsp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vicgupta/tinyagent/internal/config"
)

// Server states reported in events.
const (
	StateStarted     = "started"
	StateUnavailable = "unavailable"
	StateCrashed     = "crashed"
	StateStopped     = "stopped"
)

// Event is a server lifecycle change (the lsp.server run event).
type Event struct {
	Server string
	State  string
	Reason string
}

// maxRestarts is how often a crashed server is restarted in one run.
const maxRestarts = 1

// requestTimeout bounds one definition or references request.
const requestTimeout = 10 * time.Second

// shutdownGrace is how long Close waits for a server to stop by itself.
const shutdownGrace = 2 * time.Second

// maxFileBytes caps a file sent to a server.
const maxFileBytes = 4 << 20

// maxLogBytes caps one server's stderr log; maxLogFiles is how many logs
// the log directory keeps.
const (
	maxLogBytes = 1 << 20
	maxLogFiles = 50
)

// ErrNoServer is returned for a file no configured server handles.
var ErrNoServer = errors.New("no lsp server configured")

// Options configures a Manager.
type Options struct {
	// Root is the workspace root: the server's working directory and
	// root URI.
	Root    string
	Servers []config.LSPServer
	// LogDir receives one stderr log per server and run ("" discards
	// server stderr). LogPrefix starts each log's file name.
	LogDir    string
	LogPrefix string
}

// Manager owns the configured servers for one run. It is safe for
// concurrent use.
type Manager struct {
	root    string
	servers []*server
	byExt   map[string]*server
	logDir  string
	prefix  string

	mu     sync.Mutex
	events []Event
	closed bool
}

type server struct {
	def config.LSPServer

	mu          sync.Mutex
	client      *client
	docs        map[string]*doc // open files by path
	restarts    int
	unavailable string // why the server is unavailable for the run
	log         *limitedFile
}

type doc struct {
	version int
	text    string
}

// New returns a Manager; no server starts until a file it handles is used.
func New(o Options) *Manager {
	m := &Manager{root: o.Root, byExt: map[string]*server{}, logDir: o.LogDir, prefix: o.LogPrefix}
	for _, def := range o.Servers {
		s := &server{def: def}
		m.servers = append(m.servers, s)
		for _, e := range def.Extensions {
			m.byExt[strings.ToLower(e)] = s
		}
	}
	return m
}

// Names returns the configured server names in config order.
func (m *Manager) Names() []string {
	var out []string
	for _, s := range m.servers {
		out = append(out, s.def.Name)
	}
	return out
}

// ServerFor returns the name of the server handling path, if any.
func (m *Manager) ServerFor(path string) (string, bool) {
	s := m.byExt[strings.ToLower(filepath.Ext(path))]
	if s == nil {
		return "", false
	}
	return s.def.Name, true
}

// TakeEvents returns and clears the lifecycle events recorded so far.
func (m *Manager) TakeEvents() []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	ev := m.events
	m.events = nil
	return ev
}

func (m *Manager) record(name, state, reason string) {
	m.mu.Lock()
	m.events = append(m.events, Event{Server: name, State: state, Reason: reason})
	m.mu.Unlock()
}

// UnavailableError reports a server that can't be used for the rest of
// the run.
type UnavailableError struct {
	Server string
	Reason string
}

func (e *UnavailableError) Error() string {
	return fmt.Sprintf("lsp server %s is unavailable: %s", e.Server, e.Reason)
}

// ensure returns s's running client, starting it, or restarting it once
// after a crash. Call with s.mu held.
func (m *Manager) ensure(ctx context.Context, s *server) (*client, error) {
	if s.unavailable != "" {
		return nil, &UnavailableError{s.def.Name, s.unavailable}
	}
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return nil, &UnavailableError{s.def.Name, "the run is over"}
	}
	if s.client != nil && !s.client.isDead() {
		return s.client, nil
	}
	if s.client != nil {
		// Report the crash before deciding on a restart, so events stay in
		// order even if the exit has not been fully reaped yet.
		select {
		case <-s.client.exited:
		case <-time.After(time.Second):
		}
		m.noteCrash(s.def.Name, s.client)
		s.client, s.docs = nil, nil
		if s.restarts >= maxRestarts {
			s.unavailable = "crashed again after a restart"
			m.record(s.def.Name, StateUnavailable, s.unavailable)
			return nil, &UnavailableError{s.def.Name, s.unavailable}
		}
		s.restarts++
	}
	timeout := time.Duration(s.def.StartupTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = config.DefaultLSPStartupTimeoutSeconds * time.Second
	}
	name := s.def.Name
	c, err := startClient(ctx, s.def.Command, s.def.Args, m.root, minimalEnv(s.def.Env), m.stderr(s), s.def.InitializationOptions, timeout, func(cl *client) {
		m.noteCrash(name, cl)
	})
	if err != nil {
		if ctx.Err() != nil {
			// The call was cancelled, not the server's fault; try again
			// next time.
			return nil, ctx.Err()
		}
		s.unavailable = err.Error()
		m.record(name, StateUnavailable, s.unavailable)
		return nil, &UnavailableError{name, s.unavailable}
	}
	s.client, s.docs = c, map[string]*doc{}
	m.record(name, StateStarted, "")
	return c, nil
}

// noteCrash records c's crash once.
func (m *Manager) noteCrash(name string, c *client) {
	if c == nil {
		return
	}
	c.crashOnce.Do(func() { m.record(name, StateCrashed, c.exitReason()) })
}

// stderr is s's log writer, opened on first start.
func (m *Manager) stderr(s *server) io.Writer {
	if m.logDir == "" {
		return io.Discard
	}
	if s.log == nil {
		if err := os.MkdirAll(m.logDir, 0o700); err != nil {
			return io.Discard
		}
		pruneLogs(m.logDir, maxLogFiles-1)
		f, err := os.OpenFile(filepath.Join(m.logDir, m.prefix+s.def.Name+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return io.Discard
		}
		s.log = &limitedFile{f: f, left: maxLogBytes}
	}
	return s.log
}

// sync shows path's current disk content to the server: didOpen the first
// time, didChange when it changed, didClose when the file is gone. It
// reports whether anything was sent and the new version. Call with s.mu
// held.
func (s *server) sync(c *client, path string) (bool, int, error) {
	fi, err := os.Stat(path)
	if err == nil && fi.Size() > maxFileBytes {
		return false, 0, fmt.Errorf("%s is larger than %d bytes", path, maxFileBytes)
	}
	data, err := os.ReadFile(path)
	d := s.docs[path]
	if err != nil {
		if d != nil {
			delete(s.docs, path)
			c.notify("textDocument/didClose", map[string]any{"textDocument": map[string]any{"uri": PathToURI(path)}})
		}
		return false, 0, err
	}
	text := string(data)
	switch {
	case d == nil:
		d = &doc{version: 1, text: text}
		s.docs[path] = d
		return true, 1, c.notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{
			"uri": PathToURI(path), "languageId": languageID(path), "version": 1, "text": text,
		}})
	case d.text != text:
		d.version++
		d.text = text
		return true, d.version, c.notify("textDocument/didChange", map[string]any{
			"textDocument":   map[string]any{"uri": PathToURI(path), "version": d.version},
			"contentChanges": []any{map[string]any{"text": text}},
		})
	}
	return false, d.version, nil
}

// resync re-sends every open file whose disk content changed (or that
// was removed), except skip.
func (s *server) resync(c *client, skip string) {
	paths := make([]string, 0, len(s.docs))
	for p := range s.docs {
		if p != skip {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	for _, p := range paths {
		s.sync(c, p)
	}
}

// FileDiagnostics is the outcome for one file.
type FileDiagnostics struct {
	Path   string
	Server string
	Items  []Diagnostic
	// Ready is false when the server published nothing in time.
	Ready bool
	Err   error
}

// Diagnostics syncs each path and waits up to wait (for all of them
// together) for the server's diagnostics. Paths no server handles are
// skipped. A file whose content the server already has returns the last
// published diagnostics at once.
func (m *Manager) Diagnostics(ctx context.Context, paths []string, wait time.Duration) []FileDiagnostics {
	deadline := time.Now().Add(wait)
	type pendingFile struct {
		s       *server
		c       *client
		after   uint64
		version int
		i       int
	}
	var out []FileDiagnostics
	var waits []pendingFile
	for _, p := range paths {
		s := m.byExt[strings.ToLower(filepath.Ext(p))]
		if s == nil {
			continue
		}
		fd := FileDiagnostics{Path: p, Server: s.def.Name}
		s.mu.Lock()
		c, err := m.ensure(ctx, s)
		if err != nil {
			s.mu.Unlock()
			fd.Err = err
			out = append(out, fd)
			continue
		}
		s.resync(c, p)
		after := c.publishSeq()
		changed, version, err := s.sync(c, p)
		s.mu.Unlock()
		if err != nil {
			fd.Err = err
			out = append(out, fd)
			continue
		}
		if !changed {
			if items, ok := c.cached(p); ok {
				fd.Items, fd.Ready = items, true
				out = append(out, fd)
				continue
			}
			after = 0
		}
		out = append(out, fd)
		waits = append(waits, pendingFile{s: s, c: c, after: after, version: version, i: len(out) - 1})
	}
	for _, w := range waits {
		items, ok, err := w.c.waitDiagnostics(ctx, out[w.i].Path, w.after, w.version, deadline)
		out[w.i].Items, out[w.i].Ready, out[w.i].Err = items, ok, err
	}
	return out
}

// Definition returns where the symbol at pos in path is defined.
func (m *Manager) Definition(ctx context.Context, path string, pos Position) ([]Location, error) {
	return m.locations(ctx, "textDocument/definition", path, pos, nil)
}

// References returns the references to the symbol at pos in path,
// including its declaration.
func (m *Manager) References(ctx context.Context, path string, pos Position) ([]Location, error) {
	return m.locations(ctx, "textDocument/references", path, pos, map[string]any{"includeDeclaration": true})
}

func (m *Manager) locations(ctx context.Context, method, path string, pos Position, refCtx map[string]any) ([]Location, error) {
	s := m.byExt[strings.ToLower(filepath.Ext(path))]
	if s == nil {
		return nil, ErrNoServer
	}
	s.mu.Lock()
	c, err := m.ensure(ctx, s)
	if err == nil {
		s.resync(c, path)
		_, _, err = s.sync(c, path)
	}
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	params := map[string]any{"textDocument": map[string]any{"uri": PathToURI(path)}, "position": pos}
	if refCtx != nil {
		params["context"] = refCtx
	}
	rctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	raw, err := c.call(rctx, method, params)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return nil, fmt.Errorf("lsp server %s did not answer within %s", s.def.Name, requestTimeout)
		}
		if errors.Is(err, errExited) {
			return nil, fmt.Errorf("lsp server %s exited", s.def.Name)
		}
		return nil, fmt.Errorf("lsp server %s: %w", s.def.Name, err)
	}
	return parseLocations(raw)
}

// Close shuts every running server down (shutdown and exit, then a kill
// of its process group after a short grace) and records a stopped event
// for each. Later calls do nothing.
func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, s := range m.servers {
		s.mu.Lock()
		c := s.client
		s.client, s.docs = nil, nil
		s.mu.Unlock()
		if c == nil {
			s.closeLog()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			alive := !c.isDead()
			c.shutdown(shutdownGrace)
			if alive {
				m.record(s.def.Name, StateStopped, "")
			}
			s.closeLog()
		}()
	}
	wg.Wait()
}

func (s *server) closeLog() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.log != nil {
		s.log.f.Close()
		s.log = nil
	}
}

// limitedFile writes at most left bytes, dropping the rest.
type limitedFile struct {
	mu   sync.Mutex
	f    *os.File
	left int
}

func (l *limitedFile) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.left > 0 {
		b := p
		if len(b) > l.left {
			b = b[:l.left]
		}
		n, _ := l.f.Write(b)
		l.left -= n
	}
	return len(p), nil
}

// pruneLogs keeps the newest keep .log files in dir.
func pruneLogs(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type file struct {
		name string
		mod  time.Time
	}
	var files []file
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		if info, err := e.Info(); err == nil {
			files = append(files, file{e.Name(), info.ModTime()})
		}
	}
	if len(files) <= keep {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	for _, f := range files[keep:] {
		os.Remove(filepath.Join(dir, f.name))
	}
}
