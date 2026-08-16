// Package llmvcr provides a productized VCR (record/replay) workflow for the
// standard Go http.Client instances used by LLM provider SDKs. It records HTTP
// interactions to YAML cassettes and replays them offline for zero-cost,
// deterministic CI, failing fast on a cassette miss. Sensitive headers, query
// parameters, and body tokens are redacted (via pkg/scrub) before any cassette
// is written to disk.
//
// The zero dependency on any particular provider means the *http.Client returned
// by Recorder.HTTPClient works with any client that accepts one, including the
// OpenAI provider (openai.WithHTTPClient) and the Gemini genai.Client
// (option.WithHTTPClient).
package llmvcr

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"

	"gopkg.in/dnaeon/go-vcr.v3/cassette"
	"gopkg.in/dnaeon/go-vcr.v3/recorder"

	"github.com/vmontagh/go-llm-pipeline/pkg/scrub"
)

// ErrCassetteMiss indicates a live network call was attempted while replaying in
// ModeReplayOnly with fail-fast enabled (i.e. no recorded interaction matched
// the request). Callers can match it with errors.Is.
var ErrCassetteMiss = errors.New("llmvcr: refusing live network call (cassette miss)")

// ErrUnknownMode indicates New was called with a Mode value that is not one of
// the defined constants.
var ErrUnknownMode = errors.New("llmvcr: unknown mode")

// redactedValue is written in place of a redacted header value or query parameter.
const redactedValue = "REDACTED"

// Mode selects the record/replay behaviour of a Recorder.
type Mode int

const (
	// ModeReplayOnly replays recorded interactions and never records. When
	// fail-fast is enabled (the default) an unmatched request returns
	// ErrCassetteMiss instead of reaching the network. This is the CI default.
	ModeReplayOnly Mode = iota
	// ModeRecordOnce records interactions when the cassette does not yet exist
	// and replays from it on subsequent runs.
	ModeRecordOnce
	// ModeRecordOnly always records live interactions, overwriting any existing
	// cassette contents for matched requests.
	ModeRecordOnly
)

// vcrMode maps an llmvcr.Mode to the underlying go-vcr recorder mode.
func (m Mode) vcrMode() (recorder.Mode, error) {
	switch m {
	case ModeReplayOnly:
		return recorder.ModeReplayOnly, nil
	case ModeRecordOnce:
		return recorder.ModeRecordOnce, nil
	case ModeRecordOnly:
		return recorder.ModeRecordOnly, nil
	default:
		return 0, fmt.Errorf("%w: %d", ErrUnknownMode, int(m))
	}
}

// RecordedRequest is a minimal view of a recorded cassette request, passed to a
// MatcherFunc. It deliberately hides the underlying go-vcr types to keep the
// public API stable for semantic versioning.
type RecordedRequest struct {
	// Method is the recorded HTTP method, e.g. "POST".
	Method string
	// URL is the recorded full request URL.
	URL string
}

// MatcherFunc reports whether an incoming request matches a recorded request.
type MatcherFunc func(*http.Request, RecordedRequest) bool

// config holds resolved Recorder settings.
type config struct {
	redactHeaders     []string
	redactQueryParams []string
	scrubber          *scrub.Scrubber
	additionalRules   []scrub.Rule
	prettyJSON        bool
	failFastOnMiss    bool
	matcher           MatcherFunc
}

// Option configures a Recorder at construction time.
type Option func(*config)

// WithRedactHeaders replaces the values of the named request headers with
// "REDACTED" before the cassette is written. The names are appended to the
// defaults (Authorization, X-Api-Key, X-Goog-Api-Key).
func WithRedactHeaders(headers ...string) Option {
	return func(c *config) { c.redactHeaders = append(c.redactHeaders, headers...) }
}

// WithRedactQueryParams replaces the values of the named URL query parameters
// with "REDACTED" before the cassette is written. The names are appended to the
// defaults (key, api_key).
func WithRedactQueryParams(params ...string) Option {
	return func(c *config) { c.redactQueryParams = append(c.redactQueryParams, params...) }
}

// WithScrubber replaces the default body/header scrubber. It takes precedence
// over WithAdditionalScrubRules: when an explicit scrubber is provided, any
// additional rules are ignored. To extend rather than replace the defaults,
// use WithAdditionalScrubRules — the default scrubber's rules cannot be read
// back out of a *scrub.Scrubber.
func WithScrubber(s *scrub.Scrubber) Option {
	return func(c *config) {
		if s != nil {
			c.scrubber = s
		}
	}
}

// WithAdditionalScrubRules appends rules to the default scrubber. Multiple calls
// accumulate. It has no effect when WithScrubber provides an explicit scrubber,
// which takes precedence.
func WithAdditionalScrubRules(rules ...scrub.Rule) Option {
	return func(c *config) {
		c.additionalRules = append(c.additionalRules, rules...)
	}
}

// WithPrettyJSON toggles pretty-printing of JSON request/response bodies in the
// written cassette. Defaults to true.
func WithPrettyJSON(enabled bool) Option {
	return func(c *config) { c.prettyJSON = enabled }
}

// WithFailFastOnMiss toggles whether a cassette miss in ModeReplayOnly returns
// ErrCassetteMiss instead of performing a live call. Defaults to true.
func WithFailFastOnMiss(enabled bool) Option {
	return func(c *config) { c.failFastOnMiss = enabled }
}

// WithMatcher overrides the request matcher used during replay.
func WithMatcher(fn MatcherFunc) Option {
	return func(c *config) {
		if fn != nil {
			c.matcher = fn
		}
	}
}

// failFastTransport wraps the recorder transport and translates go-vcr's
// "interaction not found" replay miss into ErrCassetteMiss so callers can match
// it with errors.Is.
type failFastTransport struct {
	inner http.RoundTripper
}

func (t failFastTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.inner.RoundTrip(req)
	if err != nil && errors.Is(err, cassette.ErrInteractionNotFound) {
		miss := fmt.Errorf("%w: %s %s", ErrCassetteMiss, req.Method, safeURL(req.URL))
		// http.Client.Do rewraps this in a *url.Error built from req.URL, which
		// still carries the raw (possibly secret) query string. The request is
		// terminal on a fail-fast miss, so strip the sensitive parts in place to
		// stop that wrapper leaking credentials.
		stripSensitiveURL(req.URL)
		return nil, miss
	}
	return resp, err
}

// liveFallbackTransport wraps the recorder transport and, on a replay miss,
// performs a live call via a real transport. go-vcr's ModeReplayOnly never
// reaches its configured RealTransport on a miss (it returns
// cassette.ErrInteractionNotFound directly), so this restores the expected
// "attempt a live call when fail-fast is disabled" behaviour.
type liveFallbackTransport struct {
	inner http.RoundTripper
	live  http.RoundTripper
}

func (t liveFallbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.inner.RoundTrip(req)
	if err != nil && errors.Is(err, cassette.ErrInteractionNotFound) {
		return t.live.RoundTrip(req)
	}
	return resp, err
}

// safeURL renders a request URL for error messages without leaking sensitive
// query parameters or userinfo: only scheme://host/path is kept.
func safeURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	sanitized := *u
	stripSensitiveURL(&sanitized)
	return sanitized.String()
}

// stripSensitiveURL clears the parts of a URL that may carry credentials (query,
// fragment, userinfo) in place.
func stripSensitiveURL(u *url.URL) {
	if u == nil {
		return
	}
	u.RawQuery = ""
	u.Fragment = ""
	u.User = nil
}

// Recorder records and replays HTTP interactions through an *http.Client.
type Recorder struct {
	rec       *recorder.Recorder
	transport http.RoundTripper
}

// New creates a Recorder for the given cassette path and mode. The cassette path
// is the file path without the ".yaml" extension, matching go-vcr semantics.
func New(cassettePath string, mode Mode, opts ...Option) (*Recorder, error) {
	vmode, err := mode.vcrMode()
	if err != nil {
		return nil, err
	}

	cfg := config{
		redactHeaders:     []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key"},
		redactQueryParams: []string{"key", "api_key"},
		prettyJSON:        true,
		failFastOnMiss:    true,
		matcher:           DefaultMatcher,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	// Resolve the scrubber once: an explicit WithScrubber takes precedence,
	// otherwise build from the defaults plus any accumulated additional rules.
	if cfg.scrubber == nil {
		cfg.scrubber = scrub.NewScrubber(append(defaultScrubRules(), cfg.additionalRules...)...)
	}

	rec, err := recorder.NewWithOptions(&recorder.Options{
		CassetteName:       cassettePath,
		Mode:               vmode,
		RealTransport:      http.DefaultTransport,
		SkipRequestLatency: true,
	})
	if err != nil {
		return nil, fmt.Errorf("creating recorder: %w", err)
	}

	matcher := cfg.matcher
	rec.SetMatcher(func(r *http.Request, i cassette.Request) bool {
		return matcher(r, RecordedRequest{Method: i.Method, URL: i.URL})
	})

	rec.AddHook(cfg.beforeSaveHook(), recorder.BeforeSaveHook)

	var transport http.RoundTripper = rec
	if vmode == recorder.ModeReplayOnly {
		if cfg.failFastOnMiss {
			transport = failFastTransport{inner: rec}
		} else {
			transport = liveFallbackTransport{inner: rec, live: http.DefaultTransport}
		}
	}

	return &Recorder{rec: rec, transport: transport}, nil
}

// HTTPClient returns an *http.Client whose transport records or replays through
// the cassette. Pass it to any client that accepts a custom *http.Client.
func (r *Recorder) HTTPClient() *http.Client {
	return &http.Client{Transport: r.transport}
}

// Stop flushes any recorded interactions to the cassette and releases resources.
// It must be called (typically via defer or t.Cleanup) for recording to persist.
func (r *Recorder) Stop() error {
	if err := r.rec.Stop(); err != nil {
		return fmt.Errorf("stopping recorder: %w", err)
	}
	return nil
}

// DefaultMatcher matches requests by HTTP method, URL path and query,
// tolerating a changed host/port (e.g. an ephemeral httptest server) on
// replay. A query parameter redacted on save (recorded value "REDACTED")
// acts as a wildcard, so a scrubbed secret still matches while non-secret
// parameters must match exactly.
func DefaultMatcher(r *http.Request, i RecordedRequest) bool {
	if r.Method != i.Method {
		return false
	}
	recorded, err := url.Parse(i.URL)
	if err != nil {
		return false
	}
	if recorded.Path != r.URL.Path {
		return false
	}
	return queryMatches(recorded.Query(), r.URL.Query())
}

// queryMatches reports whether the live query matches the recorded one. A
// recorded parameter whose sole value is redactedValue matches any live
// value, since the real secret was scrubbed before the cassette was written.
func queryMatches(recorded, live url.Values) bool {
	if len(recorded) != len(live) {
		return false
	}
	for key, recVals := range recorded {
		liveVals, ok := live[key]
		if !ok {
			return false
		}
		if len(recVals) == 1 && recVals[0] == redactedValue {
			continue
		}
		if !slices.Equal(recVals, liveVals) {
			return false
		}
	}
	return true
}
