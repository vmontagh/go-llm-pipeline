package provider

import "errors"

// Sentinel errors returned by provider implementations and re-exported
// by the analyzer package. Callers use errors.Is to detect them without
// inspecting HTTP status codes.
var (
	// ErrMissingAPIKey is returned when no API credential is configured.
	ErrMissingAPIKey = errors.New("missing API key")
	// ErrAllKeysCoolingDown is returned by KeyPool.Acquire when every
	// key in the pool is currently parked in cooldown.
	ErrAllKeysCoolingDown = errors.New("all API keys are in cooldown")
	// ErrQuotaExceeded is returned when the upstream API quota is reached.
	ErrQuotaExceeded = errors.New("quota exceeded")
	// ErrRateLimitExceeded is returned when the upstream API rate limit
	// is reached.
	ErrRateLimitExceeded = errors.New("rate limit exceeded")
	// ErrEmptyCandidate is returned when the upstream LLM returns no
	// candidates.
	ErrEmptyCandidate = errors.New("empty candidate returned")
	// ErrInvalidResponsePart is returned when the upstream LLM returns
	// an unexpected response part.
	ErrInvalidResponsePart = errors.New("invalid response part returned")
	// ErrUnexpectedStatus is returned when the upstream API returns an
	// unexpected status code.
	ErrUnexpectedStatus = errors.New("unexpected status code returned")
)
