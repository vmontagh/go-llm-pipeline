package gemini_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/pkg/httputil"
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

func callMessages(t *testing.T, p provpkg.Provider[string], msgs []provpkg.Message) provpkg.CallResult {
	t.Helper()
	mc, ok := p.(provpkg.MessageCaller[string])
	if !ok {
		t.Fatal("provider is not a MessageCaller")
	}
	return mc.CallMessages(t.Context(), &provpkg.Lease[string]{Value: testAPIKey}, msgs)
}

func callTools(t *testing.T, p provpkg.Provider[string], msgs []provpkg.Message, tools []provpkg.ToolSpec) provpkg.CallResult {
	t.Helper()
	tc, ok := provpkg.AsToolCaller(p)
	if !ok {
		t.Fatal("provider is not a ToolCaller")
	}
	return tc.CallTools(t.Context(), &provpkg.Lease[string]{Value: testAPIKey}, msgs, tools)
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
		msgs     []provpkg.Message
		tools    []provpkg.ToolSpec
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
			name: "messages map system and assistant roles",
			msgs: []provpkg.Message{
				provpkg.TextMessage(provpkg.RoleSystem, "s"),
				provpkg.TextMessage(provpkg.RoleUser, "u"),
				provpkg.TextMessage(provpkg.RoleAssistant, "a"),
				{Role: provpkg.RoleUser, Parts: []provpkg.Part{{Image: []byte("img")}}},
			},
			wantPath: apiVersionPath + "/models/" + gemini.DefaultModel + ":generateContent",
			wantBody: map[string]any{
				"systemInstruction": map[string]any{"parts": []any{map[string]any{"text": "s"}}},
				"contents": []any{
					map[string]any{"role": "user", "parts": []any{map[string]any{"text": "u"}}},
					map[string]any{"role": "model", "parts": []any{map[string]any{"text": "a"}}},
					map[string]any{"role": "user", "parts": []any{
						map[string]any{"inlineData": map[string]any{"mimeType": "image/jpeg", "data": "aW1n"}},
					}},
				},
			},
		},
		{
			name:     "resource-qualified model is not prefixed",
			opts:     []gemini.ProviderOption{gemini.WithModel("tunedModels/t1")},
			wantPath: apiVersionPath + "/tunedModels/t1:generateContent",
			wantBody: map[string]any{"contents": textOnlyContents},
		},
		{
			name: "tool round-trip",
			msgs: []provpkg.Message{
				provpkg.TextMessage(provpkg.RoleUser, "u"),
				{Role: provpkg.RoleAssistant, Parts: []provpkg.Part{
					{Text: "checking", Signature: "ts"},
					{ToolCall: &provpkg.ToolCall{ID: "c1", Name: "add", Arguments: `{"A":1}`}, Signature: "sig"},
					{Signature: "es"},
					{ToolCall: &provpkg.ToolCall{Name: "now"}},
					{ToolCall: &provpkg.ToolCall{Name: "add", Arguments: `{"A":1`}},
					{ToolCall: &provpkg.ToolCall{Name: "add", Arguments: `[1]`}},
					{ToolCall: &provpkg.ToolCall{Name: "add", Arguments: `null`}},
				}},
				{Role: provpkg.RoleTool, Parts: []provpkg.Part{
					{ToolResult: &provpkg.ToolResult{CallID: "c1", Name: "add", Content: `{"Sum":1}`}},
					{ToolResult: &provpkg.ToolResult{Name: "now", Content: "clock broken"}},
				}},
			},
			tools:    []provpkg.ToolSpec{{Name: "add", Description: "adds", Parameters: map[string]any{"type": "object"}}, {Name: "now"}},
			wantPath: apiVersionPath + "/models/" + gemini.DefaultModel + ":generateContent",
			wantBody: map[string]any{
				"contents": []any{
					map[string]any{"role": "user", "parts": []any{map[string]any{"text": "u"}}},
					map[string]any{"role": "model", "parts": []any{
						map[string]any{"text": "checking", "thoughtSignature": "ts"},
						map[string]any{"functionCall": map[string]any{"id": "c1", "name": "add", "args": map[string]any{"A": 1.0}}, "thoughtSignature": "sig"},
						map[string]any{"text": "", "thoughtSignature": "es"},
						map[string]any{"functionCall": map[string]any{"name": "now", "args": map[string]any{}}},
						map[string]any{"functionCall": map[string]any{"name": "add", "args": map[string]any{}}},
						map[string]any{"functionCall": map[string]any{"name": "add", "args": map[string]any{}}},
						map[string]any{"functionCall": map[string]any{"name": "add", "args": map[string]any{}}},
					}},
					map[string]any{"role": "user", "parts": []any{
						map[string]any{"functionResponse": map[string]any{"id": "c1", "name": "add", "response": map[string]any{"result": map[string]any{"Sum": 1.0}}}},
						map[string]any{"functionResponse": map[string]any{"name": "now", "response": map[string]any{"result": "clock broken"}}},
					}},
				},
				"tools": []any{map[string]any{"functionDeclarations": []any{
					map[string]any{"name": "add", "description": "adds", "parametersJsonSchema": map[string]any{"type": "object"}},
					map[string]any{"name": "now"},
				}}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv, captured := newCapturingServer(t)
			opts := append([]gemini.ProviderOption{gemini.WithBaseURL(srv.URL + apiVersionPath + "/")}, tt.opts...)
			p := gemini.NewProvider(nil, opts...)

			var got provpkg.CallResult
			switch {
			case tt.tools != nil:
				got = callTools(t, p, tt.msgs, tt.tools)
			case tt.msgs != nil:
				got = callMessages(t, p, tt.msgs)
			default:
				got = call(t, p, tt.image)
			}

			if got.Err != nil {
				t.Fatalf("call error = %v", got.Err)
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

func TestCallTools_Response(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		want provpkg.CallResult
	}{
		{
			name: "calls and text in order with signatures, without thought text",
			body: `{"candidates":[{"content":{"parts":[` +
				`{"text":"thinking","thought":true},` +
				`{"text":"reasoning","thought":true,"thoughtSignature":"th"},` +
				`{"text":"checking","thoughtSignature":"ts"},` +
				`{"functionCall":{"id":"c1","name":"add","args":{"A":1}},"thoughtSignature":"sig"},` +
				`{"text":"","thoughtSignature":"es"},` +
				`{"text":""},` +
				`{"text":" more"},` +
				`{"functionCall":{"name":"now"}}]}}]}`,
			want: provpkg.CallResult{
				Outcome:   provpkg.OutcomeSuccess,
				Text:      "checking more",
				ToolCalls: []provpkg.ToolCall{{ID: "c1", Name: "add", Arguments: `{"A":1}`}, {Name: "now"}},
				Parts: []provpkg.Part{
					{Signature: "th"},
					{Text: "checking", Signature: "ts"},
					{ToolCall: &provpkg.ToolCall{ID: "c1", Name: "add", Arguments: `{"A":1}`}, Signature: "sig"},
					{Signature: "es"},
					{Text: " more"},
					{ToolCall: &provpkg.ToolCall{Name: "now"}},
				},
			},
		},
		{
			name: "calls without text",
			body: `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"now"}}]}}]}`,
			want: provpkg.CallResult{Outcome: provpkg.OutcomeSuccess, ToolCalls: []provpkg.ToolCall{{Name: "now"}}, Parts: []provpkg.Part{{ToolCall: &provpkg.ToolCall{Name: "now"}}}},
		},
		{
			name: "final reply joins text parts",
			body: `{"candidates":[{"content":{"parts":[{"text":"do"},{"text":"ne"}]}}]}`,
			want: provpkg.CallResult{Outcome: provpkg.OutcomeSuccess, Text: "done", Parts: []provpkg.Part{{Text: "do"}, {Text: "ne"}}},
		},
		{
			name: "empty answer text",
			body: `{"candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"ts"}]}}]}`,
			want: provpkg.CallResult{Outcome: provpkg.OutcomeSuccess, Parts: []provpkg.Part{{Signature: "ts"}}},
		},
		{
			name: "candidate without parts",
			body: `{"candidates":[{"content":{"parts":[]},"finishReason":"MAX_TOKENS"}]}`,
			want: provpkg.CallResult{Outcome: provpkg.OutcomeTerminal, Err: provpkg.ErrEmptyCandidate},
		},
		{
			name: "call cut off at the token limit",
			body: `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"add"}}]},"finishReason":"MAX_TOKENS"}]}`,
			want: provpkg.CallResult{Outcome: provpkg.OutcomeTerminal, Err: provpkg.ErrInvalidResponsePart},
		},
		{
			name: "only thoughts",
			body: `{"candidates":[{"content":{"parts":[{"text":"thinking","thought":true,"thoughtSignature":"th"},{"inlineData":{"mimeType":"image/png","data":""}}]}}]}`,
			want: provpkg.CallResult{Outcome: provpkg.OutcomeTerminal, Err: provpkg.ErrInvalidResponsePart},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := newStubServer(t, http.StatusOK, nil, tt.body)
			p := gemini.NewProvider(nil, gemini.WithBaseURL(srv.URL))

			got := callTools(t, p, []provpkg.Message{provpkg.TextMessage(provpkg.RoleUser, testPrompt)}, []provpkg.ToolSpec{{Name: "add"}})

			if diff := cmp.Diff(tt.want, got, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("CallTools() mismatch (-want +got):\n%s", diff)
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
			header: http.Header{httputil.RetryAfterHeader: {"7"}},
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

// roundTripperFunc adapts a function to http.RoundTripper.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// errReader is a response body whose reads always fail with err.
type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

func TestCall_TransportFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		rt   roundTripperFunc
		want provpkg.CallResult
	}{
		{
			name: "request fails",
			rt:   func(*http.Request) (*http.Response, error) { return nil, errTest },
			want: provpkg.CallResult{Outcome: provpkg.OutcomeProviderError, Err: errTest},
		},
		{
			name: "body read fails on rate limit",
			rt: func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Header:     http.Header{httputil.RetryAfterHeader: {"7"}},
					Body:       io.NopCloser(errReader{errTest}),
					Request:    r,
				}, nil
			},
			want: provpkg.CallResult{
				Outcome:    provpkg.OutcomeProviderError,
				Err:        errTest,
				StatusCode: http.StatusTooManyRequests,
				RetryAfter: retryAfterSecs * time.Second,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := gemini.NewProvider(nil, gemini.WithHTTPClient(&http.Client{Transport: tt.rt}))

			got := call(t, p, nil)

			if diff := cmp.Diff(tt.want, got, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Call() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestWithHTTPClientTimeout(t *testing.T) {
	t.Parallel()
	// The handler never reads the body, so the server may not notice the
	// client hanging up; release it explicitly before srv.Close waits on it.
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
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

// newRedirectServer answers every request with a 307 to target(srv), where
// srv is the redirecting server itself.
func newRedirectServer(t *testing.T, newServer func(http.Handler) *httptest.Server, target func(srv *httptest.Server) string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = newServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		http.Redirect(rw, r, target(srv), http.StatusTemporaryRedirect)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCall_CrossOriginRedirectRefused(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		newServer func(http.Handler) *httptest.Server
		target    func(redirector, other *httptest.Server) string
	}{
		{
			name:      "different host",
			newServer: httptest.NewServer,
			target:    func(_, other *httptest.Server) string { return other.URL },
		},
		{
			name:      "HTTPS to HTTP downgrade on the same host",
			newServer: httptest.NewTLSServer,
			target: func(redirector, _ *httptest.Server) string {
				return "http://" + redirector.Listener.Addr().String()
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			leaked := make(chan string, 1)
			other := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				leaked <- r.Header.Get("X-Goog-Api-Key")
			}))
			t.Cleanup(other.Close)
			redirector := newRedirectServer(t, tt.newServer, func(srv *httptest.Server) string { return tt.target(srv, other) })
			p := gemini.NewProvider(nil, gemini.WithBaseURL(redirector.URL), gemini.WithHTTPClient(redirector.Client()))

			got := call(t, p, []byte("img"))

			if !errors.Is(got.Err, gemini.ErrCrossOriginRedirect) {
				t.Errorf("Call() error = %v, want %v", got.Err, gemini.ErrCrossOriginRedirect)
			}
			select {
			case key := <-leaked:
				t.Errorf("redirect target received a request with API key %q", key)
			default:
			}
		})
	}
}

func TestStream(t *testing.T) {
	t.Parallel()
	const (
		hel   = "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"Hel\"}]}}],\"usageMetadata\":{\"promptTokenCount\":3}}\n\n"
		usage = "data: {\"usageMetadata\":{\"promptTokenCount\":3}}\n\n"
		done  = "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"l\"},{\"text\":\"o\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":3,\"candidatesTokenCount\":2,\"thoughtsTokenCount\":4}}\n\n"
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
			name:    "streams chunks and final usage",
			status:  http.StatusOK,
			body:    strings.NewReader(hel + usage + done),
			want:    []provpkg.Chunk{{Text: "Hel"}, {Text: "lo", Usage: &provpkg.Usage{Input: 3, Output: 6}}},
			wantRes: opened,
		},
		{
			name:    "caller stops early",
			status:  http.StatusOK,
			body:    strings.NewReader(hel + done),
			onFirst: stopReading,
			want:    []provpkg.Chunk{{Text: "Hel"}},
			wantRes: opened,
		},
		{
			name:    "caller cancels",
			status:  http.StatusOK,
			body:    strings.NewReader(hel + done),
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
			name:    "ends without finish reason",
			status:  http.StatusOK,
			body:    strings.NewReader(hel),
			want:    []provpkg.Chunk{{Text: "Hel"}},
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
			name:    "prompt blocked",
			status:  http.StatusOK,
			body:    strings.NewReader("data: {\"promptFeedback\":{\"blockReason\":\"SAFETY\"}}\n\n"),
			wantRes: opened,
			wantErr: provpkg.ErrEmptyCandidate,
		},
		{
			name:    "final event without text",
			status:  http.StatusOK,
			body:    strings.NewReader(hel + "data: {\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":3,\"candidatesTokenCount\":1}}\n\n"),
			want:    []provpkg.Chunk{{Text: "Hel"}, {Usage: &provpkg.Usage{Input: 3, Output: 1}}},
			wantRes: opened,
		},
		{
			name:    "empty text part",
			status:  http.StatusOK,
			body:    strings.NewReader("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":3}}\n\n"),
			want:    []provpkg.Chunk{{Usage: &provpkg.Usage{Input: 3}}},
			wantRes: opened,
		},
		{
			name:    "finishes without text",
			status:  http.StatusOK,
			body:    strings.NewReader(usage + "data: {\"candidates\":[{\"finishReason\":\"MAX_TOKENS\"}]}\n\n"),
			wantRes: opened,
			wantErr: provpkg.ErrEmptyCandidate,
		},
		{
			name:    "non-text part",
			status:  http.StatusOK,
			body:    strings.NewReader("data: {\"candidates\":[{\"content\":{\"parts\":[{\"inlineData\":{}}]}}]}\n\n"),
			wantRes: opened,
			wantErr: provpkg.ErrInvalidResponsePart,
		},
		{
			name:    "bad event",
			status:  http.StatusOK,
			body:    strings.NewReader("data: nope\n\n"),
			wantRes: opened,
			wantErr: provpkg.ErrInvalidResponsePart,
		},
		{
			name:   "fails to open",
			status: http.StatusServiceUnavailable,
			body:   strings.NewReader("busy"),
			wantRes: provpkg.CallResult{
				Outcome:    provpkg.OutcomeProviderError,
				Err:        &gemini.APIError{StatusCode: http.StatusServiceUnavailable, Message: "Service Unavailable"},
				StatusCode: http.StatusServiceUnavailable,
				Body:       []byte("busy"),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, body := newStreamProvider(t, tt.status, tt.body)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			seq, res := s.Stream(ctx, &provpkg.Lease[string]{Value: testAPIKey}, []provpkg.Message{provpkg.TextMessage(provpkg.RoleUser, testPrompt)})
			got, err := readStream(seq, func() bool {
				if tt.onFirst == cancelContext {
					cancel()
				}
				return tt.onFirst == stopReading
			})

			if diff := cmp.Diff(tt.wantRes, res); diff != "" {
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

// newStreamProvider returns a provider that answers a streaming request with
// status and body, and the body so a test can check that it was closed. Any
// other request fails the test.
func newStreamProvider(t *testing.T, status int, body io.Reader) (provpkg.Streamer[string], *closeTracker) {
	t.Helper()
	tracked := &closeTracker{Reader: body}
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		const wantPath = apiVersionPath + "/models/" + gemini.DefaultModel + ":streamGenerateContent"
		if r.URL.Path != wantPath || r.URL.Query().Get("alt") != "sse" {
			t.Errorf("request URL = %s, want %s?alt=sse", r.URL, wantPath)
		}
		return &http.Response{StatusCode: status, Body: tracked, Header: make(http.Header)}, nil
	})}
	s, ok := provpkg.AsStreamer(gemini.NewProvider([]string{testAPIKey}, gemini.WithBaseURL("http://example.test"+apiVersionPath), gemini.WithHTTPClient(client)))
	if !ok {
		t.Fatal("AsStreamer() = false, want true")
	}
	return s, tracked
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
