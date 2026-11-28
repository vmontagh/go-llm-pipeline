// Package httputil contains small, protocol-level HTTP helpers that
// are independent of any business logic in this repository.
//
// These helpers are intentionally placed outside of pkg/analyzer
// so that LLM provider implementations (OpenAI, Gemini, Ollama, …) can
// consume generic HTTP utilities without taking a dependency on the
// analyzer "core" (interfaces, key pool, retry policy, error
// classification). Anything that is purely a function of the HTTP
// protocol — header parsing, status-code helpers, etc. — belongs here.
package httputil

import (
	"net/http"
	"strconv"
	"time"
)

// RetryAfterHeader is the name of the HTTP header that carries a server's
// suggested retry delay; see ParseRetryAfter.
const RetryAfterHeader = "Retry-After"

// ParseRetryAfter interprets the value of an HTTP `Retry-After` header
// per RFC 7231 §7.1.3. The header may be either:
//
//   - an integer number of seconds (delta-seconds), or
//   - an HTTP-date indicating the absolute instant at which the
//     request may be retried.
//
// ParseRetryAfter returns the duration to wait before retrying. Any
// parse failure, an empty header, a non-positive delta-seconds, or an
// HTTP-date already in the past all yield 0 — callers should treat 0
// as "no server-suggested delay" and fall back to their configured
// cooldown / backoff floor.
func ParseRetryAfter(h string) time.Duration {
	return parseRetryAfter(h, time.Now())
}

// parseRetryAfter is the clock-injected implementation of ParseRetryAfter.
// HTTP-date values are measured relative to now instead of the wall clock so
// callers (and tests) can supply a fixed reference instant.
func parseRetryAfter(h string, now time.Time) time.Duration {
	if h == "" {
		return 0
	}
	if secs, err := strconv.Atoi(h); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		d := t.Sub(now)
		if d < 0 {
			return 0
		}
		return d
	}
	return 0
}
