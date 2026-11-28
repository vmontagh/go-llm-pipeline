package httputil

import (
	"errors"
	"fmt"
	"net/http"
	"time"
)

// maxRedirects mirrors net/http's default redirect limit, which no longer
// applies once CheckRedirect is set.
const maxRedirects = 10

// ErrCrossOriginRedirect is returned when a server answers with a redirect to
// a different scheme, host or port. Such redirects are refused because
// net/http would forward custom credential headers and, for 307/308, the
// request body to the new origin.
var ErrCrossOriginRedirect = errors.New("httputil: refusing cross-origin redirect")

// ErrTooManyRedirects is returned after maxRedirects same-origin redirects.
var ErrTooManyRedirects = errors.New("httputil: too many redirects")

// SameOriginClient returns a copy of client, or a new client if it is nil,
// that only follows redirects to the origin of the first request. A positive
// timeout replaces the client's Timeout. A CheckRedirect already set on
// client still runs for same-origin redirects. The caller's client is never
// mutated.
func SameOriginClient(client *http.Client, timeout time.Duration) *http.Client {
	var c http.Client
	if client != nil {
		c = *client
	}
	if timeout > 0 {
		c.Timeout = timeout
	}
	c.CheckRedirect = sameOriginRedirects(c.CheckRedirect)
	return &c
}

// sameOriginRedirects wraps next with a policy that only follows redirects
// whose scheme and host:port match the first request, which also rules out
// HTTPS-to-HTTP downgrades.
func sameOriginRedirects(next func(*http.Request, []*http.Request) error) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		orig := via[0].URL
		if req.URL.Scheme != orig.Scheme || req.URL.Host != orig.Host {
			return fmt.Errorf("%w: %s://%s to %s://%s", ErrCrossOriginRedirect, orig.Scheme, orig.Host, req.URL.Scheme, req.URL.Host)
		}
		if next != nil {
			return next(req, via)
		}
		if len(via) >= maxRedirects {
			return fmt.Errorf("%w: stopped after %d", ErrTooManyRedirects, maxRedirects)
		}
		return nil
	}
}
