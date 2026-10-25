package tools

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == SandboxHelperArg {
		os.Exit(RunSandboxHelper(os.Args[2:]))
	}
	os.Exit(m.Run())
}

// sandboxOrSkip returns a sandbox, skipping when the host can't enforce one.
func sandboxOrSkip(t *testing.T, root string, extra []string, network bool) *Sandbox {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no sandbox backend on " + runtime.GOOS)
	}
	sb, err := NewSandbox(root, extra, network, nil)
	if err != nil {
		t.Skipf("sandbox unavailable: %v", err)
	}
	t.Cleanup(sb.Cleanup)
	return sb
}

func runSandboxed(t *testing.T, sb *Sandbox, root, cmd string) string {
	t.Helper()
	out, err := bashStream(context.Background(), Local{Sandbox: sb}, root, cmd, 10*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSandboxWrites(t *testing.T) {
	root := t.TempDir()
	extra := t.TempDir()
	// The probe target must be outside every writable root.
	home, err := os.MkdirTemp("/var/tmp", "tinyagent-outside-")
	if err != nil {
		t.Skip(err)
	}
	defer os.RemoveAll(home)
	sb := sandboxOrSkip(t, root, []string{extra}, false)

	out := runSandboxed(t, sb, root, `echo ok > out.txt && mkdir -p sub && mv out.txt sub/ && echo t > "$TMPDIR/x" && echo e > `+extra+`/f && cat /etc/hosts >/dev/null && ls >/dev/null`)
	if !strings.Contains(out, "<returncode>0</returncode>") {
		t.Fatalf("allowed operations failed: %s", out)
	}
	if _, err := os.Stat(filepath.Join(root, "sub", "out.txt")); err != nil {
		t.Fatal(err)
	}

	probe := filepath.Join(home, "probe")
	for _, cmd := range []string{
		`touch ` + probe,
		`sh -c 'echo x > ` + probe + `'`,
	} {
		out := runSandboxed(t, sb, root, cmd)
		if strings.Contains(out, "<returncode>0</returncode>") {
			t.Errorf("%q succeeded: %s", cmd, out)
		}
		if _, err := os.Stat(probe); err == nil {
			t.Fatalf("%q created %s", cmd, probe)
		}
	}
}

func TestSandboxStartProcess(t *testing.T) {
	root := t.TempDir()
	outside, err := os.MkdirTemp("/var/tmp", "tinyagent-outside-")
	if err != nil {
		t.Skip(err)
	}
	defer os.RemoveAll(outside)
	sb := sandboxOrSkip(t, root, nil, false)
	e := &Executor{Root: root, Env: Local{Sandbox: sb}}
	defer e.Close()
	if _, err := e.StartProcess(`sh -c 'echo x > ` + filepath.Join(outside, "p") + `'; sleep 1`); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(outside, "p")); err == nil {
		t.Fatal("background process wrote outside the sandbox")
	}
}

func TestSandboxNetwork(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	root := t.TempDir()
	cmd := `exec 3<>/dev/tcp/` + strings.Replace(l.Addr().String(), ":", "/", 1)

	denied := sandboxOrSkip(t, root, nil, false)
	if out := runSandboxed(t, denied, root, cmd); strings.Contains(out, "<returncode>0</returncode>") {
		t.Errorf("connection succeeded with network denied: %s", out)
	}
	allowed := sandboxOrSkip(t, root, nil, true)
	if out := runSandboxed(t, allowed, root, cmd); !strings.Contains(out, "<returncode>0</returncode>") {
		t.Errorf("connection failed with network allowed: %s", out)
	}
}

func TestSandboxDescribe(t *testing.T) {
	s := &Sandbox{Backend: "x", Writable: []string{"/a", "/b"}}
	if got, want := s.Describe(), "sandbox: x; writable: /a, /b; network: denied"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	s.AllowedDomains = []string{"proxy.golang.org", "*.npmjs.org"}
	if got, want := s.NetworkMode(), "allowlist (2 domains: proxy.golang.org, *.npmjs.org)"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	s.AllowedDomains = s.AllowedDomains[:1]
	if got, want := s.NetworkMode(), "allowlist (1 domain: proxy.golang.org)"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSandboxDefaultEnvironmentUnchanged(t *testing.T) {
	root := t.TempDir()
	sb := sandboxOrSkip(t, root, nil, false)
	cmd := Local{Sandbox: sb}.Command(context.Background(), root, "true")
	for _, kv := range cmd.Env[len(os.Environ()):] {
		if kv != "TMPDIR="+sb.Tmp {
			t.Errorf("unexpected variable %q without an allowlist", kv)
		}
	}
}

func TestNewSandboxRejectsBadAllowlist(t *testing.T) {
	if _, err := NewSandbox(t.TempDir(), nil, false, []string{"*"}); err == nil || !strings.Contains(err.Error(), `"*"`) {
		t.Errorf("bare wildcard: %v", err)
	}
	if _, err := NewSandbox(t.TempDir(), nil, true, []string{"example.com"}); err == nil || !strings.Contains(err.Error(), "sandbox_network") {
		t.Errorf("with network: %v", err)
	}
}
