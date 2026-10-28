package lockdown

// SetContainerProxy records untrusted_content.lockdown.container_proxy: in
// the container environment, commands reach CommandDomains after taint
// through a lockdown proxy on an internal runtime network instead of
// having no network. Call it before the run starts.
func (s *State) SetContainerProxy(on bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.containerProxy = on
}

// ContainerProxy reports untrusted_content.lockdown.container_proxy.
func (s *State) ContainerProxy() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.containerProxy
}
