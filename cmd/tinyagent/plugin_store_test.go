package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/plugins"
)

func TestPluginsSubcommandStoreActions(t *testing.T) {
	for _, c := range []struct{ in, want []string }{
		{[]string{"plugins", "prune"}, []string{"--plugins-prune"}},
		{[]string{"plugins", "prune", "--keep", "2"}, []string{"--plugins-prune", "--plugins-keep", "2"}},
		{[]string{"plugins", "rollback", "kit__m", "abc"}, []string{"--plugins-rollback", "kit__m@abc"}},
	} {
		got, _, err := expandSubcommand(c.in)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%v -> %v %v, want %v", c.in, got, err, c.want)
		}
	}
	if _, _, err := expandSubcommand([]string{"plugins", "rollback", "kit__m"}); err == nil {
		t.Error("rollback with one value accepted")
	}
}

func runStoreCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	argv, _, err := expandSubcommand(args)
	if err != nil {
		t.Fatal(err)
	}
	var o options
	fs := newFlagSet(&o)
	if err := fs.Parse(argv); err != nil {
		t.Fatal(err)
	}
	var out, errOut strings.Builder
	code := runPluginStore(&o, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestPluginsPruneAndRollbackCLI(t *testing.T) {
	dir := t.TempDir()
	root, _ := canonicalRoot(dir)
	st := moduleStore(root)
	var hs []string
	for _, c := range []string{"a", "b", "c", "d"} {
		h, err := st.Put("kit__m", "wasm", []byte(c), plugins.Permissions{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		hs = append(hs, h)
	}
	st.SetCurrent("kit__m", hs[3])
	code, out, errOut := runStoreCLI(t, "plugins", "rollback", "kit__m", hs[0], "--cwd", dir)
	if code != 0 || !strings.Contains(out, "current is now "+hs[0]) {
		t.Fatalf("rollback: %d %q %q", code, out, errOut)
	}
	if code, _, errOut := runStoreCLI(t, "--plugins-rollback", "kit__m@"+strings.Repeat("f", 64), "--cwd", dir); code != 1 || !strings.Contains(errOut, "has no version") {
		t.Fatalf("missing hash: %d %q", code, errOut)
	}
	code, out, _ = runStoreCLI(t, "plugins", "prune", "--keep", "1", "--cwd", dir)
	if code != 0 || !strings.Contains(out, "pruned 2 version(s)") {
		t.Fatalf("prune: %d %q", code, out)
	}
	for _, h := range []string{hs[0], hs[3]} {
		if _, err := os.Stat(filepath.Join(st.Dir, "kit__m", h+".wasm")); err != nil {
			t.Fatalf("prune removed %s: %v", h[:8], err)
		}
	}
}
