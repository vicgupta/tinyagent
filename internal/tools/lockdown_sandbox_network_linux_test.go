//go:build linux

package tools

import (
	"context"
	"strings"
	"testing"
)

// The helper mode of a sandbox_network command switches from net-allow to
// net-deny (or net-proxy) once lockdown takes the direct network away.
func TestLockdownNetworkSwitchesHelperMode(t *testing.T) {
	fakeLockdownProbe(t, nil)
	for _, c := range []struct {
		allowed []string
		after   string
	}{
		{nil, "net-deny"},
		{[]string{"proxy.golang.org"}, "net-proxy="},
	} {
		sb := &Sandbox{Network: true, Writable: []string{t.TempDir()}}
		if err := sb.PrepareLockdownNetwork(c.allowed); err != nil {
			t.Fatal(err)
		}
		if mode := sb.wrap(context.Background(), []string{"true"}).Args[2]; mode != "net-allow" {
			t.Errorf("before taint: mode %q", mode)
		}
		if err := sb.NarrowNetwork(c.allowed); err != nil {
			t.Fatal(err)
		}
		cmd := sb.wrap(context.Background(), []string{"true"})
		if mode := cmd.Args[2]; !strings.HasPrefix(mode, c.after) {
			t.Errorf("after taint: mode %q, want %q", mode, c.after)
		}
		if (c.allowed != nil) != (cmd.SysProcAttr != nil) {
			t.Errorf("network namespace = %v", cmd.SysProcAttr != nil)
		}
		sb.Cleanup()
	}
}
