package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/vicgupta/tinyagent/internal/sandboxnet"
)

// containerProxy is the opt-in lockdown proxy of the container environment
// (#171, untrusted_content.lockdown.container_proxy). At the first taint the
// container is disconnected from its runtime networks as usual, then
// connected to network, an --internal runtime network with no route out,
// whose only way out is proxy, listening on the network's gateway address
// on the host and limited to untrusted_content.lockdown.allowed_domains.
type containerProxy struct {
	network string
	proxy   *sandboxnet.Proxy
	env     []string
	// connected is set (under containerLockdown.mu) once the container is
	// on network; commands then get the proxy variables.
	connected bool
	// connectErr is set when connecting failed; the container then has no
	// network at all, and commands run without the proxy.
	connectErr error
}

// isPodman reports whether rt is podman, whose inspect output has another
// shape than docker's.
func isPodman(rt string) bool {
	return strings.Contains(filepath.Base(rt), "podman")
}

// gatewayFormat prints the gateway addresses of a runtime network.
func gatewayFormat(rt string) string {
	if isPodman(rt) {
		return `{{range .Subnets}}{{.Gateway}} {{end}}`
	}
	return `{{range .IPAM.Config}}{{.Gateway}} {{end}}`
}

// ipv4Gateway returns the first IPv4 address in out.
func ipv4Gateway(out string) (netip.Addr, bool) {
	for _, f := range strings.Fields(out) {
		if a, err := netip.ParseAddr(f); err == nil && a.Is4() && !a.IsUnspecified() {
			return a, true
		}
	}
	return netip.Addr{}, false
}

// listenGateway is how the proxy listens on the network's gateway. Tests
// replace it.
var listenGateway = func(gw netip.Addr) (net.Listener, error) {
	return net.Listen("tcp", netip.AddrPortFrom(gw, 0).String())
}

func (c *Container) run(ctx context.Context, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, c.Runtime, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s %s failed: %s", c.Runtime, strings.Join(args[:min(2, len(args))], " "), msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// PrepareLockdownProxy readies the opt-in container lockdown proxy: it
// creates an --internal runtime network, starts a proxy limited to allowed
// (untrusted_content.lockdown.allowed_domains) on that network's gateway
// address, and arranges for RemoveLockdownNetwork to connect the container
// to the network after disconnecting it, so later commands reach only those
// hosts, through HTTP_PROXY and friends. Call it after
// PrepareLockdownNetwork found a network to remove. On any error it cleans
// up and leaves the network switch as it was: commands then have no network
// after taint.
func (c *Container) PrepareLockdownProxy(ctx context.Context, allowed []string) error {
	if !c.LockdownNetworkPrepared() {
		return errors.New("container: lockdown network switch not prepared")
	}
	if c.lockdown.proxy != nil {
		return errors.New("container: lockdown proxy already prepared")
	}
	rules, err := sandboxnet.ParseRules(allowed)
	if err != nil {
		return fmt.Errorf("untrusted_content.lockdown.allowed_domains %v", err)
	}
	if len(rules) == 0 {
		return errors.New("untrusted_content.lockdown.allowed_domains is empty")
	}
	name := "tinyagent-lockdown-" + newToken()
	if _, err := c.run(ctx, "network", "create", "--internal", "--label", ContainerLabel+"=1", name); err != nil {
		return err
	}
	removeNet := func() {
		rctx, cancel := context.WithTimeout(context.Background(), containerProxyCleanup)
		defer cancel()
		c.run(rctx, "network", "rm", name)
	}
	out, err := c.run(ctx, "network", "inspect", "--format", gatewayFormat(c.Runtime), name)
	if err != nil {
		removeNet()
		return err
	}
	gw, ok := ipv4Gateway(out)
	if !ok {
		removeNet()
		return fmt.Errorf("container: internal network %s has no IPv4 gateway (inspect printed %q)", name, out)
	}
	ln, err := listenGateway(gw)
	if err != nil {
		removeNet()
		return fmt.Errorf("container: can't listen on the internal network's gateway %s: %v", gw, err)
	}
	p := sandboxnet.NewLockdownProxy(rules, ln)
	go p.Serve()
	c.lockdown.proxy = &containerProxy{
		network: name,
		proxy:   p,
		env:     proxyVars("http://" + ln.Addr().String()),
	}
	return nil
}

// containerProxyCleanup bounds removing the internal network.
const containerProxyCleanup = 30 * time.Second

// LockdownProxyNetwork returns the internal network the container joins at
// taint, "" without a lockdown proxy.
func (c *Container) LockdownProxyNetwork() string {
	if !c.LockdownNetworkPrepared() || c.lockdown.proxy == nil {
		return ""
	}
	return c.lockdown.proxy.network
}

// connectProxy connects the disconnected container to the proxy's internal
// network and checks it is on that network only. Called with l.mu held.
// A failed connect leaves the container with no network, which is safe;
// it is recorded and commands run without the proxy. Ending up on any
// other network is an error that makes commands fail closed.
func (c *Container) connectProxy(ctx context.Context, cp *containerProxy) error {
	if _, err := c.run(ctx, "network", "connect", cp.network, c.ID); err != nil {
		cp.connectErr = err
		return nil
	}
	_, nets, err := c.inspectNetworks(ctx)
	if err != nil {
		return fmt.Errorf("container: checking the network after joining the lockdown network: %v", err)
	}
	for _, n := range nets {
		if n != cp.network {
			return fmt.Errorf("container: connected to %s besides the lockdown network after taint", n)
		}
	}
	if len(nets) == 0 {
		cp.connectErr = fmt.Errorf("container: not on %s after connecting", cp.network)
		return nil
	}
	cp.connected = true
	return nil
}

// LockdownProxyErr returns why the container could not join the lockdown
// proxy's network at taint (it then has no network), nil otherwise.
func (c *Container) LockdownProxyErr() error {
	if !c.LockdownNetworkPrepared() || c.lockdown.proxy == nil {
		return nil
	}
	c.lockdown.mu.Lock()
	defer c.lockdown.mu.Unlock()
	return c.lockdown.proxy.connectErr
}

// lockdownProxyEnv returns the proxy variables for commands once the
// container is on the lockdown proxy's network. Called with l.mu held.
func (l *containerLockdown) proxyEnvLocked() []string {
	if l.proxy == nil || !l.proxy.connected {
		return nil
	}
	return l.proxy.env
}

// TakeNetworkDenials returns the requests the container lockdown proxy
// refused since the last call.
func (c *Container) TakeNetworkDenials() []sandboxnet.Denial {
	if !c.LockdownNetworkPrepared() || c.lockdown.proxy == nil {
		return nil
	}
	return c.lockdown.proxy.proxy.TakeDenials()
}

// closeLockdownProxy stops the proxy and removes its network. Call it
// after the container is removed.
func (c *Container) closeLockdownProxy() error {
	if !c.LockdownNetworkPrepared() || c.lockdown.proxy == nil {
		return nil
	}
	cp := c.lockdown.proxy
	cp.proxy.Close()
	ctx, cancel := context.WithTimeout(context.Background(), containerProxyCleanup)
	defer cancel()
	if _, err := c.run(ctx, "network", "rm", cp.network); err != nil {
		return fmt.Errorf("container: removing the lockdown network: %v", err)
	}
	return nil
}
