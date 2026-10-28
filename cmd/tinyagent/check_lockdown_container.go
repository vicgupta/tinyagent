package main

import (
	"context"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/lockdown"
	"github.com/vicgupta/tinyagent/internal/tools"
)

// checkContainerLockdown sets st's after-taint command policy for --check in
// the container environment, the way confineContainerLockdownCommands will
// for a run, and returns a note for the lockdown line. It predicts the
// container's network mode from container.run_args (asking podman whether
// it runs rootless when they set none) without starting a container: --network
// none confines commands with no network, a mode that can't be disconnected
// keeps them refused after taint, and anything else has its network removed
// at taint. When the mode can't be predicted, it reports the policy for a
// disconnectable network and says so; the run decides when the container
// starts.
func checkContainerLockdown(st *lockdown.State, c config.Container) string {
	r := tools.CheckContainerLockdownNetwork(context.Background(), c.Runtime, c.RunArgs)
	switch {
	case r.Unknown != "":
		st.ConfineCommands(false, nil)
		st.SetCommandsInContainer(true)
		return "; container network: not checked (" + r.Unknown + "); a run checks it when the container starts"
	case r.Err != nil:
		return "; container network " + containerModeText(r) + ": " + r.Err.Error()
	case r.Denied:
		st.ConfineCommands(false, nil)
		st.SetCommandsInContainer(false)
		return "; container network none (from container.run_args)"
	}
	if st.ContainerProxy() && len(st.CommandDomains()) > 0 {
		st.ConfineCommands(true, nil)
		st.SetCommandsInContainer(true)
		return "; container network " + containerModeText(r) + "; container_proxy: an internal network and proxy are set up when the container starts"
	}
	st.ConfineCommands(false, nil)
	st.SetCommandsInContainer(true)
	return "; container network " + containerModeText(r)
}

func containerModeText(r tools.ContainerNetworkCheck) string {
	if r.FromRunArgs {
		return r.Mode + " (from container.run_args)"
	}
	return r.Mode + " (runtime default)"
}
