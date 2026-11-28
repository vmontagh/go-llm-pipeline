package httputil

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"golang.org/x/net/idna"
)

// maxRedirects mirrors net/http's default redirect limit, which no longer
// applies once CheckRedirect is set.
const maxRedirects = 10

var (
	// ErrCrossOriginRedirect is returned when a server answers with a
	// redirect to a different scheme, host or port. Such redirects are
	// refused because net/http would forward custom credential headers and,
	// for 307/308, the request body to the new origin.
	ErrCrossOriginRedirect = errors.New("httputil: refusing cross-origin redirect")
	// ErrTooManyRedirects is returned after maxRedirects same-origin
	// redirects.
	ErrTooManyRedirects = errors.New("httputil: too many redirects")
)

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
		// net/http always passes the earlier requests, but fail closed if a
		// direct caller does not.
		if len(via) == 0 {
			return fmt.Errorf("%w: no original request", ErrCrossOriginRedirect)
		}
		// Save the origin first: next can mutate both req.URL and via.
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
		// Later hops compare against via[0], so it must be unchanged too.
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

const (
	schemeHTTP  = "http"
	schemeHTTPS = "https"
	// Default ports for schemeHTTP and schemeHTTPS (RFC 9110 §4.2).
	portHTTP  = "80"
	portHTTPS = "443"
)

// origin returns u's scheme, host and port in the form net/http dials, with
// the scheme's default port filled in, so EXAMPLE.com and example.com:443
// compare equal over HTTPS. url.Parse already lower-cases the scheme.
func origin(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = defaultPort(u.Scheme)
	}
	canonical := url.URL{Scheme: u.Scheme, Host: net.JoinHostPort(canonicalHost(u.Hostname()), port)}
	return canonical.String()
}

func defaultPort(scheme string) string {
	switch scheme {
	case schemeHTTP:
		return portHTTP
	case schemeHTTPS:
		return portHTTPS
	default:
		return ""
	}
}

// canonicalHost maps host the way net/http resolves it: IP literals via
// netip (keeping a case-sensitive IPv6 zone as is) and names via IDNA
// lookup rules. A host neither accepts is kept as is, so it only matches
// itself exactly.
func canonicalHost(host string) string {
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.String()
	}
	if ascii, err := idna.Lookup.ToASCII(host); err == nil {
		return ascii
	}
	return host
}
