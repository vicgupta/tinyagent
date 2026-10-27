package config

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestOTelConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	write(t, p, "otel:\n  enabled: true\n  endpoint: https://collector:4318\n  protocol: http/json\n  headers_env: [MY_HEADERS]\n  timeout: 3s\n  service_name: ci\n  content: true\n  max_attribute_bytes: 100\n")
	cfg, err := Load(p)
	want := OTel{Enabled: true, Endpoint: "https://collector:4318", Protocol: "http/json", HeadersEnv: []string{"MY_HEADERS"},
		Timeout: "3s", ServiceName: "ci", Content: true, MaxAttributeBytes: 100}
	if err != nil || !reflect.DeepEqual(cfg.OTel, want) {
		t.Fatalf("otel = %+v, %v", cfg.OTel, err)
	}
	for body, msg := range map[string]string{
		"otel:\n  bogus: 1\n":                 "bogus",
		"otel:\n  protocol: grpc\n":           "http/protobuf or http/json",
		"otel:\n  timeout: soon\n":            "otel.timeout",
		"otel:\n  timeout: -1s\n":             "otel.timeout",
		"otel:\n  endpoint: collector:4318\n": "otel.endpoint",
		"otel:\n  max_attribute_bytes: -1\n":  "max_attribute_bytes",
		"otel:\n  headers_env: [\"\"]\n":      "headers_env",
	} {
		write(t, p, body)
		_, err := Load(p)
		var invalid *InvalidError
		if !errors.As(err, &invalid) || !strings.Contains(err.Error(), msg) {
			t.Errorf("%q: %v", body, err)
		}
	}
}
