package ollama

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func newTestTransport(client *http.Client, numCtx int) *transport {
	return &transport{
		host:   "http://example.test",
		model:  "test-model",
		numCtx: numCtx,
		client: client,
		logger: slog.New(slog.DiscardHandler),
		tracer: noOpTracer{},
	}
}

func showResponseBody(arch string, ctxLen int) string {
	if ctxLen == 0 {
		return `{"model_info":{}}`
	}
	return `{"model_info":{"` + arch + `.context_length":` + intToString(ctxLen) + `}}`
}

func intToString(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func TestPreflight(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		client     *http.Client
		numCtx     int
		wantErr    error
		wantCtxLen int
	}{
		{
			name: "large context window passes and records context length",
			client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(showResponseBody("llama", 16384))),
					Header:     make(http.Header),
				}, nil
			})},
			wantCtxLen: 16384,
		},
		{
			name: "small context window fails with sentinel error",
			client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(showResponseBody("llama", 1024))),
					Header:     make(http.Header),
				}, nil
			})},
			wantErr:    ErrContextWindowTooSmall,
			wantCtxLen: 1024,
		},
		{
			name: "user-supplied num_ctx caps the effective window",
			client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(showResponseBody("llama", 16384))),
					Header:     make(http.Header),
				}, nil
			})},
			numCtx:     1024,
			wantErr:    ErrContextWindowTooSmall,
			wantCtxLen: 1024,
		},
		{
			name: "missing context_length is tolerated",
			client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(showResponseBody("", 0))),
					Header:     make(http.Header),
				}, nil
			})},
		},
		{
			name: "unreachable ollama is non-fatal (graceful degradation)",
			client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errTest
			})},
		},
		{
			name: "non-200 from /api/show is non-fatal",
			client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Body:       io.NopCloser(strings.NewReader(`{"error":"model not found"}`)),
					Header:     make(http.Header),
				}, nil
			})},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tr := newTestTransport(tt.client, tt.numCtx)
			err := tr.Preflight(context.Background())
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want errors.Is(_, %v)", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if diff := cmp.Diff(tt.wantCtxLen, tr.ContextLength()); diff != "" {
				t.Errorf("ContextLength mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestExtractContextLength(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		in       map[string]any
		want     int
		wantErrs []error
	}{
		{name: "llama prefix", in: map[string]any{"llama.context_length": float64(8192)}, want: 8192},
		{name: "gemma2 prefix", in: map[string]any{"gemma2.context_length": float64(4096)}, want: 4096},
		{name: "int value", in: map[string]any{"llava.context_length": 2048}, want: 2048},
		{name: "unrelated keys are ignored", in: map[string]any{"general.parameter_count": float64(34000000000)}, wantErrs: []error{ErrContextLengthMissing}},
		{name: "missing context_length", in: map[string]any{}, wantErrs: []error{ErrContextLengthMissing}},
		{name: "non-numeric value preserves root cause", in: map[string]any{"llama.context_length": "big"}, wantErrs: []error{ErrContextLengthUnsupportedType, ErrUnsupportedNumericType}},
		{name: "fractional value preserves root cause", in: map[string]any{"llama.context_length": float64(8192.5)}, wantErrs: []error{ErrContextLengthUnsupportedType, ErrFractionalValue}},
		{name: "unsupported type preserves root cause", in: map[string]any{"llama.context_length": true}, wantErrs: []error{ErrContextLengthUnsupportedType, ErrUnsupportedNumericType}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := extractContextLength(tt.in)
			if len(tt.wantErrs) > 0 {
				for _, want := range tt.wantErrs {
					if !errors.Is(err, want) {
						t.Errorf("extractContextLength(%v) = %v, want errors.Is(_, %v)", tt.in, err, want)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("extractContextLength mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
