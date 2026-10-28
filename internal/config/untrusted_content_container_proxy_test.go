package config

import (
	"errors"
	"strings"
	"testing"
)

func TestUntrustedLockdownContainerProxy(t *testing.T) {
	cfg, _, err := loadYAML(t, "untrusted_content:\n  enabled: true\n  lockdown:\n    allowed_domains: [proxy.golang.org]\n    container_proxy: true\n")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.UntrustedContent.Lockdown.ContainerProxy {
		t.Error("container_proxy not read")
	}
	cfg, _, err = loadYAML(t, "untrusted_content:\n  enabled: true\n")
	if err != nil || cfg.UntrustedContent.Lockdown.ContainerProxy {
		t.Errorf("default: %v, err %v", cfg.UntrustedContent.Lockdown.ContainerProxy, err)
	}
	for body, want := range map[string]string{
		"untrusted_content:\n  lockdown:\n    container_proxy: true\n":       "container_proxy needs untrusted_content.lockdown.allowed_domains",
		"untrusted_content:\n  lockdown:\n    container_proxy: yes please\n": "untrusted_content",
	} {
		_, _, err := loadYAML(t, body)
		var ie *InvalidError
		if err == nil || !errors.As(err, &ie) || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v", body, err)
		}
	}
}
