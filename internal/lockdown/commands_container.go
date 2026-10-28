package lockdown

// CommandEnvironmentContainer is the environment value reported in
// run.started for commands confined in the container environment.
const CommandEnvironmentContainer = "container"

// SetCommandsInContainer records that the confined commands
// (ConfineCommands) run in the container environment. removedAtTaint says
// the container has network until the first taint, when the narrow hook
// disconnects it; otherwise it never had any (--network none). Processes
// running in the container lose their network with it, so background
// processes are left running. Call it after ConfineCommands and before the
// run starts.
func (s *State) SetCommandsInContainer(removedAtTaint bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commands.container = true
	s.commands.containerRemoved = removedAtTaint
}

// RefuseCommands withdraws ConfineCommands: commands are refused after
// taint from now on. It is the fail-closed path when the narrow hook
// can't restrict their network.
func (s *State) RefuseCommands() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commands = commandPolicy{domains: s.commands.domains}
}
