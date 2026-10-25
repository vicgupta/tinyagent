package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestImagesConfig(t *testing.T) {
	if d := Default(); d.Images.Enabled || d.MaxImageBytes != DefaultMaxImageBytes {
		t.Errorf("defaults = %+v, %d", d.Images, d.MaxImageBytes)
	}
	p := filepath.Join(t.TempDir(), "config.yaml")
	write(t, p, "images:\n  enabled: true\nmax_image_bytes: 1000\n")
	c, err := load(t, p)
	if err != nil || !c.Images.Enabled || c.MaxImageBytes != 1000 {
		t.Fatalf("config = %+v %d, err %v", c.Images, c.MaxImageBytes, err)
	}
	write(t, p, "images:\n  enabeld: true\n")
	if _, err := load(t, p); err == nil || !strings.Contains(err.Error(), "enabeld") {
		t.Errorf("typo: err = %v", err)
	}
	for _, v := range []string{"0", "-5"} {
		write(t, p, "max_image_bytes: "+v+"\n")
		if _, err := load(t, p); err == nil || !strings.Contains(err.Error(), "max_image_bytes must be >= 1") {
			t.Errorf("max_image_bytes %s: err = %v", v, err)
		}
	}
}
