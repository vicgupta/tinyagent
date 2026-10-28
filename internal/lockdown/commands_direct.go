package lockdown

// CommandNetworkDirect is run.started's lockdown.commands.network_before_taint
// when sandboxed commands have direct network (sandbox_network) until the
// first taint takes it away.
const CommandNetworkDirect = "direct"

// SetCommandsDirectUntilTaint records that the confined commands
// (ConfineCommands) have direct network until the first taint, when the
// narrow hook takes it away: a sandbox_network sandbox. Background
// processes started before taint keep their direct network, so
// StopsProcessesAtTaint reports true. Call it after ConfineCommands and
// before the run starts.
func (s *State) SetCommandsDirectUntilTaint() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commands.direct = true
}

// StopsProcessesAtTaint reports whether the first taint must stop the
// running background processes because lockdown can't restrict their
// network: commands aren't confined, or had direct network until taint.
func (s *State) StopsProcessesAtTaint() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.commands.confined || s.commands.direct
}
