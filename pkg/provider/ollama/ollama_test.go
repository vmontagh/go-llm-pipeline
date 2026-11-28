package ollama_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/vmontagh/go-llm-pipeline/pkg/httputil"
	provpkg "github.com/vmontagh/go-llm-pipeline/pkg/provider"
	"github.com/vmontagh/go-llm-pipeline/pkg/provider/ollama"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProvider_Call_Think(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		opts      []ollama.ProviderOption
		wantThink json.RawMessage
	}{
		{
			name: "unset omits think so the model default applies",
		},
		{
			name:      "disabled sends think false",
			opts:      []ollama.ProviderOption{ollama.WithThink(false)},
			wantThink: json.RawMessage("false"),
		},
		{
			name:      "enabled sends think true",
			opts:      []ollama.ProviderOption{ollama.WithThink(true)},
			wantThink: json.RawMessage("true"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client, gotBody := captureRequestBody(t)
			p := ollama.NewProvider("http://host", "m", append(tt.opts, ollama.WithHTTPClient(client))...)

			got := p.Call(t.Context(), nil, "p", nil)

			if diff := cmp.Diff(provpkg.OutcomeSuccess, got.Outcome); diff != "" {
				t.Fatalf("Call() outcome mismatch (-want +got):\n%s", diff)
			}
			var wire map[string]json.RawMessage
			if err := json.Unmarshal(*gotBody, &wire); err != nil {
				t.Fatalf("unmarshal request body: %v", err)
			}
			if diff := cmp.Diff(tt.wantThink, wire["think"]); diff != "" {
				t.Errorf("Call() wire think mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// captureRequestBody returns a client that records the request body it
// receives and answers with a minimal successful generate response.
func captureRequestBody(t *testing.T) (client *http.Client, gotBody *[]byte) {
	t.Helper()
	var body []byte
	client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, fmt.Errorf("reading request body: %w", err)
		}
		body = b
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"response":"ok"}`)),
			Header:     make(http.Header),
		}, nil
	})}
	return client, &body
}

// newCrossOriginRedirect starts a server that redirects every request to a
// second server, and returns the first server's URL and a func reporting
// whether the second one was reached.
func newCrossOriginRedirect(t *testing.T) (redirectorURL string, leaked func() bool) {
	t.Helper()
	var hit atomic.Bool
	sink := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit.Store(true) }))
	t.Cleanup(sink.Close)
	redirector := httptest.NewServer(http.RedirectHandler(sink.URL, http.StatusTemporaryRedirect))
	t.Cleanup(redirector.Close)
	return redirector.URL, hit.Load
}

func callProvider(t *testing.T, host string, client *http.Client) error {
	t.Helper()
	p := ollama.NewProvider(host, "m", ollama.WithHTTPClient(client))
	return p.Call(t.Context(), &provpkg.Lease[struct{}]{}, "prompt", nil).Err
}

// callEmbedder disables retries: a refused redirect is a transient error.
func callEmbedder(t *testing.T, host string, client *http.Client) error {
	t.Helper()
	e := ollama.NewEmbedder(host, "m", ollama.WithHTTPClient(client), ollama.WithMaxRetries(0))
	_, err := e.Embed(t.Context(), "x")
	return err
}

func TestCrossOriginRedirectRefused(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		call func(t *testing.T, host string, client *http.Client) error
	}{
		{name: "provider", call: callProvider},
		{name: "embedder", call: callEmbedder},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			host, leaked := newCrossOriginRedirect(t)
			caller := &http.Client{}

			err := tt.call(t, host, caller)

			if !errors.Is(err, httputil.ErrCrossOriginRedirect) {
				t.Errorf("error = %v, want %v", err, httputil.ErrCrossOriginRedirect)
			}
			if leaked() {
				t.Error("redirect target received the request")
			}
			if caller.CheckRedirect != nil {
				t.Error("caller client was mutated: CheckRedirect set")
			}
		})
	}
}
