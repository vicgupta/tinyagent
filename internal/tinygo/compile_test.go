package tinygo

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSources(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "a_test.go"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, PDKFile), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644)
	files, k1, err := Sources(dir)
	if err != nil || len(files) != 1 || files["a.go"] == nil {
		t.Fatalf("%v %v", files, err)
	}
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n//x\n"), 0o644)
	if _, k2, _ := Sources(dir); k2 == k1 {
		t.Fatal("key unchanged after edit")
	}
	if _, _, err := Sources(t.TempDir()); err == nil {
		t.Fatal("empty dir accepted")
	}
}

func TestBuildUsesRunner(t *testing.T) {
	var gotArgv, gotEnv []string
	var gotWritable, gotWork string
	run := func(_ context.Context, writable, workdir string, argv, env []string, out io.Writer) error {
		gotWritable, gotWork, gotArgv, gotEnv = writable, workdir, argv, env
		if _, err := os.Stat(filepath.Join(workdir, PDKFile)); err != nil {
			t.Error("no PDK shim")
		}
		return os.WriteFile(argv[3], []byte("\x00asm"), 0o644)
	}
	wasm, err := Build(context.Background(), "/x/tinygo", map[string][]byte{"a.go": []byte("package main\n")}, run)
	if err != nil || string(wasm) != "\x00asm" {
		t.Fatalf("%q %v", wasm, err)
	}
	if !strings.HasPrefix(gotWork, gotWritable) || gotArgv[0] != "/x/tinygo" || !strings.Contains(strings.Join(gotArgv, " "), "-target=wasip1") {
		t.Fatalf("argv %v work %s writable %s", gotArgv, gotWork, gotWritable)
	}
	if !strings.Contains(strings.Join(gotEnv, " "), "GOPROXY=off") {
		t.Fatalf("env %v", gotEnv)
	}
	if _, err := os.Stat(gotWritable); !os.IsNotExist(err) {
		t.Fatal("temp dir left behind")
	}
	if _, err := Build(context.Background(), "/x/tinygo", map[string][]byte{"a.go": nil}, nil); err == nil {
		t.Fatal("built without a sandbox runner")
	}
	fail := func(_ context.Context, _, _ string, _, _ []string, out io.Writer) error {
		io.WriteString(out, "syntax error")
		return errors.New("exit 1")
	}
	if _, err := Build(context.Background(), "/x/tinygo", map[string][]byte{"a.go": nil}, fail); err == nil || !strings.Contains(err.Error(), "syntax error") {
		t.Fatalf("err = %v", err)
	}
	if _, err := Build(context.Background(), "/x/tinygo", map[string][]byte{"../a.go": nil}, run); err == nil {
		t.Fatal("bad file name accepted")
	}
}
