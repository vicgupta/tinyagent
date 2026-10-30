// Package ratchet stores project rules recorded from agent failures (issue
// #249): each rule keeps the incident that caused it and an expiry date or
// review condition, so stale rules can be pruned as models improve. Rules
// live in <cwd>/.tinyagent/ratchet.toml; a project without the file pays
// nothing. Active rules become guidance at run start, and a rule with a
// deny matcher is also enforced as a permissions deny rule.
package ratchet

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/vicgupta/tinyagent/internal/config"
)

// RelPath is the rules file relative to --cwd.
const RelPath = ".tinyagent/ratchet.toml"

// DateLayout is the format of created and expires.
const DateLayout = "2006-01-02"

// DefaultExpiryDays is how long a rule recorded without --ratchet-expires or
// --ratchet-review stays active.
const DefaultExpiryDays = 90

// Default tools a deny matcher applies to when tool is left empty.
const (
	DefaultCommandTools = "bash|start_process"
	DefaultPathTools    = "write_file|edit_file|apply_patch"
	DefaultHostTools    = "web_fetch"
)

// Rule is one recorded rule.
type Rule struct {
	// ID is r1, r2, ...; assigned by Add.
	ID string `toml:"id"`
	// Rule is the guidance shown to the model.
	Rule string `toml:"rule"`
	// Incident is what went wrong and led to the rule.
	Incident string `toml:"incident"`
	// Created is the YYYY-MM-DD date the rule was recorded.
	Created string `toml:"created"`
	// Expires is the YYYY-MM-DD date from which the rule is skipped.
	Expires string `toml:"expires,omitempty"`
	// Review is a condition under which the rule should be reconsidered.
	Review string `toml:"review,omitempty"`
	// Deny, when set, also enforces the rule as a permissions deny rule.
	Deny *Deny `toml:"deny,omitempty"`
}

// Deny is an enforceable matcher with the permissions rule syntax. At most
// one of Command, Path and Host may be set.
type Deny struct {
	Tool    string `toml:"tool,omitempty"`
	Command string `toml:"command,omitempty"`
	Path    string `toml:"path,omitempty"`
	Host    string `toml:"host,omitempty"`
}

type file struct {
	Rules []Rule `toml:"rule"`
}

// Path returns the rules file for root.
func Path(root string) string {
	return filepath.Join(root, filepath.FromSlash(RelPath))
}

// Validate checks a rule's fields.
func (r Rule) Validate() error {
	if strings.TrimSpace(r.Rule) == "" {
		return errors.New("rule text is empty")
	}
	if strings.TrimSpace(r.Incident) == "" {
		return errors.New("incident is empty")
	}
	if _, err := time.Parse(DateLayout, r.Created); err != nil {
		return fmt.Errorf("created %q is not a YYYY-MM-DD date", r.Created)
	}
	if r.Expires != "" {
		if _, err := time.Parse(DateLayout, r.Expires); err != nil {
			return fmt.Errorf("expires %q is not a YYYY-MM-DD date", r.Expires)
		}
	}
	if r.Expires == "" && strings.TrimSpace(r.Review) == "" {
		return errors.New("needs an expires date or a review condition")
	}
	if d := r.Deny; d != nil {
		n := 0
		for _, s := range []string{d.Command, d.Path, d.Host} {
			if s != "" {
				n++
			}
		}
		if n > 1 {
			return errors.New("deny sets more than one of command, path and host")
		}
		if n == 0 && d.Tool == "" {
			return errors.New("deny needs a tool, command, path or host")
		}
	}
	return nil
}

// Expired reports whether r's expiry date is on or before today.
func (r Rule) Expired(today time.Time) bool {
	if r.Expires == "" {
		return false
	}
	exp, err := time.Parse(DateLayout, r.Expires)
	if err != nil {
		return false
	}
	y, m, d := today.Date()
	return !time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Before(exp)
}

// Load reads the rules in path. A missing file is no rules and no error.
// Errors name the rule's position.
func Load(path string) ([]Rule, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f file
	md, err := toml.Decode(string(b), &f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		return nil, fmt.Errorf("%s: unknown key %s", path, und[0])
	}
	seen := map[string]bool{}
	for i, r := range f.Rules {
		if r.ID == "" {
			return nil, fmt.Errorf("%s: rule %d: id is empty", path, i+1)
		}
		if seen[r.ID] {
			return nil, fmt.Errorf("%s: rule %d: duplicate id %q", path, i+1, r.ID)
		}
		seen[r.ID] = true
		if err := r.Validate(); err != nil {
			return nil, fmt.Errorf("%s: rule %s: %w", path, r.ID, err)
		}
	}
	return f.Rules, nil
}

// Split separates rules into active and expired as of today, keeping order.
func Split(rules []Rule, today time.Time) (active, expired []Rule) {
	for _, r := range rules {
		if r.Expired(today) {
			expired = append(expired, r)
		} else {
			active = append(active, r)
		}
	}
	return active, expired
}

// Section renders active rules as a system prompt section, or "" for none.
func Section(active []Rule) string {
	if len(active) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Project rules\n\nThese rules were recorded after earlier agent mistakes in this project (" + RelPath + "). Follow them. Rules marked enforced are also blocked by the permission policy.\n")
	for _, r := range active {
		fmt.Fprintf(&b, "\n- [%s] %s (incident: %s", r.ID, oneLine(r.Rule), oneLine(r.Incident))
		if r.Deny != nil {
			b.WriteString("; enforced")
		}
		b.WriteString(")")
	}
	return b.String()
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// PermissionRules returns a deny rule for each active rule with a matcher.
func PermissionRules(active []Rule) []config.PermissionRule {
	var out []config.PermissionRule
	for _, r := range active {
		d := r.Deny
		if d == nil {
			continue
		}
		tool := d.Tool
		if tool == "" {
			switch {
			case d.Command != "":
				tool = DefaultCommandTools
			case d.Path != "":
				tool = DefaultPathTools
			case d.Host != "":
				tool = DefaultHostTools
			}
		}
		out = append(out, config.PermissionRule{
			Tool:     tool,
			Command:  d.Command,
			Path:     d.Path,
			Host:     d.Host,
			Decision: config.DecisionDeny,
			Message:  fmt.Sprintf("blocked by project rule %s: %s (incident: %s)", r.ID, oneLine(r.Rule), oneLine(r.Incident)),
		})
	}
	return out
}

// Add appends r to the rules in path, creating the file and its directory
// when needed. It assigns the next id and today's created date, and with no
// expires and no review sets expires DefaultExpiryDays from now.
func Add(path string, r Rule, now time.Time) (Rule, error) {
	rules, err := Load(path)
	if err != nil {
		return Rule{}, err
	}
	r.ID = nextID(rules)
	r.Created = now.Format(DateLayout)
	if r.Expires == "" && strings.TrimSpace(r.Review) == "" {
		r.Expires = now.AddDate(0, 0, DefaultExpiryDays).Format(DateLayout)
	}
	if err := r.Validate(); err != nil {
		return Rule{}, err
	}
	rules = append(rules, r)
	if err := save(path, rules); err != nil {
		return Rule{}, err
	}
	return r, nil
}

// Remove deletes the rule with id from path.
func Remove(path, id string) error {
	rules, err := Load(path)
	if err != nil {
		return err
	}
	out := rules[:0]
	found := false
	for _, r := range rules {
		if r.ID == id {
			found = true
			continue
		}
		out = append(out, r)
	}
	if !found {
		return fmt.Errorf("no rule with id %q", id)
	}
	return save(path, out)
}

func nextID(rules []Rule) string {
	n := 0
	for _, r := range rules {
		if v, err := strconv.Atoi(strings.TrimPrefix(r.ID, "r")); err == nil && strings.HasPrefix(r.ID, "r") && v > n {
			n = v
		}
	}
	return "r" + strconv.Itoa(n+1)
}

func save(path string, rules []Rule) error {
	var buf bytes.Buffer
	buf.WriteString("# tinyagent project rules (ratchet). Commit this file; see docs/ratchet.md.\n\n")
	if err := toml.NewEncoder(&buf).Encode(file{Rules: rules}); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
