package httputil

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

var errTest = errors.New("test error")

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

// post sends a POST with a body through c and returns the response body.
func post(t *testing.T, c *http.Client, target string) (string, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, target, http.NoBody)
	if err != nil {
		t.Fatalf("NewRequest(%q) error = %v", target, err)
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing response body: %v", err)
		}
	}()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading response: %v", err)
	}
	return string(b), nil
}

func TestSameOriginClientRefusesCrossOrigin(t *testing.T) {
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
			var leaked atomic.Bool
			other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked.Store(true) }))
			t.Cleanup(other.Close)
			redirector := newRedirectServer(t, tt.newServer, func(srv *httptest.Server) string { return tt.target(srv, other) })

			_, err := post(t, SameOriginClient(redirector.Client(), 0), redirector.URL)

			if !errors.Is(err, ErrCrossOriginRedirect) {
				t.Errorf("Do() error = %v, want %v", err, ErrCrossOriginRedirect)
			}
			if leaked.Load() {
				t.Error("redirect target received a request")
			}
		})
	}
}

func TestSameOriginClientRechecksAfterCallerCheckRedirect(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		rewrite func(req *http.Request, via []*http.Request, host string)
	}{
		{
			name:    "callback rewrites the redirect URL",
			rewrite: func(req *http.Request, _ []*http.Request, host string) { req.URL.Host = host },
		},
		{
			name: "callback also rewrites the first request URL",
			rewrite: func(req *http.Request, via []*http.Request, host string) {
				req.URL.Host = host
				via[0].URL.Host = host
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var leaked atomic.Bool
			other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked.Store(true) }))
			t.Cleanup(other.Close)
			otherURL, err := url.Parse(other.URL)
			if err != nil {
				t.Fatalf("url.Parse(%q) error = %v", other.URL, err)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				http.Redirect(rw, r, "/next", http.StatusTemporaryRedirect)
			}))
			t.Cleanup(srv.Close)
			caller := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
				tt.rewrite(req, via, otherURL.Host)
				return nil
			}}

			_, err = post(t, SameOriginClient(caller, 0), srv.URL)

			if !errors.Is(err, ErrCrossOriginRedirect) {
				t.Errorf("Do() error = %v, want %v", err, ErrCrossOriginRedirect)
			}
			if leaked.Load() {
				t.Error("rewritten redirect target received a request")
			}
		})
	}
}

func TestSameOriginClientFollowsSameOrigin(t *testing.T) {
	t.Parallel()
	const okBody = "ok"
	tests := []struct {
		name          string
		checkRedirect func(*http.Request, []*http.Request) error
		want          string
		wantErr       error
	}{
		{name: "followed by default", want: okBody},
		{
			name:          "caller CheckRedirect still applies",
			checkRedirect: func(*http.Request, []*http.Request) error { return errTest },
			wantErr:       errTest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Has("moved") {
					if _, err := io.WriteString(rw, okBody); err != nil {
						t.Errorf("WriteString() = %v", err)
					}
					return
				}
				http.Redirect(rw, r, r.URL.Path+"?moved", http.StatusTemporaryRedirect)
			}))
			t.Cleanup(srv.Close)

			got, err := post(t, SameOriginClient(&http.Client{CheckRedirect: tt.checkRedirect}, 0), srv.URL)

			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Do() error mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Do() body mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSameOriginClientStopsRedirectLoop(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		client *http.Client
	}{
		{name: "default client"},
		{
			name: "caller callback accepts every redirect",
			client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
				return nil
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				http.Redirect(rw, r, r.URL.Path, http.StatusTemporaryRedirect)
			}))
			t.Cleanup(srv.Close)

			_, err := post(t, SameOriginClient(tt.client, 0), srv.URL)

			if !errors.Is(err, ErrTooManyRedirects) {
				t.Errorf("Do() error = %v, want %v", err, ErrTooManyRedirects)
			}
			const wantHits = 10
			if got := hits.Load(); got != wantHits {
				t.Errorf("server hits = %d, want %d", got, wantHits)
			}
		})
	}
}

func TestSameOriginClientTimeout(t *testing.T) {
	t.Parallel()
	const (
		callerTimeout = time.Minute
		timeout       = time.Second
	)
	tests := []struct {
		name    string
		timeout time.Duration
		want    time.Duration
	}{
		{name: "positive timeout replaces the caller's", timeout: timeout, want: timeout},
		{name: "zero keeps the caller's", want: callerTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			caller := &http.Client{Timeout: callerTimeout}

			got := SameOriginClient(caller, tt.timeout)

			if diff := cmp.Diff(tt.want, got.Timeout); diff != "" {
				t.Errorf("Timeout mismatch (-want +got):\n%s", diff)
			}
			if caller.Timeout != callerTimeout || caller.CheckRedirect != nil {
				t.Errorf("caller client was mutated: Timeout = %v, CheckRedirect set = %t", caller.Timeout, caller.CheckRedirect != nil)
			}
		})
	}
}

func TestSameOriginRedirectsOrigin(t *testing.T) {
	t.Parallel()
	tests := []struct {
		from, to string
		wantErr  error
	}{
		{from: "https://example.com/a", to: "https://EXAMPLE.com/b"},
		{from: "https://example.com/a", to: "https://example.com:443/b"},
		{from: "http://example.com:80/a", to: "HTTP://example.com/b"},
		{from: "http://[::1]:8080/a", to: "http://[::1]:8080/b"},
		{from: "https://example.com/a", to: "https://example.com:8443/b", wantErr: ErrCrossOriginRedirect},
		{from: "https://example.com/a", to: "http://example.com:443/b", wantErr: ErrCrossOriginRedirect},
		{from: "https://example.com/a", to: "https://api.example.com/b", wantErr: ErrCrossOriginRedirect},
		{from: "http://[FE80::1%25eth0]:8080/a", to: "http://[fe80::1%25eth0]:8080/b"},
		{from: "https://i.example/a", to: "https://\u0130.example/b", wantErr: ErrCrossOriginRedirect},
		{from: "http://[fe80::1%25ethA]:8080/a", to: "http://[fe80::1%25etha]:8080/b", wantErr: ErrCrossOriginRedirect},
	}
	for _, tt := range tests {
		t.Run(tt.from+" to "+tt.to, func(t *testing.T) {
			t.Parallel()
			from := httptest.NewRequestWithContext(t.Context(), http.MethodGet, tt.from, http.NoBody)
			to := httptest.NewRequestWithContext(t.Context(), http.MethodGet, tt.to, http.NoBody)

			err := sameOriginRedirects(nil)(to, []*http.Request{from})

			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("redirect %q to %q error mismatch (-want +got):\n%s", tt.from, tt.to, diff)
			}
		})
	}
}
