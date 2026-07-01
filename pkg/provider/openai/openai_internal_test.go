package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	provpkg "github.com/vmontagh/go-llm-pipeline/pkg/provider"
)

var errTest = errors.New("test sentinel")

func TestTransport_ClassifyError(t *testing.T) {
	t.Parallel()
	tr := &transport{model: DefaultModel, logger: slog.New(slog.DiscardHandler)}
	mustBody := func(t *testing.T, v any) []byte {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		return b
	}
	tests := []struct {
		name       string
		err        error
		statusCode int
		body       []byte
		wantTier   provpkg.ErrorTier
	}{
		{name: "nil err nil resp → TierTerminal", wantTier: provpkg.TierTerminal},
		{
			name:     "network error → TierShortTransient",
			err:      errTest,
			wantTier: provpkg.TierShortTransient,
		},
		{
			name:       "429 rate_limit_exceeded → TierShortRateLimit",
			statusCode: http.StatusTooManyRequests,
			body:       mustBody(t, map[string]any{"error": map[string]any{"message": "Rate limit exceeded", "type": "server_error", "code": "rate_limit_exceeded"}}),
			wantTier:   provpkg.TierShortRateLimit,
		},
		{
			name:       "429 insufficient_quota → TierLongTransientBounded",
			statusCode: http.StatusTooManyRequests,
			body:       mustBody(t, map[string]any{"error": map[string]any{"message": "You exceeded your current quota", "type": "insufficient_quota", "code": "insufficient_quota"}}),
			wantTier:   provpkg.TierLongTransientBounded,
		},
		{
			name:       "429 malformed body → TierShortRateLimit",
			statusCode: http.StatusTooManyRequests,
			body:       []byte(`{"error":`),
			wantTier:   provpkg.TierShortRateLimit,
		},
		{
			name:       "429 unknown error type → TierShortRateLimit",
			statusCode: http.StatusTooManyRequests,
			body:       mustBody(t, map[string]any{"error": map[string]any{"message": "burst", "type": "server_error", "code": "other"}}),
			wantTier:   provpkg.TierShortRateLimit,
		},
		{name: "500 → TierShortTransient", statusCode: http.StatusInternalServerError, wantTier: provpkg.TierShortTransient},
		{name: "503 → TierShortTransient", statusCode: http.StatusServiceUnavailable, wantTier: provpkg.TierShortTransient},
		{name: "404 → TierTerminal", statusCode: http.StatusNotFound, wantTier: provpkg.TierTerminal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tr.ClassifyError(provpkg.ClassifyInput{Err: tt.err, StatusCode: tt.statusCode, Body: tt.body}, provpkg.RetryContext{})
			if diff := cmp.Diff(tt.wantTier, got); diff != "" {
				t.Errorf("ClassifyError tier mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestTransport_EnrichError(t *testing.T) {
	t.Parallel()
	tr := &transport{}
	raw := errTest
	tests := []struct {
		name      string
		tier      provpkg.ErrorTier
		wantError error
	}{
		{
			name:      "LongTransientBounded wraps ErrQuotaExceeded",
			tier:      provpkg.TierLongTransientBounded,
			wantError: provpkg.ErrQuotaExceeded,
		},
		{
			name:      "ShortRateLimit wraps ErrRateLimitExceeded",
			tier:      provpkg.TierShortRateLimit,
			wantError: provpkg.ErrRateLimitExceeded,
		},
		{
			name:      "ShortTransient is passthrough",
			tier:      provpkg.TierShortTransient,
			wantError: raw,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tr.EnrichError(tt.tier, raw, nil)
			if diff := cmp.Diff(tt.wantError, got, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("EnrichError(%v) mismatch (-want +got):\n%s", tt.tier, diff)
			}
		})
	}
}

func TestProvider_HTTPClient_DefaultAllocated(t *testing.T) {
	t.Parallel()
	p := NewProvider(nil, WithHTTPClientTimeout(3*time.Second))
	tr, ok := p.(*transport)
	if !ok {
		t.Fatalf("provider is not *transport: %T", p)
	}
	if tr.client == nil || tr.client.Timeout != 3*time.Second {
		t.Errorf("default client timeout: got %v, want 3s", tr.client.Timeout)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type errReadCloser struct{}

func (errReadCloser) Read([]byte) (int, error) { return 0, errTest }
func (errReadCloser) Close() error             { return nil }

func TestTransport_Call(t *testing.T) {
	t.Parallel()
	type testCase struct {
		name     string
		client   *http.Client
		wantOut  provpkg.CallOutcome
		wantCode int
		wantText string
		wantErr  error
	}

	assertResult := func(t *testing.T, tc testCase, got provpkg.CallResult) {
		t.Helper()
		if diff := cmp.Diff(tc.wantOut, got.Outcome); diff != "" {
			t.Fatalf("Outcome mismatch (-want +got):\n%s", diff)
		}
		if tc.wantCode != 0 {
			if diff := cmp.Diff(tc.wantCode, got.StatusCode); diff != "" {
				t.Fatalf("StatusCode mismatch (-want +got):\n%s", diff)
			}
		}
		if tc.wantText != "" {
			if diff := cmp.Diff(tc.wantText, got.Text); diff != "" {
				t.Fatalf("Text mismatch (-want +got):\n%s", diff)
			}
		}
		if tc.wantErr != nil && !errors.Is(got.Err, tc.wantErr) {
			t.Fatalf("error = %v, want errors.Is(_, %v)", got.Err, tc.wantErr)
		}
	}

	tests := []testCase{
		{
			name: "network error",
			client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errTest
			})},
			wantOut: provpkg.OutcomeProviderError,
		},
		{
			name: "body read error keeps status code",
			client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusInternalServerError, Body: errReadCloser{}}, nil
			})},
			wantOut:  provpkg.OutcomeProviderError,
			wantCode: http.StatusInternalServerError,
		},
		{
			name: "successful response returns text",
			client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				body := `{"choices":[{"message":{"content":"ok"}}]}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(body)),
					Header:     make(http.Header),
				}, nil
			})},
			wantOut:  provpkg.OutcomeSuccess,
			wantText: "ok",
		},
		{
			name: "429 returns provider error with status",
			client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				body := `{"error":{"message":"rate","type":"server_error","code":"rate_limit_exceeded"}}`
				h := make(http.Header)
				h.Set("Retry-After", "10")
				return &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Body:       io.NopCloser(strings.NewReader(body)),
					Header:     h,
				}, nil
			})},
			wantOut:  provpkg.OutcomeProviderError,
			wantCode: http.StatusTooManyRequests,
			wantErr:  provpkg.ErrUnexpectedStatus,
		},
		{
			name: "non-200 non-retryable is terminal",
			client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Body:       io.NopCloser(strings.NewReader(`bad`)),
					Header:     make(http.Header),
				}, nil
			})},
			wantOut: provpkg.OutcomeTerminal,
			wantErr: provpkg.ErrUnexpectedStatus,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tr := &transport{
				model:   DefaultModel,
				baseURL: "http://example.test",
				client:  tt.client,
				logger:  slog.New(slog.DiscardHandler),
				tracer:  noopTracer{},
			}
			got := tr.Call(context.Background(), &provpkg.Lease[string]{Value: "k"}, "prompt", []byte("img"))
			assertResult(t, tt, got)
		})
	}
}

func TestTransport_MetadataAndOptions(t *testing.T) {
	t.Parallel()
	p, ok := NewProvider(
		[]string{"k1"},
		WithModel("gpt-4o-mini"),
		WithBaseURL("http://example.test"),
		WithOrganization("org1"),
	).(*transport)
	if !ok {
		t.Fatal("NewProvider did not return *transport")
	}
	if diff := cmp.Diff("openai/gpt-4o-mini", p.Name()); diff != "" {
		t.Fatalf("Name mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff("gpt-4o-mini", p.Model()); diff != "" {
		t.Fatalf("Model mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(providerLabel, p.ProviderLabel()); diff != "" {
		t.Fatalf("ProviderLabel mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(traceRegionName, p.TraceRegionName()); diff != "" {
		t.Fatalf("TraceRegionName mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(1, p.Keys().Len()); diff != "" {
		t.Fatalf("Keys length mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff("http://example.test", p.baseURL); diff != "" {
		t.Fatalf("baseURL mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff("org1", p.organization); diff != "" {
		t.Fatalf("organization mismatch (-want +got):\n%s", diff)
	}
}

func TestBuildRequest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		imageData []byte
		wantParts int
	}{
		{name: "text only", wantParts: 1},
		{name: "text and image", imageData: []byte("img"), wantParts: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := buildRequest("m", "p", tt.imageData, "", nil)
			if diff := cmp.Diff(tt.wantParts, len(req.Messages[0].Content)); diff != "" {
				t.Fatalf("content parts mismatch (-want +got):\n%s", diff)
			}
			if req.ResponseFormat != nil {
				t.Errorf("ResponseFormat should be nil when no schema is set, got %+v", req.ResponseFormat)
			}
		})
	}
}

func TestBuildRequest_ResponseSchema(t *testing.T) {
	t.Parallel()
	schema := map[string]any{"type": "object"}
	tests := []struct {
		name           string
		schemaName     string
		schema         any
		wantNil        bool
		wantFormat     *ResponseFormatDef
		wantWireSubstr string
	}{
		{
			name:    "nil schema omits response_format",
			schema:  nil,
			wantNil: true,
		},
		{
			name:       "schema with name emits strict json_schema block",
			schemaName: "analysis_result",
			schema:     schema,
			wantFormat: &ResponseFormatDef{
				Type: "json_schema",
				JSONSchema: &JSONSchemaDef{
					Name:   "analysis_result",
					Strict: true,
					Schema: schema,
				},
			},
			wantWireSubstr: `"response_format":{"type":"json_schema"`,
		},
		{
			name:       "different schema name is propagated verbatim",
			schemaName: "judge_response",
			schema:     map[string]any{"type": "object", "properties": map[string]any{"approved": map[string]any{"type": "boolean"}}},
			wantFormat: &ResponseFormatDef{
				Type: "json_schema",
				JSONSchema: &JSONSchemaDef{
					Name:   "judge_response",
					Strict: true,
					Schema: map[string]any{"type": "object", "properties": map[string]any{"approved": map[string]any{"type": "boolean"}}},
				},
			},
			wantWireSubstr: `"name":"judge_response"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := buildRequest("m", "p", nil, tt.schemaName, tt.schema)
			if tt.wantNil {
				if req.ResponseFormat != nil {
					t.Errorf("ResponseFormat = %+v, want nil", req.ResponseFormat)
				}
				b, err := json.Marshal(req)
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				if strings.Contains(string(b), "response_format") {
					t.Errorf("response_format unexpectedly present in wire body: %s", string(b))
				}
				return
			}
			if diff := cmp.Diff(tt.wantFormat, req.ResponseFormat); diff != "" {
				t.Errorf("ResponseFormat mismatch (-want +got):\n%s", diff)
			}
			b, err := json.Marshal(req)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if !strings.Contains(string(b), tt.wantWireSubstr) {
				t.Errorf("wire body missing %q: %s", tt.wantWireSubstr, string(b))
			}
		})
	}
}

type noopRegion struct{}

func (noopRegion) End() {}

type noopTracer struct{}

func (noopTracer) StartRegion(context.Context, string) interface{ End() } { return noopRegion{} }
