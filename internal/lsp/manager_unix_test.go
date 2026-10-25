//go:build !windows

package lsp_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/lsp"
	"github.com/vicgupta/tinyagent/internal/lsp/lsptest"
)

// TestCloseLeavesNoProcess covers servers that ignore shutdown and leave a
// child running: Close still stops the whole process group.
func TestCloseLeavesNoProcess(t *testing.T) {
	for _, opts := range [][]string{{"child"}, {"child", "ignore-shutdown"}} {
		root := t.TempDir()
		pidfile := filepath.Join(root, "pids")
		m := lsp.New(lsp.Options{Root: root, Servers: []config.LSPServer{lsptest.Server(t, "go", []string{".go"}, append(opts, "pidfile="+pidfile)...)}})
		a := filepath.Join(root, "a.go")
		os.WriteFile(a, []byte("package a\n"), 0o644)
		m.Diagnostics(context.Background(), []string{a}, time.Second)
		pids := lsptest.ReadPids(t, pidfile, 2)
		start := time.Now()
		m.Close()
		if d := time.Since(start); d > 6*time.Second {
			t.Errorf("%v: Close took %s", opts, d)
		}
		lsptest.AssertGone(t, pids)
	}
}
