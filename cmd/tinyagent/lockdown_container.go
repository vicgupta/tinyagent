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
// nothing. untrusted_content.lockdown.allowed_domains isn't supported in
// the container yet, so commands get no network at all after taint. When
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
	if len(st.CommandDomains()) > 0 {
		fmt.Fprintln(stderr, "lockdown: note: untrusted_content.lockdown.allowed_domains has no effect in the container environment: container commands have no network after taint")
	}
	if denied {
		st.ConfineCommands(false, nil)
		st.SetCommandsInContainer(false)
		return
	}
	st.ConfineCommands(false, func([]string) {
		ctx, cancel := context.WithTimeout(context.Background(), containerLockdownTimeout)
		defer cancel()
		if err := c.RemoveLockdownNetwork(ctx); err != nil {
			st.RefuseCommands()
			fmt.Fprintln(stderr, "lockdown: error removing the container's network; commands are refused for the rest of the run:", err)
		}
	})
	st.SetCommandsInContainer(true)
	fmt.Fprintln(stderr, "lockdown: container: commands keep the container's network until taint; then it is disconnected, so later commands and running processes have no network")
}
