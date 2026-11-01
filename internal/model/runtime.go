package model

import (
	"net/http"
	"time"
)

// Runtime holds the provider settings that used to be package-level
// globals: the HTTP client, the retry policy, and the price table. Each run
// or test can build its own Runtime and pass it through Options, so pricing
// and retry behavior are facts about a request rather than about the
// process. A nil *Runtime means DefaultRuntime.
type Runtime struct {
	// HTTP sends provider requests. nil uses a client with a 10-minute
	// timeout.
	HTTP *http.Client
	// RetryBase is the first retry's backoff; each later retry doubles it
	// (plus jitter). Zero or negative uses 1s.
	RetryBase time.Duration
	// MaxAttempts is the total number of tries for a transient failure
	// (HTTP 429, 5xx or a network error). Zero or negative uses 6.
	MaxAttempts int
	// Prices maps a model name prefix to USD per million input/output tokens
	// and, optionally, the cache-read price as a multiple of the input price
	// (0 means the standard 0.1). The longest matching prefix wins. nil uses
	// the built-in table; a non-nil empty map prices every model at 0.
	Prices map[string][3]float64
	// PriceOverride, when non-zero, replaces the price table's input and
	// output prices. The table's cache-read multiplier still applies.
	PriceOverride [2]float64
}

// Production defaults, used when a Runtime field is left zero.
const (
	defaultHTTPTimeout = 10 * time.Minute
	defaultRetryBase   = time.Second
	defaultMaxAttempts = 6
)

var defaultHTTPClient = &http.Client{Timeout: defaultHTTPTimeout}

// builtinPrices is the built-in price table. Never mutate it; DefaultPrices
// returns a copy for callers that want to start from it.
var builtinPrices = map[string][3]float64{
	"gpt-4o":            {2.50, 10.00},
	"gpt-4o-mini":       {0.15, 0.60},
	"gpt-4.1":           {2.00, 8.00},
	"gpt-4.1-mini":      {0.40, 1.60},
	"gpt-4.1-nano":      {0.10, 0.40},
	"claude-sonnet-4":   {3.00, 15.00},
	"claude-opus-4":     {15.00, 75.00},
	"claude-haiku-4-5":  {1.00, 5.00},
	"claude-3-5-haiku":  {0.80, 4.00},
	"claude-3-7-sonnet": {3.00, 15.00},
	"claude-sonnet-5-5": {2.00, 10.00, 0.05},
	"claude-opus-5-5":   {4.00, 20.00, 0.05},
	"claude-fable-5-1":  {10.00, 50.00, 0.025},
}

// DefaultPrices returns a copy of the built-in price table.
func DefaultPrices() map[string][3]float64 {
	m := make(map[string][3]float64, len(builtinPrices))
	for k, v := range builtinPrices {
		m[k] = v
	}
	return m
}

// NewRuntime returns a Runtime with today's production behavior: a 10-minute
// HTTP timeout, a 1s retry base, 6 attempts, the built-in price table, and
// no price override.
func NewRuntime() *Runtime {
	return &Runtime{
		HTTP:        &http.Client{Timeout: defaultHTTPTimeout},
		RetryBase:   defaultRetryBase,
		MaxAttempts: defaultMaxAttempts,
		Prices:      DefaultPrices(),
	}
}

// DefaultRuntime is used whenever Options.Runtime (or an adapter's Runtime
// field) is nil. It is process-wide: the CLI sets its PriceOverride from
// config once per run. Tests should build their own Runtime instead of
// changing it.
var DefaultRuntime = NewRuntime()

// orDefault returns r, or DefaultRuntime when r is nil.
func (r *Runtime) orDefault() *Runtime {
	if r == nil {
		return DefaultRuntime
	}
	return r
}

func (r *Runtime) httpClient() *http.Client {
	if r := r.orDefault(); r.HTTP != nil {
		return r.HTTP
	}
	return defaultHTTPClient
}

func (r *Runtime) retryBase() time.Duration {
	if r := r.orDefault(); r.RetryBase > 0 {
		return r.RetryBase
	}
	return defaultRetryBase
}

func (r *Runtime) maxAttempts() int {
	if r := r.orDefault(); r.MaxAttempts > 0 {
		return r.MaxAttempts
	}
	return defaultMaxAttempts
}

func (r *Runtime) prices() map[string][3]float64 {
	if r := r.orDefault(); r.Prices != nil {
		return r.Prices
	}
	return builtinPrices
}

func (r *Runtime) priceOverride() [2]float64 {
	return r.orDefault().PriceOverride
}
