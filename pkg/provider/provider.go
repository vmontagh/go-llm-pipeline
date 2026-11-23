package provider

import (
	"context"
	"time"
)

// CallOutcome classifies what the analyzer Template should do after
// Provider.Call returns.
type CallOutcome int

const (
	// OutcomeSuccess: Call produced a usable Text response.
	OutcomeSuccess CallOutcome = iota
	// OutcomeProviderError: Call observed a transport- or API-level
	// failure that should flow through ClassifyError + the Template's
	// handleProviderError path. Do NOT wrap with backoff.Permanent.
	OutcomeProviderError
	// OutcomeTerminal: Call produced an error that is already
	// non-retryable. The transport MUST wrap the error with
	// backoff.Permanent before returning this outcome.
	OutcomeTerminal
)

// CallResult is what a Provider returns from a single transport attempt.
type CallResult struct {
	// Text is the raw model output to be parsed; set on OutcomeSuccess.
	Text string
	// Err carries the provider error for OutcomeProviderError /
	// OutcomeTerminal. Must be nil for OutcomeSuccess.
	Err error
	// Outcome dispatches the Template's post-call handling.
	Outcome CallOutcome
	// StatusCode is the HTTP status from the upstream response, or 0
	// when the failure occurred before a response was received.
	StatusCode int
	// Body is the raw response body, forwarded to ClassifyError.
	Body []byte
	// RetryAfter is the parsed Retry-After hint (0 when absent).
	RetryAfter time.Duration
	// ToolCalls are the tools the model asks to run; see ToolCaller.
	ToolCalls []ToolCall
	// Parts is the reply's text and calls in their original order, with
	// their Signatures, for a ToolCaller whose provider needs the reply sent
	// back intact. The tool loop then replays it instead of Text followed by
	// ToolCalls.
	Parts []Part
}

// Provider is the per-backend hook surface the analyzer Template
// depends on.
type Provider[T any] interface {
	// Name returns the analyzer identifier surfaced to callers.
	Name() string
	// Model returns the model identifier rendered in retry logs.
	Model() string
	// ProviderLabel returns the human-readable provider name (e.g.
	// "openai", "gemini", "ollama").
	ProviderLabel() string
	// TraceRegionName is the label opened around Analyze.
	TraceRegionName() string
	// Keys returns the credential pool.
	Keys() *KeyPool[T]
	// ClassifyError maps a provider-specific error (with optional HTTP
	// metadata) to an ErrorTier.
	ClassifyError(ClassifyInput, RetryContext) ErrorTier
	// EnrichError optionally wraps the raw provider error with a
	// package sentinel so callers can use errors.Is to detect quota /
	// rate-limit without inspecting HTTP status.
	EnrichError(tier ErrorTier, rawErr error, body []byte) error
	// Call performs a single transport attempt against the upstream
	// using the credential carried by lease. It MUST NOT call
	// lease.Release / lease.MarkCooldown.
	Call(ctx context.Context, lease *Lease[T], prompt string, imageData []byte) CallResult
}

// DefaultEnrichError is the no-op EnrichError implementation. Providers
// without sentinel-wrapping needs can return this from their EnrichError
// method body.
func DefaultEnrichError(_ ErrorTier, rawErr error, _ []byte) error { return rawErr }
