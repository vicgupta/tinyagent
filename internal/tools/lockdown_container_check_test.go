package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestContainerRunNetwork(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{nil, ""},
		{[]string{"--memory", "2g"}, ""},
		{[]string{"--network", "none"}, "none"},
		{[]string{"--network=host"}, "host"},
		{[]string{"--net", "mynet"}, "mynet"},
		{[]string{"--net=container:abc"}, "container:abc"},
		{[]string{"--network", "bridge", "--network=none"}, "none"},
		{[]string{"--network"}, ""},
		{[]string{"--networks", "x"}, ""},
	}
	for _, c := range cases {
		if got := ContainerRunNetwork(c.args); got != c.want {
			t.Errorf("ContainerRunNetwork(%q) = %q, want %q", c.args, got, c.want)
		}
	}
}

func TestCheckContainerLockdownNetworkRunArgs(t *testing.T) {
	ctx := context.Background()
	r := CheckContainerLockdownNetwork(ctx, "docker", []string{"--network", "none"})
	if !r.Denied || r.Err != nil || !r.FromRunArgs || r.Mode != "none" {
		t.Errorf("none: %+v", r)
	}
	for _, mode := range []string{"host", "container:x", "ns:/proc/1/ns/net", "slirp4netns", "pasta:-T,8080"} {
		r := CheckContainerLockdownNetwork(ctx, "docker", []string{"--network=" + mode})
		if r.Err == nil || r.Denied || !strings.Contains(r.Err.Error(), "can't be disconnected") {
			t.Errorf("%s: %+v", mode, r)
		}
	}
	r = CheckContainerLockdownNetwork(ctx, "podman", []string{"--network", "mynet"})
	if r.Err != nil || r.Denied || r.Unknown != "" || r.Mode != "mynet" {
		t.Errorf("mynet: %+v", r)
	}
	// docker's default is a bridge network; no runtime is asked.
	t.Setenv("PATH", t.TempDir())
	r = CheckContainerLockdownNetwork(ctx, "", nil)
	if r.Err != nil || r.Denied || r.Unknown != "" || r.FromRunArgs || r.Mode != "bridge" {
		t.Errorf("docker default: %+v", r)
	}
}

// fakePodman puts a podman on PATH whose `info` prints out (or fails).
func fakePodman(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake runtime is a shell script")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "podman"), []byte("#!"+sh+"\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestCheckContainerLockdownNetworkPodmanDefault(t *testing.T) {
	ctx := context.Background()
	fakePodman(t, `[ "$1" = info ] && echo true`)
	r := CheckContainerLockdownNetwork(ctx, "podman", nil)
	if r.Err == nil || r.Unknown != "" || r.Mode != "slirp4netns/pasta" {
		t.Errorf("rootless: %+v", r)
	}
	// --network private is podman's default too.
	r = CheckContainerLockdownNetwork(ctx, "podman", []string{"--network=private"})
	if r.Err == nil || r.FromRunArgs {
		t.Errorf("rootless private: %+v", r)
	}

	fakePodman(t, `[ "$1" = info ] && echo false`)
	r = CheckContainerLockdownNetwork(ctx, "podman", nil)
	if r.Err != nil || r.Unknown != "" || r.Mode != "bridge" {
		t.Errorf("rootful: %+v", r)
	}

	fakePodman(t, `echo "cannot connect" >&2; exit 1`)
	r = CheckContainerLockdownNetwork(ctx, "podman", nil)
	if r.Err != nil || !strings.Contains(r.Unknown, "cannot connect") {
		t.Errorf("info fails: %+v", r)
	}

	fakePodman(t, `echo maybe`)
	r = CheckContainerLockdownNetwork(ctx, "podman", nil)
	if !strings.Contains(r.Unknown, `"maybe"`) {
		t.Errorf("odd output: %+v", r)
	}

	t.Setenv("PATH", t.TempDir())
	r = CheckContainerLockdownNetwork(ctx, "podman", nil)
	if !strings.Contains(r.Unknown, "not found in PATH") {
		t.Errorf("missing podman: %+v", r)
	}
}
