//go:build linux

package tools

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/sandboxnet"
)

// viaProxy sends a plain-HTTP proxy request for url to $HTTP_PROXY with
// bash's /dev/tcp, so the tests need no curl.
func viaProxy(url string) string {
	return `hp=${HTTP_PROXY#http://}; exec 3<>/dev/tcp/${hp%:*}/${hp##*:} && printf 'GET ` + url + ` HTTP/1.0\r\n\r\n' >&3 && cat <&3`
}

func fixture(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "fixture-ok") }))
	t.Cleanup(srv.Close)
	return srv, srv.Listener.Addr().String()
}

// TestSandboxProxyHelper runs the net-proxy helper without new namespaces,
// so it checks the forwarder, the Landlock port rule and exit status on any
// host with Landlock networking. Isolation itself is TestSandboxAllowlist's.
func TestSandboxProxyHelper(t *testing.T) {
	root := t.TempDir()
	sb := sandboxOrSkip(t, root, nil, false)
	srv, addr := fixture(t)

	rules, err := sandboxnet.ParseRules([]string{addr})
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(t.TempDir(), "p.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	p := sandboxnet.NewProxy(rules, ln)
	go p.Serve()
	defer p.Close()
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := free.Addr().(*net.TCPAddr).Port
	free.Close()

	run := func(command string) (string, int) {
		args := append([]string{SandboxHelperArg, "net-proxy=" + strconv.Itoa(port) + ":" + sock}, sb.Writable...)
		cmd := exec.Command(os.Args[0], append(args, "--", "bash", "-c", command)...)
		cmd.Env = append(os.Environ(), "HTTP_PROXY=http://127.0.0.1:"+strconv.Itoa(port))
		out, err := cmd.CombinedOutput()
		if err != nil && cmd.ProcessState == nil {
			t.Fatal(err)
		}
		return string(out), cmd.ProcessState.ExitCode()
	}
	if out, code := run(viaProxy(srv.URL + "/")); code != 0 || !strings.Contains(out, "fixture-ok") {
		t.Errorf("allowed request: exit %d: %s", code, out)
	}
	if out, _ := run(viaProxy("http://evil.example/")); !strings.Contains(out, "403") || !strings.Contains(out, "evil.example") {
		t.Errorf("unlisted host: %s", out)
	}
	if d := p.TakeDenials(); len(d) != 1 || d[0].Host != "evil.example" {
		t.Errorf("denials: %+v", d)
	}
	if out, code := run(`exec 3<>/dev/tcp/` + strings.Replace(addr, ":", "/", 1)); code == 0 {
		t.Errorf("direct connection bypassing the proxy succeeded: %s", out)
	}
	if _, code := run(`exit 7`); code != 7 {
		t.Errorf("exit status %d, want 7", code)
	}
	if out, code := run(`echo x > /var/tmp/tinyagent-should-not-exist-$$`); code == 0 {
		t.Errorf("write outside the sandbox succeeded: %s", out)
	}
}

// TestSandboxAllowlist is the end-to-end check of allowlist mode, including
// the network namespace. It skips where the host refuses unprivileged user
// namespaces; a skip is not a pass of the no-bypass check.
func TestSandboxAllowlist(t *testing.T) {
	root := t.TempDir()
	sandboxOrSkip(t, root, nil, false) // Landlock networking
	srv, addr := fixture(t)
	sb, err := NewSandbox(root, nil, false, []string{addr})
	if err != nil {
		if !strings.Contains(err.Error(), "namespaces") {
			t.Fatalf("unexpected error: %v", err)
		}
		t.Skipf("allowlist mode unavailable on this host (fails closed): %v", err)
	}
	defer sb.Cleanup()
	if !strings.Contains(sb.Describe(), "network: allowlist (1 domain: "+addr+")") {
		t.Errorf("Describe: %s", sb.Describe())
	}

	out := runSandboxed(t, sb, root, viaProxy(srv.URL+"/"))
	if !strings.Contains(out, "fixture-ok") {
		t.Errorf("allowed request failed: %s", out)
	}
	out = runSandboxed(t, sb, root, viaProxy("http://evil.example/"))
	if !strings.Contains(out, "403") || !strings.Contains(out, "evil.example") {
		t.Errorf("unlisted host: %s", out)
	}
	if d := sb.TakeNetworkDenials(); len(d) != 1 || d[0].Host != "evil.example" {
		t.Errorf("denials: %+v", d)
	}
	for _, target := range []string{strings.Replace(addr, ":", "/", 1), "10.255.255.1/" + strconv.Itoa(proxyPort), "1.1.1.1/443"} {
		if out := runSandboxed(t, sb, root, `timeout 5 bash -c 'exec 3<>/dev/tcp/`+target+`'`); strings.Contains(out, "<returncode>0</returncode>") {
			t.Errorf("direct connection to %s succeeded: %s", target, out)
		}
	}

	e := &Executor{Root: root, Env: Local{Sandbox: sb}}
	defer e.Close()
	if _, err := e.StartProcess(viaProxy(srv.URL+"/") + ` > bg.out; ` + viaProxy("http://bg.evil.example/") + ` > bg-denied.out`); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, _ := os.ReadFile(filepath.Join(root, "bg-denied.out")); len(b) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "bg.out")); !strings.Contains(string(b), "fixture-ok") {
		t.Errorf("background allowed request: %s", b)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "bg-denied.out")); !strings.Contains(string(b), "403") {
		t.Errorf("background unlisted host: %s", b)
	}
}

func TestSandboxAllowlistCleanup(t *testing.T) {
	root := t.TempDir()
	sandboxOrSkip(t, root, nil, false)
	sb, err := NewSandbox(root, nil, false, []string{"example.com"})
	if err != nil {
		t.Skipf("allowlist mode unavailable: %v", err)
	}
	sock := sb.proxy.Addr().String()
	sb.Cleanup()
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Errorf("proxy socket %s still exists: %v", sock, err)
	}
	if _, err := net.Dial("unix", sock); err == nil {
		t.Error("proxy still accepting connections")
	}
}
