// Package telemetry exports runs as OpenTelemetry traces over OTLP/HTTP.
// The Exporter is an eventlog.Sink: it builds spans from the same events as
// --json and --log-db, and sends them with a stdlib-only OTLP encoder
// (protobuf or JSON).
package telemetry

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/vicgupta/tinyagent/internal/config"
)

const (
	// DefaultEndpoint is the OTLP/HTTP traces URL used when nothing is set.
	DefaultEndpoint = "http://localhost:4318/v1/traces"
	// DefaultTimeout bounds each export and the final flush.
	DefaultTimeout = 10 * time.Second
	// DefaultMaxAttributeBytes caps each content attribute.
	DefaultMaxAttributeBytes = 4096
	// DefaultServiceName is the resource service.name.
	DefaultServiceName = "tinyagent"
)

// Settings is the resolved exporter configuration.
type Settings struct {
	// Endpoint is the full URL spans are POSTed to.
	Endpoint string
	// Protocol is http/protobuf or http/json.
	Protocol string
	// Headers are sent with every export. Their values are secrets: they
	// are never printed.
	Headers     map[string]string
	Timeout     time.Duration
	ServiceName string
	// Resource holds OTEL_RESOURCE_ATTRIBUTES other than service.name.
	Resource          map[string]string
	Content           bool
	MaxAttributeBytes int
}

// Host returns the endpoint's host, the only part of it tinyagent prints.
func (s Settings) Host() string {
	if u, err := url.Parse(s.Endpoint); err == nil {
		return u.Host
	}
	return ""
}

// HeaderValues returns the header values, for redaction.
func (s Settings) HeaderValues() []string {
	var vals []string
	for _, v := range s.Headers {
		vals = append(vals, v)
	}
	return vals
}

// Resolve combines the otel config with the standard OTEL_EXPORTER_OTLP_*,
// OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES variables read through
// getenv. Config values win; a per-signal (TRACES) variable wins over the
// generic one. Errors never contain header values.
func Resolve(c config.OTel, getenv func(string) string) (Settings, error) {
	s := Settings{Content: c.Content, MaxAttributeBytes: c.MaxAttributeBytes, Headers: map[string]string{}, Resource: map[string]string{}}
	if s.MaxAttributeBytes == 0 {
		s.MaxAttributeBytes = DefaultMaxAttributeBytes
	}
	src := "otel.endpoint"
	switch {
	case c.Endpoint != "":
		s.Endpoint = signalURL(c.Endpoint)
	case getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != "":
		s.Endpoint, src = getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"), "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"
	case getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "":
		s.Endpoint, src = signalURL(getenv("OTEL_EXPORTER_OTLP_ENDPOINT")), "OTEL_EXPORTER_OTLP_ENDPOINT"
	default:
		s.Endpoint = DefaultEndpoint
	}
	if u, err := url.Parse(s.Endpoint); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return s, fmt.Errorf("%s must be an http:// or https:// URL", src)
	}

	s.Protocol, src = first(getenv, c.Protocol, "otel.protocol", "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "OTEL_EXPORTER_OTLP_PROTOCOL")
	if s.Protocol == "" {
		s.Protocol = "http/protobuf"
	} else if !config.ValidOTelProtocol(s.Protocol) {
		return s, config.OTelProtocolError(src, s.Protocol)
	}

	s.Timeout = DefaultTimeout
	if c.Timeout != "" {
		d, err := time.ParseDuration(c.Timeout)
		if err != nil || d <= 0 {
			return s, fmt.Errorf("otel.timeout %q must be a positive duration such as 10s", c.Timeout)
		}
		s.Timeout = d
	} else if v, src := first(getenv, "", "", "OTEL_EXPORTER_OTLP_TRACES_TIMEOUT", "OTEL_EXPORTER_OTLP_TIMEOUT"); v != "" {
		ms, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || ms <= 0 {
			return s, fmt.Errorf("%s %q must be a positive number of milliseconds", src, v)
		}
		s.Timeout = time.Duration(ms) * time.Millisecond
	}

	for _, name := range append([]string{"OTEL_EXPORTER_OTLP_HEADERS", "OTEL_EXPORTER_OTLP_TRACES_HEADERS"}, c.HeadersEnv...) {
		v := getenv(name)
		if v == "" {
			if strings.HasPrefix(name, "OTEL_") && !slices.Contains(c.HeadersEnv, name) {
				continue
			}
			return s, fmt.Errorf("otel.headers_env names %s, which is not set", name)
		}
		if err := parseList(v, s.Headers); err != nil {
			return s, fmt.Errorf("%s: %w", name, err)
		}
	}

	if err := parseList(getenv("OTEL_RESOURCE_ATTRIBUTES"), s.Resource); err != nil {
		return s, fmt.Errorf("OTEL_RESOURCE_ATTRIBUTES: %w", err)
	}
	s.ServiceName = c.ServiceName
	if s.ServiceName == "" {
		s.ServiceName = getenv("OTEL_SERVICE_NAME")
	}
	if s.ServiceName == "" {
		s.ServiceName = s.Resource["service.name"]
	}
	if s.ServiceName == "" {
		s.ServiceName = DefaultServiceName
	}
	delete(s.Resource, "service.name")
	return s, nil
}

// signalURL appends the traces path to an OTLP base URL.
func signalURL(base string) string {
	if strings.HasSuffix(base, "/") {
		return base + "v1/traces"
	}
	return base + "/v1/traces"
}

// first returns the config value (named cfgName) if set, else the first set
// variable among names, with where it came from.
func first(getenv func(string) string, cfg, cfgName string, names ...string) (string, string) {
	if cfg != "" {
		return cfg, cfgName
	}
	for _, n := range names {
		if v := getenv(n); v != "" {
			return strings.TrimSpace(v), n
		}
	}
	return "", ""
}

// parseList adds the entries of a W3C Baggage-style k=v,k2=v2 list
// (percent-encoded values) to into. Errors name the entry by position only,
// since values may be secrets.
func parseList(v string, into map[string]string) error {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	for i, entry := range strings.Split(v, ",") {
		k, val, ok := strings.Cut(entry, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return fmt.Errorf("entry %d is not key=value", i+1)
		}
		dec, err := url.PathUnescape(strings.TrimSpace(val))
		if err != nil {
			return fmt.Errorf("entry %d (%s) has an invalid percent-encoded value", i+1, k)
		}
		into[k] = dec
	}
	return nil
}
