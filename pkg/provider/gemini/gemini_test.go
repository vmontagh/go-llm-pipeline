package gemini_test

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	provpkg "github.com/vmontagh/go-llm-pipeline/pkg/provider"
	"github.com/vmontagh/go-llm-pipeline/pkg/provider/gemini"
)

const (
	testAPIKey     = "test-key"
	testPrompt     = "prompt"
	apiVersionPath = "/v1beta"
	retryAfterSecs = 7
	clientTimeout  = time.Millisecond
)

var errTest = errors.New("test sentinel")

// capturedRequest is what the stub server saw for one Call.
type capturedRequest struct {
	Method      string
	Path        string
	APIKey      string
	ContentType string
	Body        map[string]any
}

// newStubServer serves status, header and body for every request.
func newStubServer(t *testing.T, status int, header http.Header, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		for k, vs := range header {
			for _, v := range vs {
				rw.Header().Add(k, v)
			}
		}
		rw.WriteHeader(status)
		if _, err := io.WriteString(rw, body); err != nil {
			t.Errorf("WriteString() = %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newCapturingServer records each request and replies with a valid response.
func newCapturingServer(t *testing.T) (srv *httptest.Server, captured <-chan capturedRequest) {
	t.Helper()
	requests := make(chan capturedRequest, 1)
	srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		requests <- capturedRequest{
			Method:      r.Method,
			Path:        r.URL.Path,
			APIKey:      r.Header.Get("X-Goog-Api-Key"),
			ContentType: r.Header.Get("Content-Type"),
			Body:        body,
		}
		if _, err := io.WriteString(rw, `{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}`); err != nil {
			t.Errorf("WriteString() = %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, requests
}

func call(t *testing.T, p provpkg.Provider[string], image []byte) provpkg.CallResult {
	t.Helper()
	return p.Call(t.Context(), &provpkg.Lease[string]{Value: testAPIKey}, testPrompt, image)
}

func TestCall_Request(t *testing.T) {
	t.Parallel()
	textOnlyContents := []any{map[string]any{
		"role":  "user",
		"parts": []any{map[string]any{"text": testPrompt}},
	}}
	tests := []struct {
		name     string
		opts     []gemini.ProviderOption
		image    []byte
		wantPath string
		wantBody map[string]any
	}{
		{
			name:     "text only with default model",
			wantPath: apiVersionPath + "/models/" + gemini.DefaultModel + ":generateContent",
			wantBody: map[string]any{"contents": textOnlyContents},
		},
		{
			name: "image and response schema",
			opts: []gemini.ProviderOption{
				gemini.WithModel("custom-model"),
				gemini.WithResponseSchema(map[string]any{
					"type":       "object",
					"properties": map[string]any{"heading": map[string]any{"type": "string"}},
					"required":   []any{"heading"},
				}),
			},
			image:    []byte("img"),
			wantPath: apiVersionPath + "/models/custom-model:generateContent",
			wantBody: map[string]any{
				"contents": []any{map[string]any{
					"role": "user",
					"parts": []any{
						map[string]any{"text": testPrompt},
						map[string]any{"inlineData": map[string]any{"mimeType": "image/jpeg", "data": "aW1n"}},
					},
				}},
				"generationConfig": map[string]any{
					"responseMimeType": "application/json",
					"responseSchema": map[string]any{
						"type":       "OBJECT",
						"properties": map[string]any{"heading": map[string]any{"type": "STRING"}},
						"required":   []any{"heading"},
					},
				},
			},
		},
		{
			name:     "resource-qualified model is not prefixed",
			opts:     []gemini.ProviderOption{gemini.WithModel("tunedModels/t1")},
			wantPath: apiVersionPath + "/tunedModels/t1:generateContent",
			wantBody: map[string]any{"contents": textOnlyContents},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv, captured := newCapturingServer(t)
			opts := append([]gemini.ProviderOption{gemini.WithBaseURL(srv.URL + apiVersionPath + "/")}, tt.opts...)
			p := gemini.NewProvider(nil, opts...)

			got := call(t, p, tt.image)

			if got.Err != nil {
				t.Fatalf("Call() error = %v", got.Err)
			}
			want := capturedRequest{
				Method:      http.MethodPost,
				Path:        tt.wantPath,
				APIKey:      testAPIKey,
				ContentType: "application/json",
				Body:        tt.wantBody,
			}
			if diff := cmp.Diff(want, <-captured); diff != "" {
				t.Errorf("request mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestCall_Response(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		want provpkg.CallResult
	}{
		{
			name: "text part",
			body: `{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}`,
			want: provpkg.CallResult{Outcome: provpkg.OutcomeSuccess, Text: "ok"},
		},
		{
			name: "empty text part",
			body: `{"candidates":[{"content":{"parts":[{"text":""}]}}]}`,
			want: provpkg.CallResult{Outcome: provpkg.OutcomeSuccess},
		},
		{
			name: "blocked prompt has no candidates",
			body: `{"promptFeedback":{"blockReason":"SAFETY"}}`,
			want: provpkg.CallResult{Outcome: provpkg.OutcomeTerminal, Err: provpkg.ErrEmptyCandidate},
		},
		{
			name: "candidate without parts",
			body: `{"candidates":[{"content":{"parts":[]},"finishReason":"MAX_TOKENS"}]}`,
			want: provpkg.CallResult{Outcome: provpkg.OutcomeTerminal, Err: provpkg.ErrEmptyCandidate},
		},
		{
			name: "non-text first part",
			body: `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/jpeg","data":"AA=="}}]}}]}`,
			want: provpkg.CallResult{Outcome: provpkg.OutcomeTerminal, Err: provpkg.ErrInvalidResponsePart},
		},
		{
			name: "malformed JSON",
			body: `{"candidates":`,
			want: provpkg.CallResult{Outcome: provpkg.OutcomeTerminal, Err: provpkg.ErrInvalidResponsePart},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := newStubServer(t, http.StatusOK, nil, tt.body)
			p := gemini.NewProvider(nil, gemini.WithBaseURL(srv.URL))

			got := call(t, p, nil)

			if diff := cmp.Diff(tt.want, got, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Call() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestCall_APIError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		status     int
		header     http.Header
		body       string
		wantResult provpkg.CallResult
		wantErr    gemini.APIError
	}{
		{
			name:   "server error with Google envelope",
			status: http.StatusInternalServerError,
			body:   `{"error":{"code":500,"message":"boom","status":"INTERNAL"}}`,
			wantResult: provpkg.CallResult{
				Outcome:    provpkg.OutcomeProviderError,
				StatusCode: http.StatusInternalServerError,
				Body:       []byte(`{"error":{"code":500,"message":"boom","status":"INTERNAL"}}`),
			},
			wantErr: gemini.APIError{StatusCode: http.StatusInternalServerError, Status: "INTERNAL", Message: "boom"},
		},
		{
			name:   "rate limit forwards Retry-After",
			status: http.StatusTooManyRequests,
			header: http.Header{"Retry-After": {"7"}},
			body:   `{"error":{"code":429,"message":"quota","status":"RESOURCE_EXHAUSTED"}}`,
			wantResult: provpkg.CallResult{
				Outcome:    provpkg.OutcomeProviderError,
				StatusCode: http.StatusTooManyRequests,
				Body:       []byte(`{"error":{"code":429,"message":"quota","status":"RESOURCE_EXHAUSTED"}}`),
				RetryAfter: retryAfterSecs * time.Second,
			},
			wantErr: gemini.APIError{StatusCode: http.StatusTooManyRequests, Status: "RESOURCE_EXHAUSTED", Message: "quota"},
		},
		{
			name:   "non-JSON body falls back to status text",
			status: http.StatusBadRequest,
			body:   "nope",
			wantResult: provpkg.CallResult{
				Outcome:    provpkg.OutcomeProviderError,
				StatusCode: http.StatusBadRequest,
				Body:       []byte("nope"),
			},
			wantErr: gemini.APIError{StatusCode: http.StatusBadRequest, Message: http.StatusText(http.StatusBadRequest)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := newStubServer(t, tt.status, tt.header, tt.body)
			p := gemini.NewProvider(nil, gemini.WithBaseURL(srv.URL))

			got := call(t, p, nil)

			if diff := cmp.Diff(tt.wantResult, got, cmpopts.IgnoreFields(provpkg.CallResult{}, "Err")); diff != "" {
				t.Errorf("Call() mismatch (-want +got):\n%s", diff)
			}
			var apiErr *gemini.APIError
			if !errors.As(got.Err, &apiErr) {
				t.Fatalf("Call() error = %v, want *gemini.APIError", got.Err)
			}
			if diff := cmp.Diff(tt.wantErr, *apiErr); diff != "" {
				t.Errorf("APIError mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestCall_TransportError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	p := gemini.NewProvider(nil, gemini.WithBaseURL(srv.URL))

	got := call(t, p, nil)

	want := provpkg.CallResult{Outcome: provpkg.OutcomeProviderError}
	if diff := cmp.Diff(want, got, cmpopts.IgnoreFields(provpkg.CallResult{}, "Err")); diff != "" {
		t.Errorf("Call() mismatch (-want +got):\n%s", diff)
	}
	if got.Err == nil {
		t.Error("Call() error = nil, want transport error")
	}
}

func TestWithHTTPClientTimeout(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	caller := &http.Client{}
	p := gemini.NewProvider(nil,
		gemini.WithBaseURL(srv.URL),
		gemini.WithHTTPClient(caller),
		gemini.WithHTTPClientTimeout(clientTimeout),
	)

	got := call(t, p, nil)

	var netErr net.Error
	if !errors.As(got.Err, &netErr) || !netErr.Timeout() {
		t.Errorf("Call() error = %v, want a client timeout", got.Err)
	}
	if caller.Timeout != 0 {
		t.Errorf("caller's http.Client.Timeout = %v, want 0 (unmodified)", caller.Timeout)
	}
}

func TestClassifyError(t *testing.T) {
	t.Parallel()
	p := gemini.NewProvider(nil)
	tests := []struct {
		name string
		in   provpkg.ClassifyInput
		want provpkg.ErrorTier
	}{
		{"no response", provpkg.ClassifyInput{Err: errTest}, provpkg.TierTerminal},
		{"429", provpkg.ClassifyInput{StatusCode: http.StatusTooManyRequests}, provpkg.TierLongTransientInfinite},
		{"500", provpkg.ClassifyInput{StatusCode: http.StatusInternalServerError}, provpkg.TierShortTransient},
		{"502", provpkg.ClassifyInput{StatusCode: http.StatusBadGateway}, provpkg.TierShortTransient},
		{"503", provpkg.ClassifyInput{StatusCode: http.StatusServiceUnavailable}, provpkg.TierShortTransient},
		{"504", provpkg.ClassifyInput{StatusCode: http.StatusGatewayTimeout}, provpkg.TierShortTransient},
		{"400", provpkg.ClassifyInput{StatusCode: http.StatusBadRequest}, provpkg.TierTerminal},
		{"403", provpkg.ClassifyInput{StatusCode: http.StatusForbidden}, provpkg.TierTerminal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := p.ClassifyError(tt.in, provpkg.RetryContext{})
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("ClassifyError(%+v) mismatch (-want +got):\n%s", tt.in, diff)
			}
		})
	}
}

func TestEnrichError(t *testing.T) {
	t.Parallel()
	p := gemini.NewProvider(nil)
	tests := []struct {
		name string
		tier provpkg.ErrorTier
		want error
	}{
		{"rate limit", provpkg.TierLongTransientInfinite, provpkg.ErrRateLimitExceeded},
		{"short transient", provpkg.TierShortTransient, provpkg.ErrUnexpectedStatus},
		{"terminal passthrough", provpkg.TierTerminal, errTest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := p.EnrichError(tt.tier, errTest, nil)
			if diff := cmp.Diff(tt.want, got, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("EnrichError(%v) mismatch (-want +got):\n%s", tt.tier, diff)
			}
			if !errors.Is(got, errTest) {
				t.Errorf("EnrichError(%v) = %v, must keep the raw error", tt.tier, got)
			}
		})
	}
}

func TestNewProvider_Metadata(t *testing.T) {
	t.Parallel()
	type metadata struct {
		Name, Model, ProviderLabel, TraceRegionName string
		Keys                                        int
	}
	tests := []struct {
		name string
		keys []string
		opts []gemini.ProviderOption
		want metadata
	}{
		{
			name: "defaults",
			want: metadata{Name: "gemini/" + gemini.DefaultModel, Model: gemini.DefaultModel, ProviderLabel: "gemini", TraceRegionName: "Analyze_Gemini"},
		},
		{
			name: "custom model and keys",
			keys: []string{"a", "b"},
			opts: []gemini.ProviderOption{gemini.WithModel("m")},
			want: metadata{Name: "gemini/m", Model: "m", ProviderLabel: "gemini", TraceRegionName: "Analyze_Gemini", Keys: 2},
		},
		{
			name: "empty model is ignored",
			opts: []gemini.ProviderOption{gemini.WithModel("")},
			want: metadata{Name: "gemini/" + gemini.DefaultModel, Model: gemini.DefaultModel, ProviderLabel: "gemini", TraceRegionName: "Analyze_Gemini"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := gemini.NewProvider(tt.keys, tt.opts...)
			got := metadata{p.Name(), p.Model(), p.ProviderLabel(), p.TraceRegionName(), p.Keys().Len()}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("NewProvider metadata mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
