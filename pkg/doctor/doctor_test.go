package doctor_test

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/vmontagh/go-llm-pipeline/pkg/doctor"
)

const (
	openAIBase = "http://openai.test/v1"
	geminiBase = "http://gemini.test/v1beta"
	ollamaHost = "http://ollama.test"
)

var errConnRefused = errors.New("connection refused")

type fakeTransport struct {
	fn func(*http.Request) (*http.Response, error)
}

func (f fakeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f.fn(r) }

func jsonResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func clientFunc(fn func(*http.Request) (*http.Response, error)) *http.Client {
	return &http.Client{Transport: fakeTransport{fn: fn}}
}

// healthyClient answers every endpoint with a passing response so a test can
// isolate the one check it cares about.
func healthyClient() *http.Client {
	return clientFunc(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Path, "/api/tags") {
			return jsonResp(http.StatusOK, `{"models":[{"name":"llama3:latest"}]}`), nil
		}
		return jsonResp(http.StatusOK, `{}`), nil
	})
}

type fakeFDReader struct {
	soft, hard uint64
	err        error
}

func (f fakeFDReader) ReadFileLimit() (soft, hard uint64, err error) {
	return f.soft, f.hard, f.err
}

func baseConfig() *doctor.Config {
	cfg := doctor.DefaultConfig()
	cfg.OpenAIBaseURL = openAIBase
	cfg.GeminiBaseURL = geminiBase
	cfg.OllamaHost = ollamaHost
	return &cfg
}

func resultByName(t *testing.T, report doctor.Report, name string) doctor.Result {
	t.Helper()
	for _, res := range report.Results {
		if res.Name == name {
			return res
		}
	}
	t.Fatalf("no result named %q in report", name)
	return doctor.Result{}
}

func TestRunner_FileDescriptors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		reader fakeFDReader
		want   doctor.Status
	}{
		{name: "below_min_fails", reader: fakeFDReader{soft: 256, hard: 512}, want: doctor.StatusFail},
		{name: "below_warn_warns", reader: fakeFDReader{soft: 2048, hard: 4096}, want: doctor.StatusWarn},
		{name: "healthy_passes", reader: fakeFDReader{soft: 65536, hard: 65536}, want: doctor.StatusPass},
		{name: "unsupported_skips", reader: fakeFDReader{err: doctor.ErrFileLimitUnsupported}, want: doctor.StatusSkip},
		{name: "unexpected_error_warns", reader: fakeFDReader{err: errConnRefused}, want: doctor.StatusWarn},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := doctor.NewRunner(baseConfig(), nil,
				doctor.WithHTTPClient(healthyClient()),
				doctor.WithFileLimitReader(tt.reader),
			)
			got := resultByName(t, r.Run(t.Context()), "file descriptors")
			if diff := cmp.Diff(tt.want, got.Status); diff != "" {
				t.Errorf("file descriptors status mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRunner_Latency(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		configureKey bool
		transportErr bool
		want         doctor.Status
	}{
		{name: "reachable_passes", want: doctor.StatusPass},
		{name: "unreachable_without_key_warns", transportErr: true, want: doctor.StatusWarn},
		{name: "unreachable_with_key_fails", transportErr: true, configureKey: true, want: doctor.StatusFail},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := baseConfig()
			if tt.configureKey {
				cfg.OpenAIAPIKey = "sk-test"
			}
			client := clientFunc(func(req *http.Request) (*http.Response, error) {
				if strings.HasPrefix(req.URL.Host, "openai") && tt.transportErr {
					return nil, errConnRefused
				}
				if strings.Contains(req.URL.Path, "/api/tags") {
					return jsonResp(http.StatusOK, `{"models":[{"name":"llama3"}]}`), nil
				}
				return jsonResp(http.StatusUnauthorized, `{}`), nil
			})
			r := doctor.NewRunner(cfg, nil,
				doctor.WithHTTPClient(client),
				doctor.WithFileLimitReader(fakeFDReader{soft: 65536, hard: 65536}),
			)
			got := resultByName(t, r.Run(t.Context()), "openai latency")
			if diff := cmp.Diff(tt.want, got.Status); diff != "" {
				t.Errorf("openai latency status mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRunner_APIKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		key        string
		respStatus int
		want       doctor.Status
	}{
		{name: "missing_skips", key: "", want: doctor.StatusSkip},
		{name: "valid_passes", key: "sk-test", respStatus: http.StatusOK, want: doctor.StatusPass},
		{name: "invalid_fails", key: "sk-bad", respStatus: http.StatusUnauthorized, want: doctor.StatusFail},
		{name: "rate_limited_warns", key: "sk-test", respStatus: http.StatusTooManyRequests, want: doctor.StatusWarn},
		{name: "server_error_warns", key: "sk-test", respStatus: http.StatusBadGateway, want: doctor.StatusWarn},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := baseConfig()
			cfg.OpenAIAPIKey = tt.key
			client := clientFunc(func(req *http.Request) (*http.Response, error) {
				if strings.Contains(req.URL.Path, "/api/tags") {
					return jsonResp(http.StatusOK, `{"models":[{"name":"llama3"}]}`), nil
				}
				if req.Header.Get("Authorization") != "" {
					return jsonResp(tt.respStatus, `{}`), nil
				}
				return jsonResp(http.StatusUnauthorized, `{}`), nil
			})
			r := doctor.NewRunner(cfg, nil,
				doctor.WithHTTPClient(client),
				doctor.WithFileLimitReader(fakeFDReader{soft: 65536, hard: 65536}),
			)
			got := resultByName(t, r.Run(t.Context()), "openai api key")
			if diff := cmp.Diff(tt.want, got.Status); diff != "" {
				t.Errorf("openai api key status mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRunner_GeminiKeyNotLeaked(t *testing.T) {
	t.Parallel()
	cfg := baseConfig()
	cfg.GeminiAPIKey = "AIzaSECRET"
	var seenKey bool
	client := clientFunc(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("x-goog-api-key") != "" {
			seenKey = true
		}
		if strings.Contains(req.URL.Path, "/api/tags") {
			return jsonResp(http.StatusOK, `{"models":[{"name":"llama3"}]}`), nil
		}
		return jsonResp(http.StatusOK, `{}`), nil
	})
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	r := doctor.NewRunner(cfg, logger,
		doctor.WithHTTPClient(client),
		doctor.WithFileLimitReader(fakeFDReader{soft: 65536, hard: 65536}),
	)
	report := r.Run(t.Context())
	report.Log(t.Context(), logger)

	if !seenKey {
		t.Fatal("gemini key check did not send the key in the x-goog-api-key header")
	}
	got := resultByName(t, report, "gemini api key")
	if diff := cmp.Diff(doctor.StatusPass, got.Status); diff != "" {
		t.Errorf("gemini api key status mismatch (-want +got):\n%s", diff)
	}
	if strings.Contains(buf.String(), "AIzaSECRET") {
		t.Errorf("log output leaked the Gemini API key:\n%s", buf.String())
	}
}

// TestRunner_GeminiKeyNotLeakedOnNetworkError guards the highest-risk leak
// path: when the transport itself errors, the key must not surface in the
// error string or the logged report.
func TestRunner_GeminiKeyNotLeakedOnNetworkError(t *testing.T) {
	t.Parallel()
	cfg := baseConfig()
	cfg.GeminiAPIKey = "AIzaSECRET"
	client := clientFunc(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Path, "/api/tags") {
			return jsonResp(http.StatusOK, `{"models":[{"name":"llama3"}]}`), nil
		}
		// Mimic net/http wrapping the request URL in a *url.Error.
		return nil, &url.Error{Op: "Get", URL: req.URL.String(), Err: errConnRefused}
	})
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	r := doctor.NewRunner(cfg, logger,
		doctor.WithHTTPClient(client),
		doctor.WithFileLimitReader(fakeFDReader{soft: 65536, hard: 65536}),
	)
	report := r.Run(t.Context())
	report.Log(t.Context(), logger)

	got := resultByName(t, report, "gemini api key")
	if diff := cmp.Diff(doctor.StatusWarn, got.Status); diff != "" {
		t.Errorf("gemini api key status mismatch (-want +got):\n%s", diff)
	}
	if strings.Contains(buf.String(), "AIzaSECRET") {
		t.Errorf("log output leaked the Gemini API key on network error:\n%s", buf.String())
	}
}

func TestRunner_Ollama(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		model   string
		tagsErr bool
		status  int
		body    string
		want    doctor.Status
	}{
		{name: "unreachable_warns", tagsErr: true, want: doctor.StatusWarn},
		{name: "non_200_warns", status: http.StatusServiceUnavailable, body: `{}`, want: doctor.StatusWarn},
		{name: "malformed_fails", status: http.StatusOK, body: `{not-json`, want: doctor.StatusFail},
		{name: "no_models_warns", status: http.StatusOK, body: `{"models":[]}`, want: doctor.StatusWarn},
		{name: "models_loaded_passes", status: http.StatusOK, body: `{"models":[{"name":"llama3:latest"}]}`, want: doctor.StatusPass},
		{name: "requested_model_missing_fails", model: "mistral", status: http.StatusOK, body: `{"models":[{"name":"llama3:latest"}]}`, want: doctor.StatusFail},
		{name: "requested_model_alias_passes", model: "llama3", status: http.StatusOK, body: `{"models":[{"name":"llama3:latest"}]}`, want: doctor.StatusPass},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := baseConfig()
			cfg.OllamaModel = tt.model
			client := clientFunc(func(req *http.Request) (*http.Response, error) {
				if strings.Contains(req.URL.Path, "/api/tags") {
					if tt.tagsErr {
						return nil, errConnRefused
					}
					return jsonResp(tt.status, tt.body), nil
				}
				return jsonResp(http.StatusOK, `{}`), nil
			})
			r := doctor.NewRunner(cfg, nil,
				doctor.WithHTTPClient(client),
				doctor.WithFileLimitReader(fakeFDReader{soft: 65536, hard: 65536}),
			)
			got := resultByName(t, r.Run(t.Context()), "ollama")
			if diff := cmp.Diff(tt.want, got.Status); diff != "" {
				t.Errorf("ollama status mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestReport_HasFailureAndSummary(t *testing.T) {
	t.Parallel()
	report := doctor.Report{Results: []doctor.Result{
		{Name: "a", Status: doctor.StatusPass},
		{Name: "b", Status: doctor.StatusWarn},
		{Name: "c", Status: doctor.StatusSkip},
		{Name: "d", Status: doctor.StatusFail},
	}}

	if !report.HasFailure() {
		t.Error("HasFailure() = false, want true")
	}
	want := doctor.Summary{Pass: 1, Warn: 1, Fail: 1, Skip: 1}
	if diff := cmp.Diff(want, report.Summary()); diff != "" {
		t.Errorf("Summary() mismatch (-want +got):\n%s", diff)
	}
}

func TestReport_WarningsDoNotFail(t *testing.T) {
	t.Parallel()
	report := doctor.Report{Results: []doctor.Result{
		{Name: "a", Status: doctor.StatusPass},
		{Name: "b", Status: doctor.StatusWarn},
		{Name: "c", Status: doctor.StatusSkip},
	}}
	if report.HasFailure() {
		t.Error("HasFailure() = true for a warning-only report, want false")
	}
}

func TestDefaultConfig(t *testing.T) {
	t.Parallel()
	cfg := doctor.DefaultConfig()
	if cfg.Timeout <= 0 {
		t.Errorf("DefaultConfig().Timeout = %v, want > 0", cfg.Timeout)
	}
	if cfg.FileLimitMin == 0 || cfg.FileLimitWarn <= cfg.FileLimitMin {
		t.Errorf("DefaultConfig() file limits = min %d warn %d, want warn > min > 0", cfg.FileLimitMin, cfg.FileLimitWarn)
	}
	if cfg.OpenAIBaseURL == "" || cfg.GeminiBaseURL == "" || cfg.OllamaHost == "" {
		t.Error("DefaultConfig() left a base URL empty")
	}
}

func TestRunner_LogEmitsSummary(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	r := doctor.NewRunner(baseConfig(), logger,
		doctor.WithHTTPClient(healthyClient()),
		doctor.WithFileLimitReader(fakeFDReader{soft: 65536, hard: 65536}),
	)
	report := r.Run(t.Context())
	report.Log(t.Context(), logger)
	if !strings.Contains(buf.String(), "doctor summary") {
		t.Errorf("Log output missing summary line:\n%s", buf.String())
	}
}

// TestRunner_EmptyOllamaHostSkips verifies that an empty OllamaHost is treated
// as "not configured" and skipped rather than defaulted to localhost.
func TestRunner_EmptyOllamaHostSkips(t *testing.T) {
	t.Parallel()
	cfg := baseConfig()
	cfg.OllamaHost = ""
	r := doctor.NewRunner(cfg, nil,
		doctor.WithHTTPClient(healthyClient()),
		doctor.WithFileLimitReader(fakeFDReader{soft: 65536, hard: 65536}),
	)
	got := resultByName(t, r.Run(t.Context()), "ollama")
	if diff := cmp.Diff(doctor.StatusSkip, got.Status); diff != "" {
		t.Errorf("ollama status mismatch (-want +got):\n%s", diff)
	}
}

// TestNewRunner_PartialConfigFillsDefaults checks that a caller-supplied Config
// with only a subset of fields set inherits the remaining defaults, while an
// empty OllamaHost is preserved (empty means skip, not localhost).
func TestNewRunner_PartialConfigFillsDefaults(t *testing.T) {
	t.Parallel()
	client := clientFunc(func(req *http.Request) (*http.Response, error) {
		// Any Ollama call would mean the empty host was defaulted; fail loudly.
		if strings.Contains(req.URL.Path, "/api/tags") {
			t.Errorf("ollama check ran despite empty OllamaHost; URL = %s", req.URL)
		}
		return jsonResp(http.StatusOK, `{}`), nil
	})
	r := doctor.NewRunner(&doctor.Config{OpenAIAPIKey: "sk-test"}, nil,
		doctor.WithHTTPClient(client),
		doctor.WithFileLimitReader(fakeFDReader{soft: 65536, hard: 65536}),
	)
	report := r.Run(t.Context())
	// File-descriptor thresholds came from defaults, so a healthy reader passes.
	if diff := cmp.Diff(doctor.StatusPass, resultByName(t, report, "file descriptors").Status); diff != "" {
		t.Errorf("file descriptors status mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(doctor.StatusSkip, resultByName(t, report, "ollama").Status); diff != "" {
		t.Errorf("ollama status mismatch (-want +got):\n%s", diff)
	}
}

func TestReport_SummaryCountsUnknown(t *testing.T) {
	t.Parallel()
	report := doctor.Report{Results: []doctor.Result{
		{Name: "a", Status: doctor.StatusPass},
		{Name: "b", Status: doctor.StatusUnknown},
	}}
	want := doctor.Summary{Pass: 1, Unknown: 1}
	if diff := cmp.Diff(want, report.Summary()); diff != "" {
		t.Errorf("Summary() mismatch (-want +got):\n%s", diff)
	}
}

func TestReport_String(t *testing.T) {
	t.Parallel()
	report := doctor.Report{Results: []doctor.Result{
		{Check: doctor.CheckOpenAIKey, Name: "openai api key", Status: doctor.StatusFail, Detail: "rejected", Remediation: "check the key"},
		{Check: doctor.CheckOllama, Name: "ollama", Status: doctor.StatusSkip, Detail: "not configured"},
	}}
	out := report.String()
	for _, want := range []string{"openai api key", "rejected", "check the key", "ollama", "FAIL", "SKIP"} {
		if !strings.Contains(out, want) {
			t.Errorf("String() output missing %q:\n%s", want, out)
		}
	}
}
