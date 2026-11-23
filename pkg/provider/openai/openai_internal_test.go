package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"testing/iotest"
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

func TestTransport_CallTools(t *testing.T) {
	t.Parallel()
	tools := []provpkg.ToolSpec{{Name: "add", Description: "adds", Parameters: map[string]any{"type": "object"}}}
	tests := []struct {
		name string
		body string
		want provpkg.CallResult
	}{
		{
			name: "tool calls",
			body: `{"choices":[{"message":{"content":"checking","tool_calls":[{"id":"c1","type":"function","function":{"name":"add","arguments":"{\"A\":1}"}}]}}]}`,
			want: provpkg.CallResult{Outcome: provpkg.OutcomeSuccess, Text: "checking", ToolCalls: []provpkg.ToolCall{{ID: "c1", Name: "add", Arguments: `{"A":1}`}}},
		},
		{
			name: "final reply",
			body: `{"choices":[{"message":{"content":"done"}}]}`,
			want: provpkg.CallResult{Outcome: provpkg.OutcomeSuccess, Text: "done"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var sent ChatGPTRequest
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
					return nil, err
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(tt.body)), Header: make(http.Header)}, nil
			})}
			tr := &transport{model: DefaultModel, baseURL: "http://example.test", client: client, logger: slog.New(slog.DiscardHandler), tracer: noopTracer{}}
			msgs := []provpkg.Message{provpkg.TextMessage(provpkg.RoleUser, "u")}

			got := tr.CallTools(t.Context(), &provpkg.Lease[string]{Value: "k"}, msgs, tools)

			if diff := cmp.Diff(tt.want, got, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("CallTools() mismatch (-want +got):\n%s", diff)
			}
			wantTools := []Tool{{Type: "function", Function: FunctionDef{Name: "add", Description: "adds", Parameters: map[string]any{"type": "object"}}}}
			if diff := cmp.Diff(wantTools, sent.Tools); diff != "" {
				t.Errorf("sent tools mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestTransport_Stream(t *testing.T) {
	t.Parallel()
	const (
		role  = "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}],\"usage\":null}\n\n"
		hel   = "data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}],\"usage\":null}\n\n"
		lo    = "data: {\"choices\":[{\"delta\":{\"content\":\"lo\"},\"finish_reason\":\"stop\"}],\"usage\":null}\n\n"
		usage = "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\n"
		done  = "data: [DONE]\n\n"
	)
	opened := provpkg.CallResult{Outcome: provpkg.OutcomeSuccess}
	tests := []struct {
		name    string
		status  int
		body    io.Reader
		onFirst readAction // what the caller does after the first chunk
		want    []provpkg.Chunk
		wantRes provpkg.CallResult
		wantErr error
	}{
		{
			name:    "streams chunks and usage",
			status:  http.StatusOK,
			body:    strings.NewReader(role + hel + lo + usage + done),
			want:    []provpkg.Chunk{{Text: "Hel"}, {Text: "lo"}, {Usage: &provpkg.Usage{Input: 3, Output: 2}}},
			wantRes: opened,
		},
		{
			name:    "caller stops early",
			status:  http.StatusOK,
			body:    strings.NewReader(hel + lo + usage + done),
			onFirst: stopReading,
			want:    []provpkg.Chunk{{Text: "Hel"}},
			wantRes: opened,
		},
		{
			name:    "caller cancels",
			status:  http.StatusOK,
			body:    strings.NewReader(hel + lo + usage + done),
			onFirst: cancelContext,
			want:    []provpkg.Chunk{{Text: "Hel"}},
			wantRes: opened,
			wantErr: context.Canceled,
		},
		{
			name:    "caller cancels at end of stream",
			status:  http.StatusOK,
			body:    strings.NewReader(hel),
			onFirst: cancelContext,
			want:    []provpkg.Chunk{{Text: "Hel"}},
			wantRes: opened,
			wantErr: context.Canceled,
		},
		{
			name:    "ends without done",
			status:  http.StatusOK,
			body:    strings.NewReader(hel + lo + usage),
			want:    []provpkg.Chunk{{Text: "Hel"}, {Text: "lo"}, {Usage: &provpkg.Usage{Input: 3, Output: 2}}},
			wantRes: opened,
			wantErr: provpkg.ErrIncompleteStream,
		},
		{
			name:    "connection drops",
			status:  http.StatusOK,
			body:    io.MultiReader(strings.NewReader(hel), iotest.ErrReader(errTest)),
			want:    []provpkg.Chunk{{Text: "Hel"}},
			wantRes: opened,
			wantErr: provpkg.ErrIncompleteStream,
		},
		{
			name:    "fails mid-stream",
			status:  http.StatusOK,
			body:    strings.NewReader(hel + "data: {\"error\":{\"message\":\"overloaded\"}}\n\n" + done),
			want:    []provpkg.Chunk{{Text: "Hel"}},
			wantRes: opened,
			wantErr: provpkg.ErrIncompleteStream,
		},
		{
			name:    "bad event",
			status:  http.StatusOK,
			body:    strings.NewReader("data: nope\n\n"),
			wantRes: opened,
			wantErr: provpkg.ErrInvalidResponsePart,
		},
		{
			name:    "fails to open",
			status:  http.StatusServiceUnavailable,
			body:    strings.NewReader("busy"),
			wantRes: provpkg.CallResult{Outcome: provpkg.OutcomeProviderError, Err: provpkg.ErrUnexpectedStatus, StatusCode: http.StatusServiceUnavailable, Body: []byte("busy")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tr, body := newStreamTransport(t, tt.status, tt.body)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			seq, res := tr.Stream(ctx, &provpkg.Lease[string]{Value: "k"}, []provpkg.Message{provpkg.TextMessage(provpkg.RoleUser, "hi")})
			got, err := readStream(seq, func() bool {
				if tt.onFirst == cancelContext {
					cancel()
				}
				return tt.onFirst == stopReading
			})

			if diff := cmp.Diff(tt.wantRes, res, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Stream() result mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Stream() chunks mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Stream() error mismatch (-want +got):\n%s", diff)
			}
			if !body.closed {
				t.Error("response body not closed")
			}
		})
	}
}

// readAction is what a test caller does after the first chunk.
type readAction int

const (
	keepReading readAction = iota
	stopReading
	cancelContext
)

// newStreamTransport returns a transport that answers a streaming request
// for usage with status and body, and the body so a test can check that it
// was closed. Any other request fails the test.
func newStreamTransport(t *testing.T, status int, body io.Reader) (*transport, *closeTracker) {
	t.Helper()
	tracked := &closeTracker{Reader: body}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var req ChatGPTRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		if !req.Stream || req.StreamOptions == nil || !req.StreamOptions.IncludeUsage {
			t.Errorf("request stream=%t stream_options=%+v, want stream with usage", req.Stream, req.StreamOptions)
		}
		return &http.Response{StatusCode: status, Body: tracked, Header: make(http.Header)}, nil
	})}
	tr := &transport{model: DefaultModel, baseURL: "http://example.test", client: client, logger: slog.New(slog.DiscardHandler), tracer: noopTracer{}}
	return tr, tracked
}

// readStream returns the chunks of seq up to its first error, and that error.
// It calls afterFirst after the first chunk and stops if it returns true. A
// nil seq has no chunks.
func readStream(seq iter.Seq2[provpkg.Chunk, error], afterFirst func() (stop bool)) ([]provpkg.Chunk, error) {
	if seq == nil {
		return nil, nil
	}
	var got []provpkg.Chunk
	for c, err := range seq {
		if err != nil {
			return got, err
		}
		got = append(got, c)
		if len(got) == 1 && afterFirst() {
			break
		}
	}
	return got, nil
}

// closeTracker records whether the response body was closed.
type closeTracker struct {
	io.Reader
	closed bool
}

func (c *closeTracker) Close() error {
	c.closed = true
	return nil
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
	imageURL := &ImageURL{URL: "data:image/jpeg;base64,aW1n"}
	tests := []struct {
		name string
		msgs []provpkg.Message
		want []RequestMessage
	}{
		{
			name: "prompt only",
			msgs: []provpkg.Message{promptMessage("p", nil)},
			want: []RequestMessage{{Role: "user", Content: []Content{{Type: "text", Text: "p"}}}},
		},
		{
			name: "prompt and image",
			msgs: []provpkg.Message{promptMessage("p", []byte("img"))},
			want: []RequestMessage{{Role: "user", Content: []Content{{Type: "text", Text: "p"}, {Type: "image_url", ImageURL: imageURL}}}},
		},
		{
			name: "multi-turn roles",
			msgs: []provpkg.Message{
				provpkg.TextMessage(provpkg.RoleSystem, "s"),
				provpkg.TextMessage(provpkg.RoleUser, "u"),
				provpkg.TextMessage(provpkg.RoleAssistant, "a"),
			},
			want: []RequestMessage{
				{Role: "system", Content: []Content{{Type: "text", Text: "s"}}},
				{Role: "user", Content: []Content{{Type: "text", Text: "u"}}},
				{Role: "assistant", Content: []Content{{Type: "text", Text: "a"}}},
			},
		}, {
			name: "tool round-trip",
			msgs: []provpkg.Message{
				provpkg.TextMessage(provpkg.RoleUser, "u"),
				{Role: provpkg.RoleAssistant, Parts: []provpkg.Part{
					{Text: "checking"},
					{ToolCall: &provpkg.ToolCall{ID: "c1", Name: "add", Arguments: `{"A":1}`}},
					{ToolCall: &provpkg.ToolCall{ID: "c2", Name: "now"}},
				}},
				{Role: provpkg.RoleTool, Parts: []provpkg.Part{
					{ToolResult: &provpkg.ToolResult{CallID: "c1", Name: "add", Content: "1"}},
					{ToolResult: &provpkg.ToolResult{CallID: "c2", Name: "now", Content: "noon"}},
				}},
			},
			want: []RequestMessage{
				{Role: "user", Content: []Content{{Type: "text", Text: "u"}}},
				{Role: "assistant", Content: []Content{{Type: "text", Text: "checking"}}, ToolCalls: []ToolCall{
					{ID: "c1", Type: "function", Function: FunctionCall{Name: "add", Arguments: `{"A":1}`}},
					{ID: "c2", Type: "function", Function: FunctionCall{Name: "now", Arguments: "{}"}},
				}},
				{Role: "tool", Content: []Content{{Type: "text", Text: "1"}}, ToolCallID: "c1"},
				{Role: "tool", Content: []Content{{Type: "text", Text: "noon"}}, ToolCallID: "c2"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := buildRequest("m", tt.msgs, "", nil)
			if diff := cmp.Diff(tt.want, req.Messages); diff != "" {
				t.Fatalf("buildRequest messages mismatch (-want +got):\n%s", diff)
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
			req := buildRequest("m", []provpkg.Message{promptMessage("p", nil)}, tt.schemaName, tt.schema)
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
