package config

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFileAccessConfig(t *testing.T) {
	if Default().FileAccess.Configured() {
		t.Error("file_access is on by default")
	}
	p := filepath.Join(t.TempDir(), "config.yaml")
	write(t, p, "file_access:\n  deny_read: [\".env\", \"secrets/\"]\n  deny_write: [go.sum, \"/vendor/**\"]\n")
	c, err := load(t, p)
	if err != nil {
		t.Fatal(err)
	}
	want := FileAccess{DenyRead: []string{".env", "secrets/"}, DenyWrite: []string{"go.sum", "/vendor/**"}}
	if !reflect.DeepEqual(c.FileAccess, want) || !c.FileAccess.Configured() {
		t.Errorf("file_access = %+v", c.FileAccess)
	}
	write(t, p, "file_access:\n  deny_exec: [x]\n")
	if _, err := load(t, p); err == nil || !strings.Contains(err.Error(), "deny_exec") {
		t.Errorf("unknown key: %v", err)
	}
	for _, bad := range []string{`""`, `"!keep.txt"`, `"["`, `"a/../b"`, `"/"`} {
		write(t, p, "file_access:\n  deny_write: ["+bad+"]\n")
		_, err := load(t, p)
		var invalid *InvalidError
		if !errors.As(err, &invalid) || !strings.Contains(err.Error(), "file_access.deny_write") {
			t.Errorf("%s: err = %v", bad, err)
		}
	}
}
