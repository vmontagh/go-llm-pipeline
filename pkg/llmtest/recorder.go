package llmtest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"gopkg.in/dnaeon/go-vcr.v3/cassette"
	"gopkg.in/dnaeon/go-vcr.v3/recorder"
)

// ErrVCRFakeModeLiveCall indicates a live network call was attempted while VCR
// is operating in replay-only mode (cassette miss).
var ErrVCRFakeModeLiveCall = errors.New("VCR fake-mode: refusing live network call")

type failFastTransport struct{}

func (failFastTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("%w: %s %s (cassette miss?)", ErrVCRFakeModeLiveCall, req.Method, req.URL.String())
}

// DefaultLLMMatcher matches requests by Method and URL Path.
func DefaultLLMMatcher(r *http.Request, i cassette.Request) bool { //nolint:gocritic // signature required by go-vcr cassette.MatcherFunc
	if r.Method == i.Method && r.URL.String() == i.URL {
		return true
	}
	if r.Method == i.Method && r.URL.Path != "" && strings.Contains(i.URL, r.URL.Path) {
		return true
	}
	return false
}

type recorderConfig struct {
	redactHeaders     []string
	redactQueryParams []string
	prettyJSON        bool
	failFastOnMiss    bool
	customMatcher     cassette.MatcherFunc
}

// RecorderOption configures the VCR recorder.
type RecorderOption func(*recorderConfig)

func WithRedactHeaders(headers ...string) RecorderOption {
	return func(c *recorderConfig) { c.redactHeaders = append(c.redactHeaders, headers...) }
}

func WithRedactQueryParams(params ...string) RecorderOption {
	return func(c *recorderConfig) { c.redactQueryParams = append(c.redactQueryParams, params...) }
}

func WithPrettyJSON(enabled bool) RecorderOption {
	return func(c *recorderConfig) { c.prettyJSON = enabled }
}

func WithFailFastOnMiss(enabled bool) RecorderOption {
	return func(c *recorderConfig) { c.failFastOnMiss = enabled }
}

func WithCustomMatcher(fn cassette.MatcherFunc) RecorderOption {
	return func(c *recorderConfig) { c.customMatcher = fn }
}

// SetupRecorder creates a go-vcr recorder with LLM-friendly defaults.
func SetupRecorder(cassettePath string, mode recorder.Mode, opts ...RecorderOption) (*recorder.Recorder, *http.Client, error) {
	cfg := recorderConfig{
		redactHeaders:     []string{"Authorization"},
		redactQueryParams: []string{"api_key", "key"},
		prettyJSON:        true,
		failFastOnMiss:    true,
		customMatcher:     DefaultLLMMatcher,
	}

	for _, opt := range opts {
		if opt == nil {
			continue
		}
		opt(&cfg)
	}

	realTransport := http.RoundTripper(http.DefaultTransport)
	if mode == recorder.ModeReplayOnly && cfg.failFastOnMiss {
		realTransport = failFastTransport{}
	}

	rec, err := recorder.NewWithOptions(&recorder.Options{
		CassetteName:       cassettePath,
		Mode:               mode,
		RealTransport:      realTransport,
		SkipRequestLatency: true,
	})
	if err != nil {
		return nil, nil, err
	}

	if cfg.customMatcher == nil {
		cfg.customMatcher = DefaultLLMMatcher
	}
	rec.SetMatcher(cfg.customMatcher)

	rec.AddHook(func(i *cassette.Interaction) error {
		for _, h := range cfg.redactHeaders {
			delete(i.Request.Headers, h)
		}

		if len(cfg.redactQueryParams) > 0 {
			u, err := url.Parse(i.Request.URL)
			if err == nil {
				q := u.Query()
				modified := false
				for _, param := range cfg.redactQueryParams {
					if q.Has(param) {
						q.Set(param, "REDACTED")
						modified = true
					}
				}
				if modified {
					u.RawQuery = q.Encode()
					i.Request.URL = u.String()
				}
			}
		}

		if cfg.prettyJSON {
			prettyFormatBody(&i.Request.Body, i.Request.Headers, &i.Request.ContentLength)
			prettyFormatBody(&i.Response.Body, i.Response.Headers, &i.Response.ContentLength)
		}

		return nil
	}, recorder.BeforeSaveHook)

	client := &http.Client{Transport: rec}
	return rec, client, nil
}

func prettyFormatBody(body *string, headers map[string][]string, cl *int64) {
	if body == nil || *body == "" || !hasJSONContentType(headers) {
		return
	}

	prettyBody, ok := prettyJSON(*body)
	if !ok {
		return
	}
	*body = prettyBody
	setContentLength(headers, cl, len(*body))
}

func hasJSONContentType(headers map[string][]string) bool {
	contentType := headers["Content-Type"]
	return len(contentType) > 0 && strings.Contains(contentType[0], "application/json")
}

func prettyJSON(raw string) (string, bool) {
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(raw), "", "  "); err != nil {
		return "", false
	}
	return buf.String(), true
}

func setContentLength(headers map[string][]string, cl *int64, length int) {
	if len(headers["Content-Length"]) > 0 {
		headers["Content-Length"][0] = strconv.Itoa(length)
	}
	*cl = int64(length)
}
