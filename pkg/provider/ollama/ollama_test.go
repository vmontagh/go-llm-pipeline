package ollama_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

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
