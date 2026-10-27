// Package permissions implements the opt-in tool permission policy: a set of
// allow/ask/deny rules matched on a tool call's name and one primary argument
// (a shell command, a path, or a URL host). It is a declarative guardrail,
// not a security boundary; containment stays with --sandbox and containers.
//
// The policy is pure: it matches calls and returns decisions. Answering an
// "ask" is the caller's job (a terminal prompt, an ACP request_permission, or
// a headless fallback), which keeps this package free of I/O.
package permissions

import (
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/sandboxnet"
	"github.com/vicgupta/tinyagent/internal/tools"
)

// Decision is a permission outcome.
type Decision string

const (
	DecisionAllow Decision = "allow"
	DecisionAsk   Decision = "ask"
	DecisionDeny  Decision = "deny"
)

// Sources for a decision's Result.
const (
	SourceRule           = "rule"
	SourceDefault        = "default"
	SourceUser           = "user"
	SourceSession        = "session"
	SourceNonInteractive = "non_interactive"
)

// Call is one tool call to evaluate. The fields hold the call's primary
// argument(s) by family; a rule only reads the field its matcher names.
type Call struct {
	Tool    string
	Command string   // bash / start_process / command tool argv joined
	Paths   []string // file tool paths (relative to the working directory)
	Host    string   // web_fetch URL host (lowercased, no port)
}

// Argument returns the primary argument a prompt or event should show: the
// command, the first path, or the host.
func (c Call) Argument() string {
	switch {
	case c.Command != "":
		return c.Command
	case len(c.Paths) > 0:
		return c.Paths[0]
	case c.Host != "":
		return c.Host
	}
	return ""
}

// Key identifies a call for "allow always" session approvals: the tool name
// and its primary argument.
func (c Call) Key() string {
	return c.Tool + "\x00" + c.Argument()
}

// Result is the policy's decision for a call.
type Result struct {
	Decision Decision
	Source   string // SourceRule or SourceDefault
	// Rule is the index of the deciding rule, or -1 for the default.
	Rule    int
	Message string
}

// Request describes an ask the caller must answer.
type Request struct {
	Tool       string
	ToolCallID string
	Rule       int
	Argument   string // the primary argument, already secret-redacted
	Subagent   string
}

// Answer is a person's (or the non-interactive fallback's) answer to an ask.
type Answer struct {
	Decision Decision // allow or deny
	Always   bool     // allow this tool and argument for the rest of the run
	Reason   string   // deny reason shown to the model
	Channel  string   // terminal, acp, or "" for the permission event
}

// Summary records a policy for run.started, trajectories and --check.
type Summary struct {
	Default        string `json:"default"`
	NonInteractive string `json:"non_interactive"`
	Rules          int    `json:"rules"`
}

type matcherKind int

const (
	kindNone matcherKind = iota // tool-only rule
	kindCommand
	kindPath
	kindHost
)

// glob matches tool names and command text, where * is the only wildcard and
// everything else is literal.
type glob struct {
	re *regexp.Regexp
}

func compileGlob(pattern string) (glob, error) {
	var b strings.Builder
	b.WriteString("^")
	for _, r := range pattern {
		if r == '*' {
			b.WriteString(".*")
		} else {
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return glob{}, err
	}
	return glob{re: re}, nil
}

func (g glob) matches(s string) bool { return g.re.MatchString(s) }

// hostMatcher reuses the sandbox_allowed_domains syntax, matching hostnames
// only (ports are not part of a permission host rule).
type hostMatcher struct {
	host     string
	wildcard bool
	addr     netip.Addr
}

func compileHost(pattern string) (hostMatcher, error) {
	r, err := sandboxnet.ParseRule(pattern)
	if err != nil {
		return hostMatcher{}, err
	}
	return hostMatcher{host: r.Host, wildcard: r.Wildcard, addr: r.Addr}, nil
}

func (h hostMatcher) matches(host string) bool {
	host = sandboxnet.NormalizeHost(host)
	if h.addr.IsValid() {
		a, err := netip.ParseAddr(host)
		return err == nil && a.Unmap() == h.addr
	}
	if h.wildcard {
		return strings.HasSuffix(host, "."+h.host)
	}
	return host == h.host
}

// rule is one compiled permissions rule.
type rule struct {
	index    int
	tool     []glob
	decision Decision
	message  string
	kind     matcherKind
	command  glob
	path     *tools.AccessPattern
	host     hostMatcher
}

// Policy is a compiled permission policy. A nil *Policy enforces nothing.
type Policy struct {
	Default        Decision
	NonInteractive Decision
	cfg            config.Permissions
	rules          []rule
}

// New compiles cfg into a policy. It returns nil when cfg has no rules and no
// settings, so a config without a permissions key enforces nothing. Errors
// name the rule index, like config validation.
func New(cfg config.Permissions) (*Policy, error) {
	if !cfg.Configured() {
		return nil, nil
	}
	p := &Policy{cfg: cfg}
	p.Default = DecisionAllow
	if cfg.Default != "" {
		p.Default = Decision(cfg.Default)
	}
	p.NonInteractive = DecisionDeny
	if cfg.NonInteractive != "" {
		p.NonInteractive = Decision(cfg.NonInteractive)
	}
	for i, r := range cfg.Rules {
		rr := rule{index: i, decision: Decision(r.Decision), message: r.Message}
		for _, t := range strings.Split(r.Tool, "|") {
			g, err := compileGlob(strings.TrimSpace(t))
			if err != nil {
				return nil, fmt.Errorf("permissions.rules[%d]: tool %q: %v", i, t, err)
			}
			rr.tool = append(rr.tool, g)
		}
		switch {
		case r.Command != "":
			g, err := compileGlob(r.Command)
			if err != nil {
				return nil, fmt.Errorf("permissions.rules[%d]: command: %v", i, err)
			}
			rr.kind, rr.command = kindCommand, g
		case r.Path != "":
			ap, err := tools.CompileAccessPattern(r.Path)
			if err != nil {
				return nil, fmt.Errorf("permissions.rules[%d]: path: %v", i, err)
			}
			rr.kind, rr.path = kindPath, ap
		case r.Host != "":
			h, err := compileHost(r.Host)
			if err != nil {
				return nil, fmt.Errorf("permissions.rules[%d]: host: %v", i, err)
			}
			rr.kind, rr.host = kindHost, h
		}
		p.rules = append(p.rules, rr)
	}
	return p, nil
}

// RuleCount returns the number of rules in the policy.
func (p *Policy) RuleCount() int {
	if p == nil {
		return 0
	}
	return len(p.rules)
}

// Rules returns the configured rules (patterns are config data, not secrets).
func (p *Policy) Rules() []config.PermissionRule {
	if p == nil {
		return nil
	}
	return p.cfg.Rules
}

// Summary describes the policy for run.started, trajectories and --check.
func (p *Policy) Summary() Summary {
	if p == nil {
		return Summary{}
	}
	return Summary{Default: string(p.Default), NonInteractive: string(p.NonInteractive), Rules: len(p.rules)}
}

// Check evaluates the call and returns the decision. A nil policy allows.
func (p *Policy) Check(call Call) Result {
	if p == nil {
		return Result{Decision: DecisionAllow, Source: SourceDefault, Rule: -1}
	}
	segments, unsafe := splitCommand(call.Command)

	var deny, ask *rule
	var allowRule *rule   // first allow rule that contributed (for the event)
	allowCovered := false // a path/host/tool-only allow rule matched
	cmdCovered := make([]bool, len(segments))

	for i := range p.rules {
		r := &p.rules[i]
		if !r.matchesTool(call.Tool) {
			continue
		}
		switch r.kind {
		case kindCommand:
			anyMatch := r.command.matchesAny(segments)
			switch r.decision {
			case DecisionDeny:
				if anyMatch && deny == nil {
					deny = r
				}
			case DecisionAsk:
				if anyMatch && ask == nil {
					ask = r
				}
			case DecisionAllow:
				if r.command.markCoverage(segments, cmdCovered) && allowRule == nil {
					allowRule = r
				}
			}
		case kindPath:
			if !r.pathMatches(call.Paths) {
				continue
			}
			applySimple(r, &deny, &ask, &allowRule, &allowCovered)
		case kindHost:
			if !r.host.matches(call.Host) {
				continue
			}
			applySimple(r, &deny, &ask, &allowRule, &allowCovered)
		case kindNone:
			applySimple(r, &deny, &ask, &allowRule, &allowCovered)
		}
	}

	// A command is allowed by rules only when it has no command substitution
	// and every segment is covered by some allow command rule.
	if len(segments) > 0 && !unsafe {
		all := true
		for _, c := range cmdCovered {
			if !c {
				all = false
				break
			}
		}
		if all {
			allowCovered = true
			if allowRule == nil {
				allowRule = p.firstAllowCommandRule()
			}
		}
	}

	switch {
	case deny != nil:
		return Result{Decision: DecisionDeny, Source: SourceRule, Rule: deny.index, Message: deny.message}
	case ask != nil:
		return Result{Decision: DecisionAsk, Source: SourceRule, Rule: ask.index, Message: ask.message}
	case allowCovered:
		return Result{Decision: DecisionAllow, Source: SourceRule, Rule: ruleIndex(allowRule)}
	default:
		return Result{Decision: p.Default, Source: SourceDefault, Rule: -1}
	}
}

func ruleIndex(r *rule) int {
	if r == nil {
		return -1
	}
	return r.index
}

func (p *Policy) firstAllowCommandRule() *rule {
	for i := range p.rules {
		if p.rules[i].kind == kindCommand && p.rules[i].decision == DecisionAllow {
			return &p.rules[i]
		}
	}
	return nil
}

// applySimple applies a rule without a compound command: it matches whole, so
// deny/ask/allow all use the same "matched" test.
func applySimple(r *rule, deny, ask, allowRule **rule, allowCovered *bool) {
	switch r.decision {
	case DecisionDeny:
		if *deny == nil {
			*deny = r
		}
	case DecisionAsk:
		if *ask == nil {
			*ask = r
		}
	case DecisionAllow:
		if *allowRule == nil {
			*allowRule = r
		}
		*allowCovered = true
	}
}

func (r *rule) matchesTool(name string) bool {
	for _, g := range r.tool {
		if g.matches(name) {
			return true
		}
	}
	return false
}

func (r *rule) pathMatches(paths []string) bool {
	for _, p := range paths {
		if r.path.Match(p) {
			return true
		}
	}
	return false
}

func (g glob) matchesAny(segments []string) bool {
	for _, s := range segments {
		if g.matches(s) {
			return true
		}
	}
	return false
}

// markCoverage marks each segment covered by g and reports whether any new
// segment was covered.
func (g glob) markCoverage(segments []string, covered []bool) bool {
	any := false
	for i, s := range segments {
		if g.matches(s) && !covered[i] {
			covered[i] = true
			any = true
		}
	}
	return any
}

// splitCommand splits a shell command on unquoted &&, ||, ;, | and newlines,
// respecting single quotes, double quotes and backslash escapes. It returns
// the non-empty segments and whether the text contains command substitution
// ($(...), backticks) or an eval command, which never matches an allow rule.
func splitCommand(s string) (segments []string, unsafe bool) {
	var cur strings.Builder
	flush := func() {
		if t := strings.TrimSpace(cur.String()); t != "" {
			segments = append(segments, t)
		}
		cur.Reset()
	}
	i, n := 0, len(s)
	var quote byte
	for i < n {
		c := s[i]
		if quote != 0 {
			cur.WriteByte(c)
			if c == quote {
				quote = 0
			}
			i++
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
			cur.WriteByte(c)
			i++
		case '\\':
			cur.WriteByte(c)
			if i+1 < n {
				cur.WriteByte(s[i+1])
				i += 2
			} else {
				i++
			}
		case '`':
			unsafe = true
			cur.WriteByte(c)
			i++
		case '$':
			if i+1 < n && s[i+1] == '(' {
				unsafe = true
			}
			cur.WriteByte(c)
			i++
		case '&':
			if i+1 < n && s[i+1] == '&' {
				flush()
				i += 2
			} else {
				cur.WriteByte(c)
				i++
			}
		case '|':
			if i+1 < n && s[i+1] == '|' {
				flush()
				i += 2
			} else {
				flush()
				i++
			}
		case ';', '\n':
			flush()
			i++
		default:
			cur.WriteByte(c)
			i++
		}
	}
	flush()
	for _, seg := range segments {
		if f := strings.Fields(seg); len(f) > 0 && f[0] == "eval" {
			unsafe = true
		}
	}
	return segments, unsafe
}

// HostOf returns the lowercased hostname of a URL, or "" when it can't be
// parsed. It is the primary argument a host rule matches against.
func HostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return sandboxnet.NormalizeHost(u.Hostname())
}

// PortOf returns the port a URL reaches: its explicit port, or the scheme's
// default. It is unused by hostname matching but kept for symmetry with
// sandbox_allowed_domains.
func PortOf(rawURL string) int {
	u, err := url.Parse(rawURL)
	if err != nil {
		return 0
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			return n
		}
	}
	if u.Scheme == "https" {
		return 443
	}
	return 80
}
