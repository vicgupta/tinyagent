package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
)

func TestInitConfig(t *testing.T) {
	for _, custom := range []bool{false, true} {
		name := "default"
		if custom {
			name = "custom"
		}
		t.Run(name, func(t *testing.T) {
			hits := checkEnv(t)
			path, err := config.DefaultPath()
			if err != nil {
				t.Fatal(err)
			}
			args := []string{"--init-config"}
			if custom {
				path = filepath.Join(t.TempDir(), "nested", "starter.yaml")
				args = append(args, "-c", path)
			}
			code, out, stderr := runCLI(t, args...)
			if code != 0 || !strings.Contains(out, path) {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, out, stderr)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := config.Load(path); err != nil {
				t.Fatal(err)
			}
			// Also activate the examples so unknown keys in comments cannot drift.
			active := strings.ReplaceAll(string(before), "\n# ", "\n")
			examples := filepath.Join(t.TempDir(), "examples.yaml")
			if err := os.WriteFile(examples, []byte(active), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := config.Load(examples); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
				t.Errorf("mode %o", info.Mode().Perm())
			}
			code, _, stderr = runCLI(t, args...)
			if code != 2 || !strings.Contains(stderr, path) {
				t.Fatalf("second run: exit %d, stderr %q", code, stderr)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(before) {
				t.Fatalf("existing file changed: %v", err)
			}
			if hits.Load() != 0 {
				t.Errorf("made %d requests", hits.Load())
			}
		})
	}
}

func TestInitConfigConflicts(t *testing.T) {
	for _, args := range [][]string{{"-t", "x"}, {"--task="}, {"--resume", "missing.json"}, {"--rewind", "missing.json"}, {"--update"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			flags := append([]string{"--init-config", "-c", path}, args...)
			code, _, stderr := runCLI(t, flags...)
			if code != 2 || !strings.Contains(stderr, "can't be combined") {
				t.Fatalf("exit %d, stderr %q", code, stderr)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("created config: %v", err)
			}
		})
	}
}

func TestInitConfigWriteErrors(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "file")
	if err := os.WriteFile(blocked, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dir, filepath.Join(blocked, "config.yaml")} {
		code, _, stderr := runCLI(t, "--init-config", "-c", path)
		if code != 2 || !strings.Contains(stderr, path) {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
	}
}

func TestInitConfigRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.yaml")
	link := filepath.Join(dir, "config.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	code, _, stderr := runCLI(t, "--init-config", "-c", link)
	if code != 2 || !strings.Contains(stderr, link) {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("created symlink target: %v", err)
	}
}
