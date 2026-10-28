package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/vicgupta/tinyagent/internal/lockdown"
	"github.com/vicgupta/tinyagent/internal/tools"
)

// containerLockdownTimeout bounds inspecting and disconnecting the
// container's networks.
const containerLockdownTimeout = 30 * time.Second

// confineLockdownEnv keeps commands running after taint where env lets
// lockdown confine their network: the container environment (see
// confineContainerLockdownCommands) or the local sandbox (see
// confineLockdownCommands).
func confineLockdownEnv(st *lockdown.State, env tools.Environment, stderr io.Writer) {
	if c, ok := env.(*tools.Container); ok && c != nil {
		confineContainerLockdownCommands(st, c, stderr)
		return
	}
	confineLockdownCommands(st, tools.CommandSandbox(env), stderr)
}

// confineContainerLockdownCommands keeps container commands running after
// taint (#171): the first taint disconnects the container from every
// network, so later commands, and the processes already running there,
// have no network. A container started with --network none needs
// nothing. With untrusted_content.lockdown.container_proxy and
// allowed_domains, the disconnected container then joins an internal
// runtime network whose only way out is a proxy limited to those domains;
// otherwise (or when that can't be set up) commands get no network at all
// after taint. When
// the network can't be removed by disconnecting (host networking, rootless
// slirp4netns or pasta), commands stay refused after taint, and it says
// why. If disconnecting fails at taint, commands are refused from then on.
func confineContainerLockdownCommands(st *lockdown.State, c *tools.Container, stderr io.Writer) {
	ctx, cancel := context.WithTimeout(context.Background(), containerLockdownTimeout)
	denied, err := c.PrepareLockdownNetwork(ctx)
	cancel()
	if err != nil {
		fmt.Fprintf(stderr, "lockdown: commands will be refused after taint: the container's network can't be removed at taint (%v)\n", err)
		return
	}
	proxied := false
	switch domains := st.CommandDomains(); {
	case len(domains) == 0:
	case denied:
		fmt.Fprintln(stderr, "lockdown: note: untrusted_content.lockdown.allowed_domains has no effect in the container environment: the container has no network (--network none)")
	case !st.ContainerProxy():
		fmt.Fprintln(stderr, "lockdown: note: untrusted_content.lockdown.allowed_domains has no effect in the container environment unless untrusted_content.lockdown.container_proxy is true: container commands have no network after taint")
	default:
		ctx, cancel := context.WithTimeout(context.Background(), containerLockdownTimeout)
		err := c.PrepareLockdownProxy(ctx, domains)
		cancel()
		if err != nil {
			fmt.Fprintf(stderr, "lockdown: note: container_proxy not available, so container commands have no network after taint: %v\n", err)
		} else {
			proxied = true
		}
	}
	if denied {
		st.ConfineCommands(false, nil)
		st.SetCommandsInContainer(false)
		return
	}
	st.ConfineCommands(proxied, func([]string) {
		ctx, cancel := context.WithTimeout(context.Background(), containerLockdownTimeout)
		defer cancel()
		if err := c.RemoveLockdownNetwork(ctx); err != nil {
			st.RefuseCommands()
			fmt.Fprintln(stderr, "lockdown: error removing the container's network; commands are refused for the rest of the run:", err)
			return
		}
		if err := c.LockdownProxyErr(); err != nil {
			fmt.Fprintln(stderr, "lockdown: the container could not join the lockdown proxy's network, so commands have no network:", err)
		}
	})
	st.SetCommandsInContainer(true)
	if proxied {
		fmt.Fprintf(stderr, "lockdown: container: commands keep the container's network until taint; then it is disconnected and joins %s, where they reach only untrusted_content.lockdown.allowed_domains through a proxy\n", c.LockdownProxyNetwork())
		return
	}
	fmt.Fprintln(stderr, "lockdown: container: commands keep the container's network until taint; then it is disconnected, so later commands and running processes have no network")
}
