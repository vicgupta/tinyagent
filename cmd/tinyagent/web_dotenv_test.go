package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestWebDotenvLowercaseModel reproduces "tinyagent web is not accepting
// .env which has model=...": a lowercase model= line used to be loaded as
// a variable named "model" that nothing reads, so web fell back to
// openai/gpt-4o. It is now an alias of AGENT_MODEL.
func TestWebDotenvLowercaseModel(t *testing.T) {
	newACPProvider(t)
	root := acpWorkspace(t)
	dotenvDirAt(t, root, "model=openai/gpt-4o-mini\n", "AGENT_MODEL", "model")
	if err := loadDotenv(root, false); err != nil {
		t.Fatal(err)
	}
	c := startWebArgs(t, root)
	if st := c.state(); st["model"] != "openai/gpt-4o-mini" || st["handoff"] != false {
		t.Fatalf("web model = %v handoff = %v, want openai/gpt-4o-mini from .env model=", st["model"], st["handoff"])
	}
}

// TestWebDotenvHandoffModel starts web with a codex-exec handoff model from
// .env (no OPENAI_API_KEY) and checks the state flags it as a handoff.
func TestWebDotenvHandoffModel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake CLI is a shell script")
	}
	root := acpWorkspace(t)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\necho codex-cli 9.9.9\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OPENAI_API_KEY", "")
	os.Unsetenv("OPENAI_API_KEY")
	dotenvDirAt(t, root, "AGENT_MODEL=codex-exec/gpt-6-luna\n", "AGENT_MODEL", "model")
	if err := loadDotenv(root, false); err != nil {
		t.Fatal(err)
	}
	c := startWebArgs(t, root)
	if st := c.state(); st["model"] != "codex-exec/gpt-6-luna" || st["handoff"] != true {
		t.Fatalf("web model = %v handoff = %v", st["model"], st["handoff"])
	}
}
