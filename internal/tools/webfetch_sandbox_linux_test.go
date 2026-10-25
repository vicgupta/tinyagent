package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestWebFetchUnaffectedBySandbox checks that web_fetch works under
// --sandbox with networking off, while commands still cannot reach the same
// host (AC3). Skips where the sandbox is unavailable.
func TestWebFetchUnaffectedBySandbox(t *testing.T) {
	root := t.TempDir()
	sb := sandboxOrSkip(t, root, nil, false)
	srv, addr := fixture(t)
	f, err := NewWebFetcher([]string{addr}, 0, 5*time.Second, "tinyagent/test")
	if err != nil {
		t.Fatal(err)
	}
	e := &Executor{Root: root, Env: Local{Sandbox: sb}, WebFetch: f, MaxOutputChars: 10000}
	defer e.Close()
	out, err := e.run(context.Background(), WebFetchToolName, fmt.Sprintf(`{"url":%q}`, srv.URL+"/"), nil)
	if err != nil || !strings.Contains(out, "fixture-ok") {
		t.Fatalf("web_fetch under the sandbox: %q, %v", out, err)
	}
	cmd := `timeout 5 bash -c 'exec 3<>/dev/tcp/` + strings.Replace(addr, ":", "/", 1) + `'`
	if out := runSandboxed(t, sb, root, cmd); strings.Contains(out, "<returncode>0</returncode>") {
		t.Errorf("sandboxed command reached the web_fetch host: %s", out)
	}
}
