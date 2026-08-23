package ollama

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDoJSONPost(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.DiscardHandler)

	t.Run("marshal failure is not sent", func(t *testing.T) {
		t.Parallel()
		_, err := doJSONPost(t.Context(), jsonPostRequest{
			client: &http.Client{},
			logger: logger,
			host:   "http://ollama.test",
			path:   "/api/embed",
			body:   make(chan int),
		})
		if !errors.Is(err, errRequestNotSent) {
			t.Fatalf("doJSONPost() error = %v, want errors.Is errRequestNotSent", err)
		}
	})

	t.Run("request build failure is not sent", func(t *testing.T) {
		t.Parallel()
		_, err := doJSONPost(t.Context(), jsonPostRequest{
			client: &http.Client{},
			logger: logger,
			host:   "http://ollama\x7f.test",
			path:   "/api/embed",
			body:   map[string]string{"k": "v"},
		})
		if !errors.Is(err, errRequestNotSent) {
			t.Fatalf("doJSONPost() error = %v, want errors.Is errRequestNotSent", err)
		}
	})

	t.Run("read failure keeps status code and is retryable", func(t *testing.T) {
		t.Parallel()
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusInternalServerError, Body: errReadCloser{}, Header: make(http.Header)}, nil
		})}
		res, err := doJSONPost(t.Context(), jsonPostRequest{
			client: client,
			logger: logger,
			host:   "http://ollama.test",
			path:   "/api/embed",
			body:   map[string]string{"k": "v"},
		})
		if err == nil {
			t.Fatalf("doJSONPost() error = nil, want read error")
		}
		if errors.Is(err, errRequestNotSent) {
			t.Fatalf("doJSONPost() error = %v, want not errRequestNotSent", err)
		}
		if res.statusCode != http.StatusInternalServerError {
			t.Fatalf("doJSONPost() statusCode = %d, want %d", res.statusCode, http.StatusInternalServerError)
		}
	})

	t.Run("success returns body, status, and retry-after", func(t *testing.T) {
		t.Parallel()
		const wantBody = `{"ok":true}`
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			h := make(http.Header)
			h.Set("Retry-After", "2")
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(wantBody)),
				Header:     h,
			}, nil
		})}
		res, err := doJSONPost(t.Context(), jsonPostRequest{
			client: client,
			logger: logger,
			host:   "http://ollama.test/",
			path:   "/api/embed",
			body:   map[string]string{"k": "v"},
		})
		if err != nil {
			t.Fatalf("doJSONPost() error = %v, want nil", err)
		}
		if res.statusCode != http.StatusOK {
			t.Errorf("doJSONPost() statusCode = %d, want %d", res.statusCode, http.StatusOK)
		}
		if string(res.body) != wantBody {
			t.Errorf("doJSONPost() body = %q, want %q", res.body, wantBody)
		}
		if res.retryAfter != 2*time.Second {
			t.Errorf("doJSONPost() retryAfter = %v, want %v", res.retryAfter, 2*time.Second)
		}
	})
}
