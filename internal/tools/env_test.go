package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeRuntime stands in for docker: exec runs the command on the host in a
// new session, so killing the host-side client's process group does not reach
// it and only the marker-based kill can.
const fakeRuntime = `#!/bin/bash
case "$1" in
run) echo fakeid ;;
rm) ;;
exec)
  shift; envs=()
  while [ $# -gt 0 ]; do
    case "$1" in -w) cd "$2"; shift 2 ;; -e) envs+=("$2"); shift 2 ;; -u) shift 2 ;; *) break ;; esac
  done
  shift
  exec setsid -w env "${envs[@]}" "$@" ;;
esac
`

func fakeContainer(t *testing.T) (*Container, string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("fake runtime needs Linux /proc")
	}
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid not available")
	}
	bin := filepath.Join(t.TempDir(), "fakedocker")
	if err := os.WriteFile(bin, []byte(fakeRuntime), 0o755); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	c, err := StartContainer(context.Background(), bin, "img", nil, root)
	if err != nil {
		t.Fatal(err)
	}
	return c, root
}

// markedProcesses returns host processes carrying containerMarker.
func markedProcesses() []string {
	dirs, _ := filepath.Glob("/proc/[0-9]*")
	var pids []string
	for _, d := range dirs {
		env, _ := os.ReadFile(filepath.Join(d, "environ"))
		if strings.Contains(string(env), containerMarker+"=") {
			pids = append(pids, filepath.Base(d))
		}
	}
	return pids
}

func waitUnmarked(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for len(markedProcesses()) > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("processes left running: %v", markedProcesses())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestContainerCommand(t *testing.T) {
	c, root := fakeContainer(t)
	if c.ID != "fakeid" || c.Describe() != "environment: container ("+c.Runtime+", image=img); mount: "+root {
		t.Fatalf("container %+v, describe %q", c, c.Describe())
	}
	out, err := bashStream(context.Background(), c, root, "pwd; echo err >&2; exit 3", 10*time.Second, nil)
	if err != nil || out != "<returncode>3</returncode>\n<output>\n"+root+"\nerr\n</output>" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestContainerTimeoutKillsInside(t *testing.T) {
	c, root := fakeContainer(t)
	start := time.Now()
	out, err := bashStream(context.Background(), c, root, "sleep 600 & sleep 600", time.Second, nil)
	if err != nil || !strings.HasPrefix(out, "command timed out after 1s") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %s", d)
	}
	waitUnmarked(t)
}

func TestContainerStopProcess(t *testing.T) {
	c, root := fakeContainer(t)
	e := &Executor{Root: root, Env: c, MaxProcesses: 1}
	out, err := e.StartProcess("echo up; sleep 600")
	if err != nil || !strings.Contains(out, "<status>running</status>") || !strings.Contains(out, "up") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if _, err := e.StartProcess("sleep 1"); err == nil {
		t.Error("process limit not applied")
	}
	if _, err := e.StopProcess("p1", 0); err != nil {
		t.Fatal(err)
	}
	waitUnmarked(t)
}

func TestStartContainerFailsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	root := t.TempDir()
	if _, err := StartContainer(context.Background(), "no-such-runtime-xyz", "img", nil, root); err == nil || !strings.Contains(err.Error(), "not found in PATH") {
		t.Errorf("missing runtime: %v", err)
	}
	if _, err := StartContainer(context.Background(), "docker", "", nil, root); err == nil || !strings.Contains(err.Error(), "image") {
		t.Errorf("missing image: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "down")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'Cannot connect to the Docker daemon' >&2; exit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := StartContainer(context.Background(), bin, "img", nil, root); err == nil || !strings.Contains(err.Error(), "Cannot connect") {
		t.Errorf("daemon down: %v", err)
	}
}

// realRuntime returns a working docker or podman ($TINYAGENT_TEST_RUNTIME
// first), skipping the test when there is none.
func realRuntime(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("container tests run on Linux")
	}
	for _, rt := range []string{os.Getenv("TINYAGENT_TEST_RUNTIME"), "docker", "podman"} {
		if rt == "" {
			continue
		}
		if _, err := exec.LookPath(rt); err == nil && exec.Command(rt, "info").Run() == nil {
			return rt
		}
	}
	t.Skip("no working docker or podman; skipping container tests")
	return ""
}

const testImage = "debian:bookworm-slim"

// inContainer lists the command lines of every process in c.
func inContainer(t *testing.T, c *Container, root string) string {
	t.Helper()
	out, err := bashStream(context.Background(), c, root, `for p in /proc/[0-9]*; do tr '\0' ' ' < $p/cmdline 2>/dev/null; echo; done`, 30*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRealContainer(t *testing.T) {
	rt := realRuntime(t)
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c, err := StartContainer(ctx, rt, testImage, nil, root)
	if err != nil {
		t.Fatal(err)
	}
	removed := false
	defer func() {
		if !removed {
			c.Close()
		}
	}()
	run := func(cmd string) string {
		t.Helper()
		out, err := bashStream(ctx, c, root, cmd, 30*time.Second, nil)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	if out := run("cat /etc/os-release"); !strings.Contains(out, "Debian") {
		t.Errorf("os-release is not the image's: %s", out)
	}
	run("echo hi > out.txt")
	if b, err := os.ReadFile(filepath.Join(root, "out.txt")); err != nil || string(b) != "hi\n" {
		t.Errorf("out.txt on host = %q, %v", b, err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("from host\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := run("cat a.txt"); !strings.Contains(out, "from host") {
		t.Errorf("cat a.txt: %s", out)
	}
	probe := "tinyagent-probe-" + newToken()
	run("echo x > $HOME/" + probe)
	for _, dir := range []string{os.Getenv("HOME"), os.TempDir(), "/tmp"} {
		if _, err := os.Stat(filepath.Join(dir, probe)); err == nil {
			t.Errorf("probe outside the mount appeared on the host in %s", dir)
		}
	}

	out, err := bashStream(ctx, c, root, "sleep 600", 2*time.Second, nil)
	if err != nil || !strings.HasPrefix(out, "command timed out") {
		t.Fatalf("timeout: %q %v", out, err)
	}
	if ps := inContainer(t, c, root); strings.Contains(ps, "sleep 600") {
		t.Errorf("sleep survived the timeout:\n%s", ps)
	}

	e := &Executor{Root: root, Env: c}
	if _, err := e.StartProcess("sleep 601"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.StopProcess("p1", 0); err != nil {
		t.Fatal(err)
	}
	if ps := inContainer(t, c, root); strings.Contains(ps, "sleep 601") {
		t.Errorf("sleep survived stop_process:\n%s", ps)
	}

	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	removed = true
	left, _ := exec.Command(rt, "ps", "-a", "-q", "--filter", "label="+ContainerLabel, "--filter", "id="+c.ID).Output()
	if strings.TrimSpace(string(left)) != "" {
		t.Errorf("container %s still exists after Close", c.ID)
	}
}
