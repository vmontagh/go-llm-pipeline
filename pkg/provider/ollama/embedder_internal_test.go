package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	provpkg "github.com/vmontagh/go-llm-pipeline/pkg/provider"
)

// newTestEmbedder returns an Embedder wired to rt with a no-op clock and zero
// base backoff so retry paths run without real waits.
func newTestEmbedder(model string, rt roundTripFunc, opts ...ProviderOption) *Embedder {
	opts = append([]ProviderOption{WithHTTPClient(&http.Client{Transport: rt})}, opts...)
	e := NewEmbedder("http://ollama.test", model, opts...)
	e.baseBackoff = 0
	e.sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	return e
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func TestEmbedder_Embed_Success(t *testing.T) {
	t.Parallel()
	var gotReq embedRequest
	var gotPath string
	e := newTestEmbedder("nomic", func(r *http.Request) (*http.Response, error) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Errorf("decode request: %v", err)
		}
		return jsonResponse(http.StatusOK, `{"embeddings":[[0.1,0.2,0.3]]}`), nil
	})

	got, err := e.Embed(t.Context(), "hello")
	if err != nil {
		t.Fatalf("Embed(hello) error = %v, want nil", err)
	}
	want := []float64{0.1, 0.2, 0.3}
	if diff := cmp.Diff(want, got, cmpopts.EquateApprox(0, 1e-9)); diff != "" {
		t.Errorf("Embed(hello) vector mismatch (-want +got):\n%s", diff)
	}
	if gotPath != embedEndpoint {
		t.Errorf("request path = %q, want %q", gotPath, embedEndpoint)
	}
	if diff := cmp.Diff(embedRequest{Model: "nomic", Input: "hello"}, gotReq); diff != "" {
		t.Errorf("request body mismatch (-want +got):\n%s", diff)
	}
}

func TestEmbedder_EmbedBatch_SuccessPreservesOrder(t *testing.T) {
	t.Parallel()
	var gotInput any
	e := newTestEmbedder("nomic", func(r *http.Request) (*http.Response, error) {
		var req embedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		gotInput = req.Input
		return jsonResponse(http.StatusOK, `{"embeddings":[[1,0],[0,1],[1,1]]}`), nil
	})

	got, err := e.EmbedBatch(t.Context(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("EmbedBatch error = %v, want nil", err)
	}
	want := [][]float64{{1, 0}, {0, 1}, {1, 1}}
	if diff := cmp.Diff(want, got, cmpopts.EquateApprox(0, 1e-9)); diff != "" {
		t.Errorf("EmbedBatch vectors mismatch (-want +got):\n%s", diff)
	}
	// Batch input must be marshaled as a JSON array so Ollama batches natively.
	inputs, ok := gotInput.([]any)
	if !ok {
		t.Fatalf("request input type = %T, want []any", gotInput)
	}
	if diff := cmp.Diff([]any{"a", "b", "c"}, inputs); diff != "" {
		t.Errorf("request input mismatch (-want +got):\n%s", diff)
	}
}

func TestEmbedder_ValidationErrors(t *testing.T) {
	t.Parallel()
	// A transport that fails the test if called: validation must short-circuit
	// before any HTTP request.
	tests := []struct {
		name  string
		model string
		call  func(e *Embedder) error
		want  error
	}{
		{
			name:  "empty single input",
			model: "m",
			call:  func(e *Embedder) error { _, err := e.Embed(t.Context(), ""); return err },
			want:  ErrEmptyInput,
		},
		{
			name:  "empty batch",
			model: "m",
			call:  func(e *Embedder) error { _, err := e.EmbedBatch(t.Context(), nil); return err },
			want:  ErrEmptyInput,
		},
		{
			name:  "batch with empty element",
			model: "m",
			call:  func(e *Embedder) error { _, err := e.EmbedBatch(t.Context(), []string{"a", ""}); return err },
			want:  ErrEmptyInput,
		},
		{
			name:  "empty model single",
			model: "",
			call:  func(e *Embedder) error { _, err := e.Embed(t.Context(), "a"); return err },
			want:  ErrEmptyModel,
		},
		{
			name:  "empty model batch",
			model: "",
			call:  func(e *Embedder) error { _, err := e.EmbedBatch(t.Context(), []string{"a"}); return err },
			want:  ErrEmptyModel,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newTestEmbedder(tt.model, func(*http.Request) (*http.Response, error) {
				t.Error("transport called; validation should short-circuit")
				return jsonResponse(http.StatusOK, `{"embeddings":[[1]]}`), nil
			})
			err := tt.call(e)
			if !errors.Is(err, tt.want) {
				t.Errorf("error = %v, want errors.Is %v", err, tt.want)
			}
		})
	}
}

func TestEmbedder_ResponseErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		rt   roundTripFunc
		want error
	}{
		{
			name: "malformed json is terminal",
			rt: func(*http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusOK, `{"embeddings":`), nil
			},
			want: provpkg.ErrInvalidResponsePart,
		},
		{
			name: "wrong embedding count",
			rt: func(*http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusOK, `{"embeddings":[]}`), nil
			},
			want: provpkg.ErrInvalidResponsePart,
		},
		{
			name: "400 is terminal",
			rt: func(*http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusBadRequest, `bad model`), nil
			},
			want: provpkg.ErrUnexpectedStatus,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newTestEmbedder("m", tt.rt)
			_, err := e.Embed(t.Context(), "x")
			if !errors.Is(err, tt.want) {
				t.Errorf("error = %v, want errors.Is %v", err, tt.want)
			}
		})
	}
}

func TestEmbedder_InconsistentBatchDimensions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
	}{
		{name: "shorter trailing vector", body: `{"embeddings":[[1,2,3],[1,2]]}`},
		{name: "zero-length first vector", body: `{"embeddings":[[],[1]]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newTestEmbedder("m", func(*http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusOK, tt.body), nil
			})
			_, err := e.EmbedBatch(t.Context(), []string{"a", "b"})
			if !errors.Is(err, ErrDimensionMismatch) {
				t.Errorf("error = %v, want errors.Is ErrDimensionMismatch", err)
			}
		})
	}
}

// TestEmbedder_ValidationJoinsAllFailures verifies that independent validation
// failures are reported together so callers can fix them in one pass (§1.5).
func TestEmbedder_ValidationJoinsAllFailures(t *testing.T) {
	t.Parallel()
	e := newTestEmbedder("", func(*http.Request) (*http.Response, error) {
		t.Error("transport called; validation should short-circuit")
		return jsonResponse(http.StatusOK, `{"embeddings":[[1]]}`), nil
	})
	_, err := e.EmbedBatch(t.Context(), []string{"a", ""})
	if !errors.Is(err, ErrEmptyInput) {
		t.Errorf("error = %v, want errors.Is ErrEmptyInput", err)
	}
	if !errors.Is(err, ErrEmptyModel) {
		t.Errorf("error = %v, want errors.Is ErrEmptyModel", err)
	}
}

// TestEmbedder_RetryTiming verifies the retry loop's delay schedule using a
// logical clock: the injected sleep records the durations it is asked to wait
// instead of touching the wall clock, so the test asserts on backoff logic
// rather than elapsed real time.
func TestEmbedder_RetryTiming(t *testing.T) {
	t.Parallel()
	const okBody = `{"embeddings":[[0.5]]}`
	type stubResp struct {
		status     int
		body       string
		retryAfter string
	}
	tests := []struct {
		name       string
		opts       []ProviderOption
		responses  []stubResp
		wantDelays []time.Duration
		wantErrIs  error
		wantVec    []float64
	}{
		{
			name: "exponential backoff capped by max",
			opts: []ProviderOption{
				WithMaxRetries(4),
				WithRetryBackoff(100*time.Millisecond, 250*time.Millisecond),
			},
			responses: []stubResp{{status: http.StatusInternalServerError, body: "boom"}},
			wantDelays: []time.Duration{
				100 * time.Millisecond,
				200 * time.Millisecond,
				250 * time.Millisecond,
				250 * time.Millisecond,
			},
			wantErrIs: provpkg.ErrUnexpectedStatus,
		},
		{
			name: "base larger than max is clamped on the first retry",
			opts: []ProviderOption{
				WithMaxRetries(2),
				WithRetryBackoff(10*time.Second, time.Second),
			},
			responses:  []stubResp{{status: http.StatusInternalServerError, body: "boom"}},
			wantDelays: []time.Duration{time.Second, time.Second},
			wantErrIs:  provpkg.ErrUnexpectedStatus,
		},
		{
			name: "retry-after overrides base backoff then succeeds",
			opts: []ProviderOption{
				WithMaxRetries(3),
				WithRetryBackoff(50*time.Millisecond, 0),
			},
			responses: []stubResp{
				{status: http.StatusTooManyRequests, body: "slow down", retryAfter: "2"},
				{status: http.StatusOK, body: okBody},
			},
			wantDelays: []time.Duration{2 * time.Second},
			wantVec:    []float64{0.5},
		},
		{
			name:      "success on first attempt never sleeps",
			opts:      []ProviderOption{WithMaxRetries(2)},
			responses: []stubResp{{status: http.StatusOK, body: okBody}},
			wantVec:   []float64{0.5},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var attempt int
			rt := roundTripFunc(func(*http.Request) (*http.Response, error) {
				r := tt.responses[min(attempt, len(tt.responses)-1)]
				attempt++
				resp := jsonResponse(r.status, r.body)
				if r.retryAfter != "" {
					resp.Header.Set("Retry-After", r.retryAfter)
				}
				return resp, nil
			})
			opts := append([]ProviderOption{WithHTTPClient(&http.Client{Transport: rt})}, tt.opts...)
			e := NewEmbedder("http://ollama.test", "m", opts...)

			var delays []time.Duration
			e.sleep = func(_ context.Context, d time.Duration) error {
				delays = append(delays, d)
				return nil
			}

			got, err := e.Embed(t.Context(), "x")
			if !errors.Is(err, tt.wantErrIs) {
				t.Fatalf("Embed(x) error = %v, want errors.Is %v", err, tt.wantErrIs)
			}
			if diff := cmp.Diff(tt.wantVec, got, cmpopts.EquateApprox(0, 1e-9), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Embed(x) vector mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantDelays, delays, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("backoff delays mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestEmbedder_TransientRetriesThenExhausts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		maxRetries   int
		wantAttempts int
	}{
		{name: "default budget", maxRetries: 2, wantAttempts: 3},
		{name: "zero budget disables retries", maxRetries: 0, wantAttempts: 1},
		{name: "single retry", maxRetries: 1, wantAttempts: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var attempts int
			e := newTestEmbedder("m", func(*http.Request) (*http.Response, error) {
				attempts++
				return jsonResponse(http.StatusInternalServerError, `boom`), nil
			}, WithMaxRetries(tt.maxRetries))

			_, err := e.Embed(t.Context(), "x")
			if !errors.Is(err, provpkg.ErrUnexpectedStatus) {
				t.Errorf("error = %v, want errors.Is ErrUnexpectedStatus", err)
			}
			if attempts != tt.wantAttempts {
				t.Errorf("attempts = %d, want %d", attempts, tt.wantAttempts)
			}
		})
	}
}

func TestEmbedder_ContextCancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	e := newTestEmbedder("m", func(r *http.Request) (*http.Response, error) {
		if err := r.Context().Err(); err != nil {
			return nil, err
		}
		return jsonResponse(http.StatusOK, `{"embeddings":[[1]]}`), nil
	})
	_, err := e.Embed(ctx, "x")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want errors.Is context.Canceled", err)
	}
}

func TestEmbedder_HTTPTestServer(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != embedEndpoint {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if _, err := io.WriteString(w, `{"embeddings":[[0.25,0.75]]}`); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer srv.Close()

	e := NewEmbedder(srv.URL, "m")
	got, err := e.Embed(t.Context(), "hello")
	if err != nil {
		t.Fatalf("Embed error = %v, want nil", err)
	}
	if diff := cmp.Diff([]float64{0.25, 0.75}, got, cmpopts.EquateApprox(0, 1e-9)); diff != "" {
		t.Errorf("vector mismatch (-want +got):\n%s", diff)
	}
}

func TestEmbedder_DoesNotMutateCallerClient(t *testing.T) {
	t.Parallel()
	shared := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"embeddings":[[1]]}`), nil
	})}
	e := NewEmbedder("http://ollama.test", "m",
		WithHTTPClient(shared), WithHTTPClientTimeout(5*time.Second))

	if shared.Timeout != 0 {
		t.Errorf("caller client Timeout = %v, want 0 (must not be mutated)", shared.Timeout)
	}
	if e.client == shared {
		t.Error("embedder reused the caller's client pointer; want a defensive copy")
	}
	if e.client.Timeout != 5*time.Second {
		t.Errorf("embedder client Timeout = %v, want 5s", e.client.Timeout)
	}
	if e.client.Transport == nil {
		t.Error("embedder client copy lost the caller's Transport")
	}
}

func TestEmbedder_ConcurrentUse(t *testing.T) {
	t.Parallel()
	e := newTestEmbedder("m", func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"embeddings":[[0.1,0.2]]}`), nil
	})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.Embed(t.Context(), "x"); err != nil {
				t.Errorf("Embed error = %v, want nil", err)
			}
			if _, err := e.EmbedBatch(t.Context(), []string{"a", "b"}); err != nil {
				// Single-vector canned response yields a count mismatch for a
				// 2-input batch; that is expected and not a data race.
				if !errors.Is(err, provpkg.ErrInvalidResponsePart) {
					t.Errorf("EmbedBatch error = %v, want ErrInvalidResponsePart", err)
				}
			}
		}()
	}
	wg.Wait()
}
