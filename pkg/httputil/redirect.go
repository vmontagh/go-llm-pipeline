package httputil

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
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
		// Snapshot the origin first: next can mutate both req.URL and via.
		want := origin(via[0].URL)
		if err := checkOrigin(want, req.URL); err != nil {
			return err
		}
		if len(via) >= maxRedirects {
			return fmt.Errorf("%w: stopped after %d", ErrTooManyRedirects, maxRedirects)
		}
		if next == nil {
			return nil
		}
		if err := next(req, via); err != nil {
			return err
		}
		// via[0] must stay intact too: later hops compare against it.
		if err := checkOrigin(want, via[0].URL); err != nil {
			return err
		}
		return checkOrigin(want, req.URL)
	}
}

func checkOrigin(want string, target *url.URL) error {
	if got := origin(target); got != want {
		return fmt.Errorf("%w: %s to %s", ErrCrossOriginRedirect, want, got)
	}
	return nil
}

// origin returns u's scheme, host and port in canonical form, with the
// scheme's default port filled in, so EXAMPLE.com and example.com:443
// compare equal over HTTPS. url.Parse already lower-cases the scheme.
func origin(u *url.URL) string {
	scheme := u.Scheme
	port := u.Port()
	if port == "" {
		port = defaultPorts[scheme]
	}
	return scheme + "://" + net.JoinHostPort(foldHost(u.Hostname()), port)
}

var defaultPorts = map[string]string{"http": "80", "https": "443"}

// foldHost lower-cases only ASCII letters, as DNS does. Unicode folding
// would merge distinct names (İ.example and i.example), and an IPv6 zone
// after '%' is case-sensitive, so it is kept verbatim.
func foldHost(host string) string {
	addr, zone, hasZone := strings.Cut(host, "%")
	addr = strings.Map(func(r rune) rune {
		if 'A' <= r && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, addr)
	if hasZone {
		return addr + "%" + zone
	}
	return addr
}
