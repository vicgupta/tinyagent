package lsp_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/lsp"
	"github.com/vicgupta/tinyagent/internal/lsp/lsptest"
)

func TestMain(m *testing.M) {
	lsptest.Run()
	os.Exit(m.Run())
}

func newManager(t *testing.T, root string, servers ...config.LSPServer) *lsp.Manager {
	t.Helper()
	m := lsp.New(lsp.Options{Root: root, Servers: servers, LogDir: filepath.Join(root, ".tinyagent", "lsp"), LogPrefix: "run-"})
	t.Cleanup(m.Close)
	return m
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func states(ev []lsp.Event) string {
	var s []string
	for _, e := range ev {
		s = append(s, e.Server+":"+e.State)
	}
	return strings.Join(s, ",")
}

func TestDiagnosticsFollowDisk(t *testing.T) {
	root := t.TempDir()
	log := filepath.Join(root, "server.log")
	m := newManager(t, root, lsptest.Server(t, "go", []string{".go"}, "log="+log))
	a := filepath.Join(root, "a.go")
	write(t, a, "package a\nvar x = y // ERR: undefined: y\n")
	if _, ok := m.ServerFor(filepath.Join(root, "b.GO")); !ok {
		t.Error("extension match is case-sensitive")
	}
	if _, ok := m.ServerFor(filepath.Join(root, "b.md")); ok {
		t.Error(".md matched")
	}
	got := m.Diagnostics(context.Background(), []string{a, filepath.Join(root, "notes.md")}, 3*time.Second)
	if len(got) != 1 || !got[0].Ready || got[0].Err != nil || len(got[0].Items) != 1 || got[0].Items[0].Message != "undefined: y" || got[0].Items[0].Range.Start.Line != 1 {
		t.Fatalf("diagnostics = %+v", got)
	}
	// Unchanged content answers from the cache.
	got = m.Diagnostics(context.Background(), []string{a}, 3*time.Second)
	if !got[0].Ready || len(got[0].Items) != 1 {
		t.Fatalf("cached = %+v", got)
	}
	write(t, a, "package a\n")
	got = m.Diagnostics(context.Background(), []string{a}, 3*time.Second)
	if !got[0].Ready || len(got[0].Items) != 0 {
		t.Fatalf("after fix = %+v", got)
	}
	if ev := states(m.TakeEvents()); ev != "go:started" {
		t.Errorf("events = %s", ev)
	}
	m.Close()
	if ev := states(m.TakeEvents()); ev != "go:stopped" {
		t.Errorf("close events = %s", ev)
	}
	b, _ := os.ReadFile(log)
	for _, want := range []string{"start", "initialize", "initialized", "didOpen", "didChange \"package a\\n\"", "shutdown", "exit"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("server log lacks %q:\n%s", want, b)
		}
	}
	logs, _ := filepath.Glob(filepath.Join(root, ".tinyagent", "lsp", "run-go.log"))
	if len(logs) != 1 {
		t.Fatalf("stderr log: %v", logs)
	}
	if b, _ := os.ReadFile(logs[0]); !strings.Contains(string(b), "fake lsp started") {
		t.Errorf("stderr log = %q", b)
	}
}

func TestDiagnosticsTimeout(t *testing.T) {
	root := t.TempDir()
	m := newManager(t, root, lsptest.Server(t, "go", []string{".go"}, "nopublish"))
	a := filepath.Join(root, "a.go")
	write(t, a, "package a\n")
	m.Diagnostics(context.Background(), []string{a}, 100*time.Millisecond) // start the server
	write(t, a, "package b\n")
	start := time.Now()
	got := m.Diagnostics(context.Background(), []string{a}, 300*time.Millisecond)
	if d := time.Since(start); d > 800*time.Millisecond || d < 250*time.Millisecond {
		t.Errorf("waited %s", d)
	}
	if got[0].Ready || got[0].Err != nil {
		t.Fatalf("got %+v", got)
	}
}

func TestMissingBinaryUnavailable(t *testing.T) {
	root := t.TempDir()
	m := newManager(t, root, config.LSPServer{Name: "go", Command: filepath.Join(root, "no-such-gopls"), Extensions: []string{".go"}})
	a := filepath.Join(root, "a.go")
	write(t, a, "package a\n")
	for range 2 {
		got := m.Diagnostics(context.Background(), []string{a}, time.Second)
		var ue *lsp.UnavailableError
		if len(got) != 1 || got[0].Err == nil || !errorsAs(got[0].Err, &ue) {
			t.Fatalf("got %+v", got)
		}
	}
	if _, err := m.References(context.Background(), a, lsp.Position{}); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("references err = %v", err)
	}
	if ev := states(m.TakeEvents()); ev != "go:unavailable" {
		t.Errorf("events = %s", ev)
	}
}

func TestStartupTimeout(t *testing.T) {
	root := t.TempDir()
	s := lsptest.Server(t, "go", []string{".go"}, "hang-init")
	s.StartupTimeoutSeconds = 1
	m := newManager(t, root, s)
	a := filepath.Join(root, "a.go")
	write(t, a, "package a\n")
	start := time.Now()
	got := m.Diagnostics(context.Background(), []string{a}, time.Second)
	if got[0].Err == nil || !strings.Contains(got[0].Err.Error(), "initialize did not finish within 1s") {
		t.Fatalf("got %+v", got)
	}
	if time.Since(start) > 4*time.Second {
		t.Errorf("took %s", time.Since(start))
	}
}

func TestCrashRestartsOnce(t *testing.T) {
	root := t.TempDir()
	log := filepath.Join(root, "server.log")
	m := newManager(t, root, lsptest.Server(t, "go", []string{".go"}, "crash", "log="+log))
	a := filepath.Join(root, "a.go")
	write(t, a, "package a\n")
	// A call can reach a server that is dying but not yet reaped (common
	// on macOS), so call until the manager gives up on the server rather
	// than a fixed number of times.
	var unavail *lsp.UnavailableError
	for i := 0; unavail == nil; i++ {
		if i == 20 {
			t.Fatal("server never became unavailable")
		}
		done := make(chan error, 1)
		go func() {
			_, err := m.References(context.Background(), a, lsp.Position{})
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Fatalf("call %d: no error", i)
			}
			errors.As(err, &unavail)
		case <-time.After(5 * time.Second):
			t.Fatalf("call %d hung", i)
		}
	}
	b, _ := os.ReadFile(log)
	if n := strings.Count(string(b), "start\n"); n != 2 {
		t.Errorf("server started %d times, want 2:\n%s", n, b)
	}
	ev := m.TakeEvents()
	var crashed, unavailable int
	for _, e := range ev {
		switch e.State {
		case lsp.StateCrashed:
			crashed++
		case lsp.StateUnavailable:
			unavailable++
		}
	}
	if crashed != 2 || unavailable != 1 {
		t.Errorf("events = %s", states(ev))
	}
}

func TestMinimalEnvironment(t *testing.T) {
	root := t.TempDir()
	envFile := filepath.Join(root, "env")
	t.Setenv("TINYAGENT_LSP_SECRET", "s3cret")
	t.Setenv("TINYAGENT_LSP_FORWARD", "yes")
	s := lsptest.Server(t, "go", []string{".go"}, "envfile="+envFile)
	s.Env = []string{"TINYAGENT_LSP_FORWARD"}
	m := newManager(t, root, s)
	a := filepath.Join(root, "a.go")
	write(t, a, "package a\n")
	m.Diagnostics(context.Background(), []string{a}, time.Second)
	b, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, kv := range strings.Split(string(b), "\n") {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "PATH", "TINYAGENT_LSP_FORWARD", "SystemRoot", "WINDIR", "USERPROFILE", "TEMP", "TMP":
		default:
			t.Errorf("unexpected variable %q", kv)
		}
	}
	if !strings.Contains(string(b), "TINYAGENT_LSP_FORWARD=yes") {
		t.Errorf("forwarded variable missing: %s", b)
	}
}

func TestReferencesAndDefinition(t *testing.T) {
	root := t.TempDir()
	refs := filepath.Join(root, "refs.json")
	a := filepath.Join(root, "a.go")
	lsptest.WriteLocs(t, refs, []lsptest.Loc{{Path: a, Line: 1, Character: 4}, {Path: "/elsewhere/x.go", Line: 0, Character: 0}})
	m := newManager(t, root, lsptest.Server(t, "go", []string{".go"}, "refs="+refs, "def="+refs))
	write(t, a, "package a\nvar x = 1\n")
	locs, err := m.References(context.Background(), a, lsp.Position{Line: 1, Character: 4})
	if err != nil || len(locs) != 2 || locs[0].Path != a || locs[0].Range.Start.Line != 1 || locs[1].Path != filepath.Clean("/elsewhere/x.go") {
		t.Fatalf("refs = %+v, %v", locs, err)
	}
	if locs, err := m.Definition(context.Background(), a, lsp.Position{}); err != nil || len(locs) != 2 {
		t.Fatalf("def = %+v, %v", locs, err)
	}
	if _, err := m.Definition(context.Background(), filepath.Join(root, "x.rs"), lsp.Position{}); err != lsp.ErrNoServer {
		t.Fatalf("no server err = %v", err)
	}
}

func errorsAs(err error, target any) bool { return errors.As(err, target) }
