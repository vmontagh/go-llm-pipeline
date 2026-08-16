// Package doctor provides a reusable diagnostic engine that checks local
// environment readiness before batch LLM jobs. It is exposed as a library
// API so consumer applications can wire it into their own command surface
// (for example a "doctor" subcommand or a "--dry-run" preflight) instead of
// depending on a repo-level binary.
//
// A Runner executes a fixed set of checks and returns a Report of pass, warn,
// fail, and skip Results. Callers decide how to surface the Report: Report.Log
// emits it through an injected *slog.Logger, and Report.HasFailure reports
// whether any hard failure occurred so callers can choose a non-zero exit.
package doctor

import (
	"cmp"
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/vmontagh/go-llm-pipeline/pkg/provider/openai"
)

// Default configuration values used by DefaultConfig.
const (
	defaultTimeout       = 5 * time.Second
	defaultLatencyWarn   = 2 * time.Second
	defaultFileLimitMin  = 1024
	defaultFileLimitWarn = 4096
	defaultGeminiBaseURL = "https://generativelanguage.googleapis.com/v1beta"
	defaultOllamaHost    = "http://localhost:11434"
)

// Config declares the inputs for a diagnostic run. Consumers populate it from
// their own environment and flags; the doctor package never reads environment
// variables itself, keeping the engine deterministic and testable.
type Config struct {
	// Timeout bounds each individual network check.
	Timeout time.Duration
	// LatencyWarn is the hosted-API round-trip above which a reachable
	// endpoint is reported as a warning rather than a pass.
	LatencyWarn time.Duration
	// FileLimitMin is the soft file-descriptor limit below which the check
	// hard-fails.
	FileLimitMin uint64
	// FileLimitWarn is the soft file-descriptor limit below which the check
	// warns.
	FileLimitWarn uint64
	// OpenAIBaseURL is the base URL used for OpenAI reachability and key checks.
	OpenAIBaseURL string
	// GeminiBaseURL is the base URL used for Gemini reachability and key checks.
	GeminiBaseURL string
	// OpenAIAPIKey is the OpenAI credential to validate. Empty skips the check.
	OpenAIAPIKey string
	// GeminiAPIKey is the Gemini credential to validate. Empty skips the check.
	GeminiAPIKey string
	// OllamaHost is the base URL of the local Ollama instance to probe. An
	// empty host skips the Ollama check, so consumers that do not use Ollama
	// can opt out. DefaultConfig sets a localhost default.
	OllamaHost string
	// OllamaModel, when set, asserts a specific model is loaded and runs the
	// Ollama context-window preflight against it.
	OllamaModel string
}

// DefaultConfig returns a Config populated with sensible defaults. Callers
// typically override credentials, hosts, and thresholds before running.
func DefaultConfig() Config {
	return Config{
		Timeout:       defaultTimeout,
		LatencyWarn:   defaultLatencyWarn,
		FileLimitMin:  defaultFileLimitMin,
		FileLimitWarn: defaultFileLimitWarn,
		OpenAIBaseURL: openai.DefaultBaseURL,
		GeminiBaseURL: defaultGeminiBaseURL,
		OllamaHost:    defaultOllamaHost,
	}
}

// FileLimitReader reads the process file-descriptor limits. It is defined at
// the consumer side so tests can inject a fake instead of touching the OS.
type FileLimitReader interface {
	// ReadFileLimit returns the soft and hard open-file limits. It returns
	// ErrFileLimitUnsupported on platforms without rlimit support.
	ReadFileLimit() (soft, hard uint64, err error)
}

// Option customizes a Runner at construction time.
type Option func(*Runner)

// WithHTTPClient injects the *http.Client used for network checks. Tests pass
// a client backed by a fake RoundTripper to avoid live calls.
func WithHTTPClient(client *http.Client) Option {
	return func(r *Runner) {
		if client != nil {
			r.client = client
		}
	}
}

// WithFileLimitReader injects the FileLimitReader used for the file-descriptor
// check, overriding the platform default.
func WithFileLimitReader(reader FileLimitReader) Option {
	return func(r *Runner) {
		if reader != nil {
			r.fdReader = reader
		}
	}
}

// Runner executes the diagnostic checks described by its Config.
type Runner struct {
	cfg      Config
	logger   *slog.Logger
	client   *http.Client
	fdReader FileLimitReader
}

// withDefaults returns a copy of c with each zero-value operational field
// replaced by its DefaultConfig value, so a partially populated Config (for
// example, one that only sets an API key) still gets sane timeouts, thresholds,
// and hosted-API base URLs. OllamaHost is deliberately not defaulted: an empty
// host means "skip the Ollama check", which lets consumers that do not use
// Ollama opt out cleanly.
func (c *Config) withDefaults() Config {
	d := DefaultConfig()
	return Config{
		Timeout:       cmp.Or(c.Timeout, d.Timeout),
		LatencyWarn:   cmp.Or(c.LatencyWarn, d.LatencyWarn),
		FileLimitMin:  cmp.Or(c.FileLimitMin, d.FileLimitMin),
		FileLimitWarn: cmp.Or(c.FileLimitWarn, d.FileLimitWarn),
		OpenAIBaseURL: cmp.Or(c.OpenAIBaseURL, d.OpenAIBaseURL),
		GeminiBaseURL: cmp.Or(c.GeminiBaseURL, d.GeminiBaseURL),
		OpenAIAPIKey:  c.OpenAIAPIKey,
		GeminiAPIKey:  c.GeminiAPIKey,
		OllamaHost:    c.OllamaHost,
		OllamaModel:   c.OllamaModel,
	}
}

// NewRunner builds a Runner from cfg and logger. A nil cfg uses DefaultConfig
// and a nil logger discards output. Zero-value operational fields in cfg are
// filled from DefaultConfig, and cfg is copied, so later caller mutations do
// not affect the Runner.
func NewRunner(cfg *Config, logger *slog.Logger, opts ...Option) *Runner {
	effective := DefaultConfig()
	if cfg != nil {
		effective = cfg.withDefaults()
	}
	r := &Runner{
		cfg:      effective,
		logger:   logger,
		client:   &http.Client{},
		fdReader: defaultFileLimitReader{},
	}
	if r.logger == nil {
		r.logger = slog.New(slog.DiscardHandler)
	}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}
	return r
}

// Run executes every check in a deterministic order and returns the aggregated
// Report. Checks are synchronous so output ordering is stable; callers that
// want concurrency can run independent Runners themselves.
func (r *Runner) Run(ctx context.Context) Report {
	checks := []func(context.Context) Result{
		r.checkFileDescriptors,
		r.checkOpenAILatency,
		r.checkGeminiLatency,
		r.checkOpenAIKey,
		r.checkGeminiKey,
		r.checkOllama,
	}

	var report Report
	for _, check := range checks {
		report.Results = append(report.Results, check(ctx))
	}
	return report
}
