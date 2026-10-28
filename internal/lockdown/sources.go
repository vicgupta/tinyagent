package lockdown

import (
	"fmt"
	"strings"
)

// SourceACPClientMCP is a result from an MCP server an ACP client supplied
// to the session (see SetACPClientMCPServers).
const SourceACPClientMCP = "acp_client_mcp"

// ConfigurableSources are the taint sources untrusted_content.sources can
// choose from, in the order they are described. --untrusted-task and a
// resumed untrusted checkpoint are not configurable: asking for them is
// explicit, and a tainted history stays tainted.
var ConfigurableSources = []string{SourceWebFetch, SourceMCP, SourceReview, SourceACPClientMCP}

func configurable(source string) bool {
	for _, s := range ConfigurableSources {
		if s == source {
			return true
		}
	}
	return false
}

// ParseSources checks an untrusted_content.sources list and returns the
// enabled set. A nil list (the key is absent) enables every configurable
// source and returns nil. An explicit empty list, an unknown name and a
// duplicate are errors, so a typo can't silently turn a source off.
func ParseSources(list []string) (map[string]bool, error) {
	if list == nil {
		return nil, nil
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("untrusted_content.sources is empty; omit the key to use every source (%s)", strings.Join(ConfigurableSources, ", "))
	}
	set := make(map[string]bool, len(list))
	for _, s := range list {
		if !configurable(s) {
			return nil, fmt.Errorf("untrusted_content.sources: unknown source %q (valid: %s)", s, strings.Join(ConfigurableSources, ", "))
		}
		if set[s] {
			return nil, fmt.Errorf("untrusted_content.sources: duplicate source %q", s)
		}
		set[s] = true
	}
	return set, nil
}

// SetSources limits the configurable sources that taint the run to list,
// in the untrusted_content.sources syntax (nil means all of them). Call it
// before the run starts.
func (s *State) SetSources(list []string) error {
	set, err := ParseSources(list)
	if err != nil {
		return err
	}
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabledSources = set
	return nil
}

// TaintsFrom reports whether a result from source taints the run. It is
// always false when lockdown is off, and always true for the sources that
// aren't configurable (an untrusted task, a resumed untrusted checkpoint).
func (s *State) TaintsFrom(source string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.taintsFromLocked(source)
}

func (s *State) taintsFromLocked(source string) bool {
	return s.enabledSources == nil || !configurable(source) || s.enabledSources[source]
}

// describeSources lists the sources that taint, for the startup line.
func (s *State) describeSources() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, src := range ConfigurableSources {
		if src == SourceACPClientMCP && !s.acp {
			// Only ACP sessions have client-supplied servers.
			continue
		}
		if s.taintsFromLocked(src) {
			out = append(out, src)
		}
	}
	return strings.Join(append(out, "untrusted task"), ", ")
}
