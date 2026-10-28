package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

// containerLockdown is the untrusted-content lockdown switch (#171) of a
// container whose network RemoveLockdownNetwork takes away at the first
// taint. It is shared by every copy of the *Container, workers included.
type containerLockdown struct {
	mu sync.Mutex
	// networks are the runtime networks the container is connected to.
	networks []string
	// removed: the container has been disconnected from every network.
	removed bool
	// err is set when removing the network failed; commands then fail
	// closed instead of running with network.
	err error
}

// lockdownInspectFormat prints the network mode, then the names of the
// connected networks.
const lockdownInspectFormat = `{{.HostConfig.NetworkMode}}|{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}`

// inspectNetworks returns the container's network mode and connected
// networks.
func (c *Container) inspectNetworks(ctx context.Context) (mode string, networks []string, err error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, c.Runtime, "inspect", "--format", lockdownInspectFormat, c.ID)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", nil, fmt.Errorf("%s inspect failed: %s", c.Runtime, msg)
	}
	out := strings.TrimSpace(stdout.String())
	mode, rest, ok := strings.Cut(out, "|")
	if !ok {
		return "", nil, fmt.Errorf("%s inspect printed %q, not a network mode and network list", c.Runtime, out)
	}
	for _, n := range strings.Fields(rest) {
		if n != "none" {
			networks = append(networks, n)
		}
	}
	return strings.TrimSpace(mode), networks, nil
}

// PrepareLockdownNetwork readies the container so the first taint can take
// its network away with RemoveLockdownNetwork. denied is true when the
// container already has no network (--network none in run_args), so there
// is nothing to remove. It fails when the network can't be removed by
// disconnecting runtime networks: host networking, another container's
// or a namespace's network, rootless slirp4netns or pasta networking.
func (c *Container) PrepareLockdownNetwork(ctx context.Context) (denied bool, err error) {
	if c == nil {
		return false, errors.New("no container")
	}
	mode, networks, err := c.inspectNetworks(ctx)
	if err != nil {
		return false, err
	}
	if mode == "none" && len(networks) == 0 {
		return true, nil
	}
	switch {
	case lockdownModeUnsupported(mode):
		return false, unsupportedModeErr(mode)
	case len(networks) == 0:
		return false, fmt.Errorf("network mode %q has no runtime network to disconnect", mode)
	}
	c.lockdown = &containerLockdown{networks: networks}
	return false, nil
}

// LockdownNetworkPrepared reports whether PrepareLockdownNetwork readied
// the container's network switch.
func (c *Container) LockdownNetworkPrepared() bool {
	return c != nil && c.lockdown != nil
}

// RemoveLockdownNetwork disconnects the container from every network
// PrepareLockdownNetwork found, for the rest of the run, and checks that
// none is left. Processes already running in the container lose their
// network too. Commands started while it runs wait for it; if it fails,
// every later command fails instead of running with network. It runs once;
// later calls return the first result.
func (c *Container) RemoveLockdownNetwork(ctx context.Context) error {
	if !c.LockdownNetworkPrepared() {
		return errors.New("container: lockdown network switch not prepared")
	}
	l := c.lockdown
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.removed || l.err != nil {
		return l.err
	}
	l.err = c.disconnect(ctx, l.networks)
	l.removed = l.err == nil
	return l.err
}

func (c *Container) disconnect(ctx context.Context, networks []string) error {
	var failed []string
	for _, n := range networks {
		out, err := exec.CommandContext(ctx, c.Runtime, "network", "disconnect", n, c.ID).CombinedOutput()
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s (%s)", n, strings.TrimSpace(firstNonEmpty(string(out), err.Error()))))
		}
	}
	_, left, err := c.inspectNetworks(ctx)
	if err != nil {
		return fmt.Errorf("container: checking the network after disconnecting: %v", err)
	}
	if len(left) > 0 {
		msg := fmt.Sprintf("container: still connected to %s after lockdown taint", strings.Join(left, ", "))
		if len(failed) > 0 {
			msg += "; disconnect failed for " + strings.Join(failed, ", ")
		}
		return errors.New(msg)
	}
	return nil
}

// lockdownErr waits for a running RemoveLockdownNetwork and returns the
// error that makes commands fail closed, nil when they may run.
func (c *Container) lockdownErr() error {
	if c.lockdown == nil {
		return nil
	}
	c.lockdown.mu.Lock()
	defer c.lockdown.mu.Unlock()
	if c.lockdown.err != nil {
		return fmt.Errorf("lockdown: commands refused: the container's network could not be removed at taint: %v", c.lockdown.err)
	}
	return nil
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
