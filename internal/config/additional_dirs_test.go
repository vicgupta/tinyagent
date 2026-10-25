package config

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAdditionalDirsConfig(t *testing.T) {
	if d := Default().AdditionalDirs; d != nil {
		t.Fatalf("default additional_dirs = %v", d)
	}
	p := filepath.Join(t.TempDir(), "c.yaml")
	write(t, p, "additional_dirs:\n  - {path: /srv/ref, mode: read}\n  - {path: /srv/out, mode: write}\n")
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []AdditionalDir{{Path: "/srv/ref", Mode: "read"}, {Path: "/srv/out", Mode: "write"}}
	if !reflect.DeepEqual(cfg.AdditionalDirs, want) {
		t.Fatalf("got %+v", cfg.AdditionalDirs)
	}
	for _, c := range []struct{ yaml, want string }{
		{"additional_dirs: [{mode: read}]", "path is required"},
		{"additional_dirs: [{path: ref, mode: read}]", "absolute path"},
		{"additional_dirs: [{path: /srv/ref}]", "mode is required"},
		{"additional_dirs: [{path: /srv/ref, mode: rw}]", "must be read or write"},
	} {
		write(t, p, c.yaml+"\n")
		_, err := Load(p)
		var invalid *InvalidError
		if err == nil || !strings.Contains(err.Error(), c.want) || !errors.As(err, &invalid) {
			t.Errorf("%s: err = %v, want InvalidError naming %q", c.yaml, err, c.want)
		}
	}
	write(t, p, "additional_dirs: [{path: /srv/ref, mode: read, extra: 1}]\n")
	if _, err := Load(p); err == nil {
		t.Error("unknown key in additional_dirs entry accepted")
	}
}
