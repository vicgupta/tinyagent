package config

import (
	"fmt"

	"github.com/vicgupta/tinyagent/internal/sandboxnet"
)

// UntrustedContent configures the opt-in untrusted-content lockdown (issue
// #171). When enabled, a run that reads untrusted content (a web_fetch or
// MCP result, a --review diff, an --untrusted-task task) is tainted, and
// from then on its outbound channels are refused: commands, web_fetch
// outside Lockdown.WebFetchAllowedDomains, and MCP tools. Like mcp_servers
// and memory it comes only from the user config, -c or a profile, never
// from the repository.
type UntrustedContent struct {
	// Enabled turns lockdown on (off by default; --lockdown and
	// --lockdown=false override it).
	Enabled bool `yaml:"enabled"`
	// Lockdown is what stays reachable after taint.
	Lockdown UntrustedLockdown `yaml:"lockdown"`
}

// UntrustedLockdown is the after-taint allowlist.
type UntrustedLockdown struct {
	// WebFetchAllowedDomains are the web_fetch hosts still readable after
	// taint, in the sandbox_allowed_domains syntax; each must also be in
	// web_fetch.allowed_domains to be fetched at all. Empty (the default)
	// refuses every web_fetch after taint. A bare * is rejected.
	WebFetchAllowedDomains []string `yaml:"web_fetch_allowed_domains,omitempty"`
}

// Validate checks the lockdown allowlist syntax.
func (u UntrustedContent) Validate() error {
	if _, err := sandboxnet.ParseRules(u.Lockdown.WebFetchAllowedDomains); err != nil {
		return fmt.Errorf("untrusted_content.lockdown.web_fetch_allowed_domains %v", err)
	}
	return nil
}
