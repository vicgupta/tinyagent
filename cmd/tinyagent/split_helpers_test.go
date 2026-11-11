package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/lockdown"
	"github.com/vicgupta/tinyagent/internal/ratchet"
)

// The session keeps the config as setupLockdown left it: the lockdown
// changes reach s.cfg, the config the rest of setup and every turn use.
func TestLoadSessionConfigKeepsLockdownChanges(t *testing.T) {
	root := t.TempDir()
	o := &options{}
	fs := newFlagSet(o)
	if err := fs.Parse([]string{"--cwd", root}); err != nil {
		t.Fatal(err)
	}
	s := &session{o: o, fs: fs, root: root, closeEnv: func() {}}
	m := sessionMode{setupLockdown: func(cfg *config.AgentConfig) (*lockdown.State, error) {
		cfg.ReadOnly = true
		return nil, nil
	}}
	if err := s.loadSessionConfig(m); err != nil {
		t.Fatal(err)
	}
	if !s.cfg.ReadOnly {
		t.Error("s.cfg lost the change setupLockdown made")
	}
}

// --ratchet-path=false is not the path command: as before the split, the
// rules are listed instead of the path being printed.
func TestRatchetPathFalseLists(t *testing.T) {
	cwd := ratchetEnv(t)
	code, out, errOut := runRatchetCLI(t, "--ratchet-path=false", "--cwd", cwd)
	if code != 0 || strings.HasSuffix(strings.TrimSpace(out), filepath.FromSlash(ratchet.RelPath)) {
		t.Fatalf("exit %d\nstdout %q\nstderr %s", code, out, errOut)
	}
}
