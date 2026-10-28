// Package lockdown holds the run-level taint state of the opt-in
// untrusted-content lockdown (untrusted_content.enabled / --lockdown, issue
// #171). A run becomes tainted when content from an untrusted source enters
// the model's context (a web_fetch or MCP result, a review diff, an
// --untrusted-task task). From then until the run ends the outbound channels
// tinyagent controls are refused: commands, web_fetch outside its lockdown
// allowlist, and MCP tools.
//
// Taint is decided by source, never by content, and records hold only
// source names, tool names, ids, steps and hosts: no fetched content, MCP
// results, URLs, query strings or environment values.
package lockdown

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/vicgupta/tinyagent/internal/sandboxnet"
)

// Taint sources.
const (
	SourceWebFetch      = "web_fetch"
	SourceMCP           = "mcp"
	SourceReview        = "review"
	SourceUntrustedTask = "untrusted_task"
	// SourceResume marks a resumed checkpoint whose history already holds
	// untrusted content.
	SourceResume = "resume"
)

// Channels lockdown restricts after taint.
const (
	ChannelCommand  = "command"
	ChannelWebFetch = "web_fetch"
	ChannelMCP      = "mcp"
)

// Notice is appended once per run to the tool result that first taints it.
const Notice = "[tinyagent: untrusted content received; commands, web_fetch and MCP tools are restricted for the rest of this run]"

// Source is one recorded taint: what brought untrusted content into
// context and where.
type Source struct {
	Source     string `json:"source"`
	Step       int    `json:"step"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Tool       string `json:"tool,omitempty"`
	// Host is the web_fetch URL host (no scheme, path or query).
	Host string `json:"host,omitempty"`
	// Resumed marks a source restored from a checkpoint: it tainted an
	// earlier invocation of the run.
	Resumed bool `json:"resumed,omitempty"`
}

// Fields returns the taint event fields.
func (s Source) Fields() map[string]any {
	f := map[string]any{"source": s.Source, "step": s.Step}
	if s.ToolCallID != "" {
		f["tool_call_id"] = s.ToolCallID
	}
	if s.Tool != "" {
		f["tool"] = s.Tool
	}
	if s.Host != "" {
		f["host"] = s.Host
	}
	if s.Resumed {
		f["resumed"] = true
	}
	return f
}

func (s Source) key() string { return s.Source + "\x00" + s.Tool + "\x00" + s.Host }

// Denial is one call refused by lockdown.
type Denial struct {
	Channel    string `json:"channel"`
	Tool       string `json:"tool"`
	ToolCallID string `json:"tool_call_id"`
	Step       int    `json:"step"`
	Host       string `json:"host,omitempty"`
}

// Fields returns the lockdown.denied event fields.
func (d Denial) Fields() map[string]any {
	f := map[string]any{"channel": d.Channel, "tool": d.Tool, "tool_call_id": d.ToolCallID, "step": d.Step}
	if d.Host != "" {
		f["host"] = d.Host
	}
	return f
}

// State is one run's lockdown state. It is shared by the run's agent and
// its children (workers, delegates), so it is safe for concurrent use. A
// nil *State means lockdown is off: it is never tainted and refuses
// nothing.
type State struct {
	mu            sync.Mutex
	webFetchRules []sandboxnet.Rule
	webFetchCount int
	sources       []Source
	seen          map[string]bool
	denials       []Denial
	notified      bool
	// enabledSources are the configurable sources that taint; nil means
	// all of them (see SetSources).
	enabledSources map[string]bool
	// webFetchDomains is the configured lockdown allowlist, for records.
	webFetchDomains []string
	// mcpServers are the configured MCP servers' lockdown settings, by
	// alias (see SetMCPServers).
	mcpServers map[string]MCPServerPolicy
	// commands is the after-taint command policy (see ConfineCommands).
	commands commandPolicy
	// containerProxy is untrusted_content.lockdown.container_proxy (see
	// SetContainerProxy).
	containerProxy bool
	// acp marks an ACP session; clientMCP holds the aliases of the MCP
	// servers its client supplied (see SetACPClientMCPServers).
	acp       bool
	clientMCP map[string]bool
}

// New returns an untainted state whose web_fetch lockdown allowlist is
// webFetchAllowed, in the sandbox_allowed_domains syntax.
func New(webFetchAllowed []string) (*State, error) {
	rules, err := sandboxnet.ParseRules(webFetchAllowed)
	if err != nil {
		return nil, fmt.Errorf("untrusted_content.lockdown.web_fetch_allowed_domains %v", err)
	}
	return &State{webFetchRules: rules, webFetchCount: len(rules), webFetchDomains: append([]string(nil), webFetchAllowed...), seen: map[string]bool{}}, nil
}

// Tainted reports whether untrusted content has entered the run's context.
func (s *State) Tainted() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sources) > 0
}

// Taint records src. added is false when the same source, tool and host
// were already recorded, or when src's source is turned off in
// untrusted_content.sources. first is true only for the run's first taint, the
// one whose tool result carries Notice; a taint recorded before the run
// starts (task, review, resume) has no result and uses it up, since every
// refusal explains itself anyway.
func (s *State) Taint(src Source) (added, first bool) {
	if s == nil {
		return false, false
	}
	s.mu.Lock()
	if !s.taintsFromLocked(src.Source) || s.seen[src.key()] {
		s.mu.Unlock()
		return false, false
	}
	s.seen[src.key()] = true
	s.sources = append(s.sources, src)
	first = !s.notified
	s.notified = true
	narrow := s.takeNarrowLocked()
	s.mu.Unlock()
	if narrow != nil {
		narrow()
	}
	return true, first
}

// Sources returns the recorded taint sources in order.
func (s *State) Sources() []Source {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Source(nil), s.sources...)
}

// First returns the run's first taint source; ok is false when untainted.
func (s *State) First() (src Source, ok bool) {
	if s == nil {
		return Source{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sources) == 0 {
		return Source{}, false
	}
	return s.sources[0], true
}

// Deny records a refused call.
func (s *State) Deny(d Denial) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.denials = append(s.denials, d)
}

// Denials returns the refused calls in order.
func (s *State) Denials() []Denial {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Denial(nil), s.denials...)
}

// WebFetchAllowed reports whether web_fetch may still read host:port: always
// before taint, and after taint only when it matches the lockdown
// allowlist.
func (s *State) WebFetchAllowed(host string, port int) bool {
	if s == nil || !s.Tainted() {
		return true
	}
	host = sandboxnet.NormalizeHost(host)
	for _, r := range s.webFetchRules {
		if r.Allows(host, port) {
			return true
		}
	}
	return false
}

// WebFetchGate is the per-hop check web_fetch runs before every request,
// redirects included: after taint, a host outside the lockdown allowlist is
// refused before any connection.
func (s *State) WebFetchGate(host string, port int) error {
	if s.WebFetchAllowed(host, port) {
		return nil
	}
	return fmt.Errorf("lockdown: host %s (port %d) is not in untrusted_content.lockdown.web_fetch_allowed_domains and this run has read untrusted content", host, port)
}

// Describe is the startup line printed when lockdown is enabled.
func (s *State) Describe() string {
	if s == nil {
		return ""
	}
	return fmt.Sprintf("lockdown: on (sources: %s; after taint: %s, web_fetch → %d domains, %s)", s.describeSources(), s.describeCommands(), s.webFetchCount, s.describeMCP())
}

// URLHostPort returns the host and port a web_fetch URL would connect to
// first. ok is false for a URL web_fetch could not fetch; lockdown refuses
// those after taint rather than guessing.
func URLHostPort(raw string) (host string, port int, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", 0, false
	}
	host = sandboxnet.NormalizeHost(u.Hostname())
	if host == "" {
		return "", 0, false
	}
	port = 80
	if u.Scheme == "https" {
		port = 443
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return "", 0, false
		}
		port = n
	}
	return host, port, true
}

// RefusalMessage is the tool error the model sees for a refused call.
func RefusalMessage(tool, channel string, first Source, host string) string {
	why := fmt.Sprintf("this run has read untrusted content (%s, step %d)", first.Source, first.Step)
	var detail string
	switch channel {
	case ChannelCommand:
		detail = "; commands can't run after taint unless they are sandboxed with a network policy lockdown can narrow (--sandbox without sandbox_network)"
	case ChannelWebFetch:
		if host != "" {
			detail = fmt.Sprintf("; host %s is not in untrusted_content.lockdown.web_fetch_allowed_domains", host)
		} else {
			detail = "; the url is not one lockdown can check"
		}
	case ChannelMCP:
		detail = "; MCP tools are not callable after taint unless their server is lockdown_allowed: true"
	}
	return fmt.Sprintf("error: lockdown: %s refused because %s%s. The rest of the run (file tools, edits, plans, submission) still works", tool, why, detail)
}
