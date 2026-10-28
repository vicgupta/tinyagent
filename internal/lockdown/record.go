package lockdown

// EnabledSources lists the configurable sources that taint the run, in
// ConfigurableSources order.
func (s *State) EnabledSources() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, src := range ConfigurableSources {
		if s.taintsFromLocked(src) {
			out = append(out, src)
		}
	}
	return out
}

// WebFetchDomains returns the configured web_fetch lockdown allowlist.
func (s *State) WebFetchDomains() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.webFetchDomains...)
}

// Info returns the lockdown config and current taint state for
// run.started: names and host patterns only, never content.
func (s *State) Info() map[string]any {
	if s == nil {
		return nil
	}
	domains := s.WebFetchDomains()
	if domains == nil {
		domains = []string{}
	}
	return map[string]any{
		"enabled":                   true,
		"sources":                   s.EnabledSources(),
		"web_fetch_allowed_domains": domains,
		"tainted":                   s.Tainted(),
	}
}

// Restore records the taint sources and denials of a resumed checkpoint
// ahead of any recorded so far, so a run that was tainted stays tainted
// with its original sources. The sources are marked Resumed, and the
// first-taint notice counts as given: the conversation already carries
// it. Call it before the run starts.
func (s *State) Restore(sources []Source, denials []Denial) {
	if s == nil || len(sources)+len(denials) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var restored []Source
	for _, src := range sources {
		src.Resumed = true
		if s.seen[src.key()] {
			continue
		}
		s.seen[src.key()] = true
		restored = append(restored, src)
	}
	if len(restored) > 0 {
		s.sources = append(restored, s.sources...)
		s.notified = true
	}
	s.denials = append(append([]Denial(nil), denials...), s.denials...)
}
