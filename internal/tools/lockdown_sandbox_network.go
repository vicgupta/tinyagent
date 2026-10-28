package tools

import (
	"errors"
	"fmt"
	"sync"

	"github.com/vicgupta/tinyagent/internal/sandboxnet"
)

// lockdownNetwork is the network a sandbox_network sandbox switches to at
// the first taint (issue #171): denied, or the lockdown proxy limited to
// untrusted_content.lockdown.allowed_domains.
type lockdownNetwork struct {
	proxy *sandboxnet.Proxy // nil: network denied after taint
	env   []string

	mu sync.Mutex
	// switched is set once commands have lost their direct network.
	switched bool
}

func (l *lockdownNetwork) isSwitched() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.switched
}

// probeLockdownNetwork checks that this host can confine commands the way
// the switch needs: network denied, plus the network namespace when
// proxied. Tests replace it.
var probeLockdownNetwork = func(proxied bool) error {
	_, err := probeSandbox(false, proxied)
	return err
}

// PrepareLockdownNetwork readies a sandbox_network sandbox for the
// untrusted-content lockdown: at the first taint NarrowNetwork takes its
// commands' direct network away. Commands started after that have their
// network denied when allowed (untrusted_content.lockdown.allowed_domains)
// is empty, or reach only those hosts through a proxy tinyagent starts now.
// It fails, leaving the sandbox unchanged, when the sandbox has no direct
// network, allowed is invalid, or this host can't deny the network (or
// create the network namespace the proxy needs); lockdown then refuses
// commands after taint. Call it once, before the run starts and before
// workers copy the sandbox.
func (s *Sandbox) PrepareLockdownNetwork(allowed []string) error {
	if s == nil || !s.Network {
		return errors.New("sandbox: commands don't have direct network (sandbox_network)")
	}
	if s.lockdown != nil {
		return errors.New("sandbox: lockdown network already prepared")
	}
	rules, err := sandboxnet.ParseRules(allowed)
	if err != nil {
		return fmt.Errorf("untrusted_content.lockdown.allowed_domains %v", err)
	}
	proxied := len(rules) > 0
	if err := probeLockdownNetwork(proxied); err != nil {
		return err
	}
	ln := &lockdownNetwork{}
	if proxied {
		l, url, err := s.listenProxy()
		if err != nil {
			return fmt.Errorf("sandbox: starting the lockdown network proxy: %w", err)
		}
		ln.proxy = sandboxnet.NewLockdownProxy(rules, l)
		ln.env = proxyVars(url)
		go ln.proxy.Serve()
	}
	s.lockdown = ln
	return nil
}

// DirectUntilTaint reports whether commands keep their direct network
// (sandbox_network) until the first taint, when the lockdown takes it
// away (PrepareLockdownNetwork succeeded). Background processes started
// before then keep theirs, so the lockdown stops them at taint.
func (s *Sandbox) DirectUntilTaint() bool {
	return s != nil && s.lockdown != nil
}

// switchLockdownNetwork takes away the direct network of commands started
// from now on, in this sandbox and its copies. It is idempotent.
func (s *Sandbox) switchLockdownNetwork() {
	if s.lockdown == nil {
		return
	}
	s.lockdown.mu.Lock()
	s.lockdown.switched = true
	s.lockdown.mu.Unlock()
}
