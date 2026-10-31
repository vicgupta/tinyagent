//go:build linux

package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeProbes replaces bwrap lookup, the bwrap probe run and the Landlock
// probe for one test.
func fakeProbes(t *testing.T, bwrapErr, runErr, landlockErr error) {
	t.Helper()
	oldLook, oldRun, oldLL, oldAA := bwrapLook, bwrapRun, landlockProbe, apparmorUsernsPath
	t.Cleanup(func() { bwrapLook, bwrapRun, landlockProbe, apparmorUsernsPath = oldLook, oldRun, oldLL, oldAA })
	apparmorUsernsPath = filepath.Join(t.TempDir(), "missing")
	bwrapLook = func() (string, error) { return "/usr/bin/bwrap", bwrapErr }
	bwrapRun = func(string, []string) ([]byte, error) {
		if runErr != nil {
			return []byte("bwrap: setting up uid map: Permission denied"), runErr
		}
		return nil, nil
	}
	landlockProbe = func(network, proxied bool) (string, error) {
		if landlockErr != nil {
			return "", landlockErr
		}
		return "landlock (ABI 6)", nil
	}
}

func TestBwrapArgs(t *testing.T) {
	base := []string{"--die-with-parent", "--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts"}
	mounts := []string{"--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc"}
	cat := func(parts ...[]string) []string {
		var out []string
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	s := &Sandbox{Writable: []string{"/work", "/tmp/tinyagent-sandbox-1"}}
	binds := []string{"--bind", "/work", "/work", "--bind", "/tmp/tinyagent-sandbox-1", "/tmp/tinyagent-sandbox-1"}
	helper := func(mode string) []string {
		return []string{"/bin/tinyagent", SandboxHelperArg, mode, "/work", "/tmp/tinyagent-sandbox-1", "--"}
	}
	cmd := []string{"bash", "-c", "make"}
	tests := []struct {
		name     string
		s        *Sandbox
		network  bool
		proxyDir string
		helper   []string
		want     []string
	}{
		{"network off", s, false, "", helper("net-deny"), cat(base, []string{"--unshare-net"}, mounts, binds,
			[]string{"--ro-bind", "/bin/tinyagent", "/bin/tinyagent", "--"}, helper("net-deny"), cmd)},
		{"network on", s, true, "", helper("net-allow"), cat(base, mounts, binds,
			[]string{"--ro-bind", "/bin/tinyagent", "/bin/tinyagent", "--"}, helper("net-allow"), cmd)},
		{"allowed domains", s, false, "/tmp/tinyagent-proxy-1", helper("net-proxy=3128:/tmp/tinyagent-proxy-1/proxy.sock"), cat(base, []string{"--unshare-net"}, mounts, binds,
			[]string{"--bind", "/tmp/tinyagent-proxy-1", "/tmp/tinyagent-proxy-1", "--ro-bind", "/bin/tinyagent", "/bin/tinyagent", "--"},
			helper("net-proxy=3128:/tmp/tinyagent-proxy-1/proxy.sock"), cmd)},
		{"extra writable, bwrap alone", &Sandbox{Writable: []string{"/work", "/data", "/cache"}}, false, "", nil, cat(base, []string{"--unshare-net"}, mounts,
			[]string{"--bind", "/work", "/work", "--bind", "/data", "/data", "--bind", "/cache", "/cache", "--"}, cmd)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.s.bwrapArgs(tt.network, tt.proxyDir, tt.helper, cmd)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("bwrapArgs:\n got %q\nwant %q", got, tt.want)
			}
		})
	}
}

func TestWrapBwrapUsesHelper(t *testing.T) {
	s := &Sandbox{bwrap: "/usr/bin/bwrap", landlock: true, Writable: []string{"/work"}}
	c := s.wrap(context.Background(), []string{"bash", "-c", "true"})
	if c.Path != "/usr/bin/bwrap" {
		t.Fatalf("path %s", c.Path)
	}
	joined := strings.Join(c.Args, " ")
	for _, want := range []string{"--unshare-net", "--bind /work /work", SandboxHelperArg + " net-deny /work -- bash -c true"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: %s", want, joined)
		}
	}
}

func TestSelectSandbox(t *testing.T) {
	fail := errors.New("boom")
	tests := []struct {
		name                    string
		backend                 string
		proxied                 bool
		lookErr, runErr, llErr  error
		wantDesc, wantErr, warn string
		wantBwrap, wantLandlock bool
	}{
		{name: "auto stacks", backend: "auto", wantDesc: "bwrap + landlock (ABI 6)", wantBwrap: true, wantLandlock: true},
		{name: "empty is auto", backend: "", wantDesc: "bwrap + landlock (ABI 6)", wantBwrap: true, wantLandlock: true},
		{name: "auto without bwrap", backend: "auto", lookErr: fail, wantDesc: "landlock (ABI 6)", wantLandlock: true},
		{name: "auto with bwrap blocked", backend: "auto", runErr: fail, wantDesc: "landlock (ABI 6)", wantLandlock: true, warn: "bwrap unavailable"},
		{name: "auto without either", backend: "auto", runErr: fail, llErr: errors.New("sandbox: Landlock is unavailable"), wantErr: "Landlock is unavailable"},
		{name: "auto bwrap alone", backend: "auto", llErr: errors.New("sandbox: Landlock is unavailable"), wantDesc: "bwrap", wantBwrap: true, warn: "using bwrap alone"},
		{name: "allowlist needs landlock", backend: "auto", proxied: true, llErr: errors.New("sandbox: Landlock ABI 3"), wantErr: "Linux 6.7+"},
		{name: "landlock", backend: "landlock", wantDesc: "landlock (ABI 6)", wantLandlock: true},
		{name: "landlock fails", backend: "landlock", llErr: fail, wantErr: "boom"},
		{name: "bwrap", backend: "bwrap", wantDesc: "bwrap + landlock (ABI 6)", wantBwrap: true, wantLandlock: true},
		{name: "bwrap missing", backend: "bwrap", lookErr: fail, wantErr: "not found on PATH"},
		{name: "bwrap blocked", backend: "bwrap", runErr: fail, wantErr: "can't create its sandbox"},
		{name: "invalid", backend: "docker", wantErr: "must be auto, landlock or bwrap"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeProbes(t, tt.lookErr, tt.runErr, tt.llErr)
			e, err := selectSandbox(tt.backend, false, tt.proxied)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if e.desc != tt.wantDesc || (e.bwrap != "") != tt.wantBwrap || e.landlock != tt.wantLandlock {
				t.Errorf("got %+v", e)
			}
			if w := strings.Join(e.warnings, "\n"); (tt.warn == "") != (w == "") || !strings.Contains(w, tt.warn) {
				t.Errorf("warnings %q, want %q", w, tt.warn)
			}
		})
	}
}

func TestBwrapAppArmorDiagnosis(t *testing.T) {
	proc := filepath.Join(t.TempDir(), "apparmor_restrict_unprivileged_userns")
	probeErr := errors.New("exit status 1: bwrap: setting up uid map: Permission denied")
	if err := os.WriteFile(proc, []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	msg := bwrapProbeError(probeErr, proc).Error()
	for _, want := range []string{"AppArmor restricts unprivileged user namespaces", "Permission denied",
		"sudo apt install apparmor-profiles apparmor-utils",
		"sudo install -m 0644 /usr/share/apparmor/extra-profiles/bwrap-userns-restrict /etc/apparmor.d/bwrap-userns-restrict",
		"sudo apparmor_parser -r /etc/apparmor.d/bwrap-userns-restrict"} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in:\n%s", want, msg)
		}
	}
	for _, content := range []string{"0\n", ""} {
		os.WriteFile(proc, []byte(content), 0o644)
		if msg := bwrapProbeError(probeErr, proc).Error(); strings.Contains(msg, "AppArmor") || !strings.Contains(msg, "user namespaces") {
			t.Errorf("sysctl %q: %s", content, msg)
		}
	}
	if msg := bwrapProbeError(probeErr, filepath.Join(t.TempDir(), "none")).Error(); strings.Contains(msg, "AppArmor") {
		t.Errorf("missing sysctl: %s", msg)
	}

	// auto warns and falls back; bwrap fails with the fix.
	fakeProbes(t, nil, errors.New("exit status 1"), nil)
	os.WriteFile(proc, []byte("1\n"), 0o644)
	apparmorUsernsPath = proc
	e, err := selectSandbox("auto", false, false)
	if err != nil || e.bwrap != "" || len(e.warnings) != 1 || !strings.Contains(e.warnings[0], "bwrap-userns-restrict") {
		t.Errorf("auto: %+v, %v", e, err)
	}
	if _, err := selectSandbox("bwrap", false, false); err == nil || !strings.Contains(err.Error(), "apparmor_parser") {
		t.Errorf("bwrap: %v", err)
	}
}

// bwrapOrSkip returns a sandbox on the real bwrap backend, skipping when
// bwrap is missing or the host refuses it (as GitHub's Ubuntu 24.04
// runners do through AppArmor).
func bwrapOrSkip(t *testing.T, root string, extra []string, network bool, allowed []string) *Sandbox {
	t.Helper()
	if _, err := findBwrap(); err != nil {
		t.Skipf("bwrap unavailable: %v", err)
	}
	sb, err := NewSandboxBackend("bwrap", root, extra, network, allowed)
	if err != nil {
		t.Skipf("bwrap sandbox unavailable: %v", err)
	}
	t.Cleanup(sb.Cleanup)
	if !strings.HasPrefix(sb.Backend, "bwrap") || !strings.Contains(sb.Describe(), "sandbox: bwrap") {
		t.Fatalf("Describe: %s", sb.Describe())
	}
	return sb
}

func TestBwrapSandboxWrites(t *testing.T) {
	root, extra := t.TempDir(), t.TempDir()
	outside, err := os.MkdirTemp("/var/tmp", "tinyagent-outside-")
	if err != nil {
		t.Skip(err)
	}
	defer os.RemoveAll(outside)
	sb := bwrapOrSkip(t, root, []string{extra}, false, nil)

	out := runSandboxed(t, sb, root, `pwd && echo ok > out.txt && echo t > "$TMPDIR/x" && echo e > `+extra+`/f && cat /etc/hosts >/dev/null && echo n > /dev/null`)
	if !strings.Contains(out, "<returncode>0</returncode>") || !strings.Contains(out, root) {
		t.Fatalf("allowed operations failed: %s", out)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "out.txt")); string(b) != "ok\n" {
		t.Errorf("out.txt %q", b)
	}
	if out := runSandboxed(t, sb, root, `echo x > `+outside+`/f`); strings.Contains(out, "<returncode>0</returncode>") {
		t.Errorf("write outside succeeded: %s", out)
	}
	if out := runSandboxed(t, sb, root, `exit 7`); !strings.Contains(out, "<returncode>7</returncode>") {
		t.Errorf("exit status: %s", out)
	}
}

func TestBwrapSandboxNetwork(t *testing.T) {
	root := t.TempDir()
	_, addr := fixture(t)
	dial := `timeout 5 bash -c 'exec 3<>/dev/tcp/` + strings.Replace(addr, ":", "/", 1) + `'`
	off := bwrapOrSkip(t, root, nil, false, nil)
	if out := runSandboxed(t, off, root, dial); strings.Contains(out, "<returncode>0</returncode>") {
		t.Errorf("network off: connection succeeded: %s", out)
	}
	on := bwrapOrSkip(t, root, nil, true, nil)
	if out := runSandboxed(t, on, root, dial); !strings.Contains(out, "<returncode>0</returncode>") {
		t.Errorf("network on: connection failed: %s", out)
	}
}

func TestBwrapSandboxAllowlist(t *testing.T) {
	root := t.TempDir()
	srv, addr := fixture(t)
	sb := bwrapOrSkip(t, root, nil, false, []string{addr})
	if out := runSandboxed(t, sb, root, viaProxy(srv.URL+"/")); !strings.Contains(out, "fixture-ok") {
		t.Errorf("allowed request failed: %s", out)
	}
	if out := runSandboxed(t, sb, root, viaProxy("http://evil.example/")); !strings.Contains(out, "403") {
		t.Errorf("unlisted host: %s", out)
	}
	if out := runSandboxed(t, sb, root, `timeout 5 bash -c 'exec 3<>/dev/tcp/`+strings.Replace(addr, ":", "/", 1)+`'`); strings.Contains(out, "<returncode>0</returncode>") {
		t.Errorf("direct connection succeeded: %s", out)
	}
}
