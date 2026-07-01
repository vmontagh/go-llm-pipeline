package vcrpatch

import (
	"net/url"
	"strings"
)

// Matcher defines the contract to match a recorded request.
type Matcher interface {
	Match(req *InteractionReq) bool
}

type urlContainsMatcher struct {
	substr string
}

func (m urlContainsMatcher) Match(req *InteractionReq) bool {
	return strings.Contains(req.URL, m.substr)
}

// URLContains matches if the request URL contains the substring.
func URLContains(substr string) Matcher {
	return urlContainsMatcher{substr: substr}
}

type methodEqualsMatcher struct {
	method string
}

func (m methodEqualsMatcher) Match(req *InteractionReq) bool {
	return strings.EqualFold(req.Method, m.method)
}

// MethodEquals matches if the request method equals (case-insensitive) the method.
func MethodEquals(method string) Matcher {
	return methodEqualsMatcher{method: method}
}

type bodyContainsMatcher struct {
	substr string
}

func (m bodyContainsMatcher) Match(req *InteractionReq) bool {
	return strings.Contains(req.Body, m.substr)
}

// BodyContains matches if the request body contains the substring.
func BodyContains(substr string) Matcher {
	return bodyContainsMatcher{substr: substr}
}

type headerEqualsMatcher struct {
	key, value string
}

func (m headerEqualsMatcher) Match(req *InteractionReq) bool {
	if req.Headers == nil {
		return false
	}
	vals, ok := req.Headers[m.key]
	if !ok {
		for k, v := range req.Headers {
			if strings.EqualFold(k, m.key) {
				vals = v
				ok = true
				break
			}
		}
	}
	if !ok {
		return false
	}
	for _, val := range vals {
		if val == m.value {
			return true
		}
	}
	return false
}

// HeaderEquals matches if the request header contains the key-value pair.
func HeaderEquals(key, value string) Matcher {
	return headerEqualsMatcher{key: key, value: value}
}

type urlQueryParamMatcher struct {
	key, value string
}

func (m urlQueryParamMatcher) Match(req *InteractionReq) bool {
	u, err := url.Parse(req.URL)
	if err != nil {
		return false
	}
	for _, val := range u.Query()[m.key] {
		if val == m.value {
			return true
		}
	}
	return false
}

// URLQueryParam matches if the request URL contains a query param matching key=value.
func URLQueryParam(key, value string) Matcher {
	return urlQueryParamMatcher{key: key, value: value}
}

type nthMatchMatcher struct {
	inner Matcher
	n     int
	count int
}

func (m *nthMatchMatcher) Match(req *InteractionReq) bool {
	if m.inner.Match(req) {
		m.count++
		return m.count == m.n
	}
	return false
}

// NthMatch matches only on the Nth occurrence (1-based index) of the inner matcher.
// Note: The returned Matcher is stateful (it keeps track of matches across calls)
// and is not safe for concurrent use. If a Scenario or Patch is reused, or executed
// concurrently, a fresh Matcher must be constructed.
func NthMatch(inner Matcher, n int) Matcher {
	return &nthMatchMatcher{inner: inner, n: n}
}

type andMatcher struct {
	matchers []Matcher
}

func (m andMatcher) Match(req *InteractionReq) bool {
	for _, matcher := range m.matchers {
		if !matcher.Match(req) {
			return false
		}
	}
	return true
}

// And matches if all matchers match.
func And(matchers ...Matcher) Matcher {
	return andMatcher{matchers: matchers}
}

type orMatcher struct {
	matchers []Matcher
}

func (m orMatcher) Match(req *InteractionReq) bool {
	for _, matcher := range m.matchers {
		if matcher.Match(req) {
			return true
		}
	}
	return false
}

// Or matches if any of the matchers match.
func Or(matchers ...Matcher) Matcher {
	return orMatcher{matchers: matchers}
}
