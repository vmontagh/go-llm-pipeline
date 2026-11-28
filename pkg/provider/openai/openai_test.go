package openai_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vmontagh/go-llm-pipeline/pkg/httputil"
	provpkg "github.com/vmontagh/go-llm-pipeline/pkg/provider"
	"github.com/vmontagh/go-llm-pipeline/pkg/provider/openai"
)

func TestProvider_CrossOriginRedirectRefused(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		newServer func(http.Handler) *httptest.Server
		// target is where the redirector sends the request.
		target func(redirector, sink *httptest.Server) string
	}{
		{
			name:      "different host",
			newServer: httptest.NewServer,
			target:    func(_, sink *httptest.Server) string { return sink.URL },
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
			var leaked atomic.Bool
			sink := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked.Store(true) }))
			t.Cleanup(sink.Close)
			redirector := newRedirectServer(t, tt.newServer, func(srv *httptest.Server) string { return tt.target(srv, sink) })
			caller := redirector.Client()
			p := openai.NewProvider(nil, openai.WithBaseURL(redirector.URL), openai.WithHTTPClient(caller), openai.WithHTTPClientTimeout(time.Minute))

			got := p.Call(t.Context(), &provpkg.Lease[string]{Value: "k"}, "prompt", []byte("img"))

			if !errors.Is(got.Err, httputil.ErrCrossOriginRedirect) {
				t.Errorf("Call() error = %v, want %v", got.Err, httputil.ErrCrossOriginRedirect)
			}
			if leaked.Load() {
				t.Error("redirect target received the request")
			}
			if caller.Timeout != 0 || caller.CheckRedirect != nil {
				t.Errorf("caller client was mutated: Timeout = %v, CheckRedirect set = %t", caller.Timeout, caller.CheckRedirect != nil)
			}
		})
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
