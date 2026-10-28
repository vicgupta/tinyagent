package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ContainerNetworkCheck is what --check predicts lockdown (#171) will do
// with the container's network at taint, without starting a container.
type ContainerNetworkCheck struct {
	// Mode is the network mode the container would start with: the
	// --network (or --net) value in container.run_args, or the runtime's
	// default ("bridge", or podman's rootless default).
	Mode string
	// FromRunArgs is true when container.run_args set Mode.
	FromRunArgs bool
	// Denied: the container would have no network at all (--network none).
	Denied bool
	// Err is set when Mode can't be removed by disconnecting runtime
	// networks, so a run would refuse commands after taint.
	Err error
	// Unknown explains why the mode couldn't be predicted (the runtime
	// couldn't be asked). A run inspects the started container and decides
	// then.
	Unknown string
}

// containerProbeTimeout bounds asking the runtime for its default network.
const containerProbeTimeout = 10 * time.Second

// ContainerRunNetwork returns the network mode container run arguments set
// with --network or --net (the last one wins), or "" when they set none.
func ContainerRunNetwork(runArgs []string) string {
	mode := ""
	for i := 0; i < len(runArgs); i++ {
		a := runArgs[i]
		for _, name := range []string{"--network", "--net"} {
			if a == name && i+1 < len(runArgs) {
				mode = runArgs[i+1]
				i++
				break
			}
			if v, ok := strings.CutPrefix(a, name+"="); ok {
				mode = v
				break
			}
		}
	}
	return strings.TrimSpace(mode)
}

// lockdownModeUnsupported reports whether a container network mode can't be
// removed by disconnecting runtime networks: host networking, another
// container's or a namespace's network, rootless slirp4netns or pasta.
func lockdownModeUnsupported(mode string) bool {
	return mode == "host" || strings.HasPrefix(mode, "container:") || strings.HasPrefix(mode, "ns:") ||
		strings.HasPrefix(mode, "slirp4netns") || strings.HasPrefix(mode, "pasta")
}

func unsupportedModeErr(mode string) error {
	return fmt.Errorf("network mode %q can't be disconnected; use a bridge network or --network none in container.run_args", mode)
}

// CheckContainerLockdownNetwork predicts, for --check, how lockdown will
// handle the network of a container started by runtime (docker when empty)
// with runArgs. It reads --network from runArgs; without one, docker's
// default is a bridge network, and for podman it asks `podman info`
// whether podman runs rootless, whose default (slirp4netns or pasta) can't
// be disconnected. It never starts a container.
func CheckContainerLockdownNetwork(ctx context.Context, runtime string, runArgs []string) ContainerNetworkCheck {
	if runtime == "" {
		runtime = "docker"
	}
	mode := ContainerRunNetwork(runArgs)
	if mode != "" && !(runtime == "podman" && mode == "private") {
		r := ContainerNetworkCheck{Mode: mode, FromRunArgs: true}
		switch {
		case mode == "none":
			r.Denied = true
		case lockdownModeUnsupported(mode):
			r.Err = unsupportedModeErr(mode)
		}
		return r
	}
	if runtime != "podman" {
		return ContainerNetworkCheck{Mode: "bridge"}
	}
	rootless, err := podmanRootless(ctx, runtime)
	if err != nil {
		return ContainerNetworkCheck{Unknown: err.Error()}
	}
	if rootless {
		return ContainerNetworkCheck{Mode: "slirp4netns/pasta", Err: unsupportedModeErr("slirp4netns/pasta")}
	}
	return ContainerNetworkCheck{Mode: "bridge"}
}

// podmanRootless asks podman whether it runs rootless.
func podmanRootless(ctx context.Context, runtime string) (bool, error) {
	if _, err := exec.LookPath(runtime); err != nil {
		return false, fmt.Errorf("runtime %q not found in PATH", runtime)
	}
	ctx, cancel := context.WithTimeout(ctx, containerProbeTimeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, runtime, "info", "--format", "{{.Host.Security.Rootless}}")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return false, fmt.Errorf("%s info failed: %s", runtime, msg)
	}
	switch out := strings.TrimSpace(stdout.String()); out {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("%s info printed %q, not true or false", runtime, out)
	}
}
