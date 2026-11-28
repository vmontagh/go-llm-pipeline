package ollama

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

// errRead is returned by errReadBody so tests can assert that doJSONPost
// preserves the underlying read error through its wrapping.
var errRead = errors.New("read failed")

// errReadBody is a response body whose Read always fails with errRead.
type errReadBody struct{}

func (errReadBody) Read([]byte) (int, error) { return 0, errRead }
func (errReadBody) Close() error             { return nil }

func TestDoJSONPost(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.DiscardHandler)

	const okBody = `{"ok":true}`
	readErrClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		h := make(http.Header)
		h.Set("Retry-After", "3")
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: errReadBody{}, Header: h}, nil
	})}
	okClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		h := make(http.Header)
		h.Set("Retry-After", "2")
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(okBody)), Header: h}, nil
	})}

	tests := []struct {
		name       string
		client     *http.Client
		host       string
		body       any
		wantErrIs  error
		wantResult jsonPostResult
	}{
		{
			name:      "marshal failure is not sent",
			client:    &http.Client{},
			host:      "http://ollama.test",
			body:      make(chan int),
			wantErrIs: errRequestNotSent,
		},
		{
			name:      "request build failure is not sent",
			client:    &http.Client{},
			host:      "http://ollama\x7f.test",
			body:      map[string]string{"k": "v"},
			wantErrIs: errRequestNotSent,
		},
		{
			name:       "read failure preserves status code and underlying error",
			client:     readErrClient,
			host:       "http://ollama.test",
			body:       map[string]string{"k": "v"},
			wantErrIs:  errRead,
			wantResult: jsonPostResult{statusCode: http.StatusServiceUnavailable, retryAfter: 3 * time.Second},
		},
		{
			name:       "success returns body, status, and retry-after",
			client:     okClient,
			host:       "http://ollama.test/",
			body:       map[string]string{"k": "v"},
			wantResult: jsonPostResult{statusCode: http.StatusOK, body: []byte(okBody), retryAfter: 2 * time.Second},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := doJSONPost(t.Context(), jsonPostRequest{
				client: tt.client,
				logger: logger,
				host:   tt.host,
				path:   "/api/embed",
				body:   tt.body,
			})
			if tt.wantErrIs != nil {
				if !errors.Is(err, tt.wantErrIs) {
					t.Fatalf("doJSONPost(%q) error = %v, want errors.Is %v", tt.name, err, tt.wantErrIs)
				}
			} else if err != nil {
				t.Fatalf("doJSONPost(%q) error = %v, want nil", tt.name, err)
			}
			if diff := cmp.Diff(tt.wantResult, got, cmp.AllowUnexported(jsonPostResult{})); diff != "" {
				t.Errorf("doJSONPost(%q) result mismatch (-want +got):\n%s", tt.name, diff)
			}
		})
	}
}
