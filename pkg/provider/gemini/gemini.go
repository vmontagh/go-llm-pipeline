// Package gemini implements the transport half of the analyzer Template
// Method using Google's Gemini models. It talks to the Gemini REST API
// directly over net/http, so it pulls in no Google SDK dependencies.
//
// # Migrating from v0.4
//
// Up to v0.4.0 the provider wrapped the deprecated
// github.com/google/generative-ai-go SDK and leased *genai.Client values.
// It now leases API keys:
//
//	// v0.4
//	gemini.NewProvider(model, []*genai.Client{client}, opts...)
//	// v0.5
//	gemini.NewProvider(apiKeys, gemini.WithModel(model), opts...)
//
// Callers that build analyzers use analyzer.NewBuilder[string, R] instead
// of analyzer.NewBuilder[*genai.Client, R]. Replace option.WithEndpoint
// with WithBaseURL, whose value includes the API version path (see
// DefaultBaseURL), and option.WithHTTPClient with WithHTTPClient. API
// failures are *APIError values instead of *googleapi.Error.
package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"

	"github.com/vmontagh/go-llm-pipeline/pkg/httputil"
	provpkg "github.com/vmontagh/go-llm-pipeline/pkg/provider"
	"github.com/vmontagh/go-llm-pipeline/pkg/trace"
)

// Default model identifier and base URL used by the Gemini provider.
// DefaultBaseURL includes the API version, mirroring the OpenAI provider.
const (
	DefaultModel   = "gemini-3.5-flash"
	DefaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"
)

// providerLabel is the human-readable provider identifier
const providerLabel = "gemini"

// traceRegionName is the trace region the Template opens around Analyze
const traceRegionName = "Analyze_Gemini"

// callRegionName is the trace region opened around each individual Call
const callRegionName = "Call_Gemini"

// Request constants for the generateContent REST endpoint.
const (
	modelResourcePrefix   = "models/"
	generateContentMethod = ":generateContent"
	authHeaderName        = "X-Goog-Api-Key"
	jsonMIMEType          = "application/json"
	imageMIMEType         = "image/jpeg"
	userRole              = "user"
)

// maxRedirects mirrors net/http's default redirect limit, which no longer
// applies once CheckRedirect is set.
const maxRedirects = 10

// ErrCrossOriginRedirect is returned when the API answers with a redirect to
// a different scheme, host or port. Such redirects are refused because
// net/http would forward the X-Goog-Api-Key header and, for 307/308, the
// prompt and image body to the new origin.
var ErrCrossOriginRedirect = errors.New("gemini: refusing cross-origin redirect")

// ErrTooManyRedirects is returned after maxRedirects same-origin redirects.
var ErrTooManyRedirects = errors.New("gemini: too many redirects")

// Tracer is the tracing interface required by the Gemini transport.
type Tracer interface {
	StartRegion(ctx context.Context, name string) interface{ End() }
}

// APIError is a non-2xx response from the Gemini API. Callers can recover it
// with errors.As to inspect the HTTP status and Google's status string.
type APIError struct {
	// StatusCode is the HTTP status code of the response.
	StatusCode int
	// Status is Google's canonical status name, e.g. "RESOURCE_EXHAUSTED".
	Status string
	// Message is the human-readable error message returned by the API.
	Message string
}

// Error implements error.
func (e *APIError) Error() string {
	return fmt.Sprintf("gemini api: HTTP %d %s: %s", e.StatusCode, e.Status, e.Message)
}

// newAPIError builds an APIError from a non-2xx response body, falling back
// to the HTTP status text when the body is not a Google error envelope.
func newAPIError(statusCode int, body []byte) *APIError {
	var env struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	apiErr := &APIError{StatusCode: statusCode, Message: http.StatusText(statusCode)}
	if err := json.Unmarshal(body, &env); err != nil {
		return apiErr
	}
	apiErr.Status = env.Error.Status
	if env.Error.Message != "" {
		apiErr.Message = env.Error.Message
	}
	return apiErr
}

type generateContentRequest struct {
	Contents         []content         `json:"contents"`
	GenerationConfig *generationConfig `json:"generationConfig,omitempty"`
}

type content struct {
	Role  string `json:"role,omitempty"`
	Parts []part `json:"parts"`
}

// part is one piece of content. Text is a pointer so that an empty text part
// can be told apart from a non-text part (e.g. inline data).
type part struct {
	Text       *string `json:"text,omitempty"`
	InlineData *blob   `json:"inlineData,omitempty"`
}

// blob is inline binary data; encoding/json base64-encodes Data, which is
// the encoding the API expects.
type blob struct {
	MIMEType string `json:"mimeType"`
	Data     []byte `json:"data"`
}

type generationConfig struct {
	ResponseMIMEType string  `json:"responseMimeType"`
	ResponseSchema   *schema `json:"responseSchema"`
}

type generateContentResponse struct {
	Candidates     []candidate     `json:"candidates"`
	PromptFeedback *promptFeedback `json:"promptFeedback,omitempty"`
}

type candidate struct {
	Content      content `json:"content"`
	FinishReason string  `json:"finishReason,omitempty"`
}

type promptFeedback struct {
	BlockReason string `json:"blockReason,omitempty"`
}

// transport is the Gemini implementation of provpkg.Provider[string].
type transport struct {
	model          string
	baseURL        string
	apiKeys        *provpkg.KeyPool[string]
	responseSchema *schema
	client         *http.Client
	tracer         Tracer
	logger         *slog.Logger
}

// Name implements provpkg.Provider.
func (t *transport) Name() string { return "gemini/" + t.model }

// Model implements provpkg.Provider.
func (t *transport) Model() string { return t.model }

// ProviderLabel implements provpkg.Provider.
func (t *transport) ProviderLabel() string { return providerLabel }

// TraceRegionName implements provpkg.Provider.
func (t *transport) TraceRegionName() string { return traceRegionName }

// Keys implements provpkg.Provider.
func (t *transport) Keys() *provpkg.KeyPool[string] { return t.apiKeys }

// ClassifyError maps the HTTP status of a failed Gemini call to a
// provpkg.ErrorTier. Failures without a response (StatusCode 0) are terminal.
func (t *transport) ClassifyError(in provpkg.ClassifyInput, _ provpkg.RetryContext) provpkg.ErrorTier {
	switch in.StatusCode {
	case http.StatusTooManyRequests:
		return provpkg.TierLongTransientInfinite
	case http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return provpkg.TierShortTransient
	default:
		return provpkg.TierTerminal
	}
}

// EnrichError wraps 429-class errors and 5xx errors with sentinels
func (t *transport) EnrichError(tier provpkg.ErrorTier, rawErr error, _ []byte) error {
	switch tier {
	case provpkg.TierLongTransientInfinite:
		return fmt.Errorf("%w: %w", provpkg.ErrRateLimitExceeded, rawErr)
	case provpkg.TierShortTransient:
		return fmt.Errorf("%w: %w", provpkg.ErrUnexpectedStatus, rawErr)
	default:
		return rawErr
	}
}

// Call performs a single generateContent POST against Gemini.
func (t *transport) Call(ctx context.Context, lease *provpkg.Lease[string], prompt string, imageData []byte) provpkg.CallResult {
	defer t.tracer.StartRegion(ctx, callRegionName).End()
	reqB, err := json.Marshal(t.buildRequest(prompt, imageData))
	if err != nil {
		return terminal(fmt.Errorf("marshaling gemini request: %w", err))
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint(), bytes.NewReader(reqB))
	if err != nil {
		return terminal(fmt.Errorf("building gemini request: %w", err))
	}
	httpReq.Header.Set(authHeaderName, lease.Value)
	httpReq.Header.Set("Content-Type", jsonMIMEType)

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return provpkg.CallResult{Outcome: provpkg.OutcomeProviderError, Err: fmt.Errorf("calling gemini: %w", err)}
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.logger.Warn("close error", "err", cerr)
		}
	}()

	// Parsed before reading the body so a failed read on a 429/503 still
	// honors the server's cooldown.
	retryAfter := httputil.ParseRetryAfter(resp.Header.Get("Retry-After"))
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return provpkg.CallResult{
			Outcome:    provpkg.OutcomeProviderError,
			Err:        fmt.Errorf("reading gemini response: %w", err),
			StatusCode: resp.StatusCode,
			RetryAfter: retryAfter,
		}
	}
	if resp.StatusCode != http.StatusOK {
		return provpkg.CallResult{
			Outcome:    provpkg.OutcomeProviderError,
			Err:        newAPIError(resp.StatusCode, body),
			StatusCode: resp.StatusCode,
			Body:       body,
			RetryAfter: retryAfter,
		}
	}
	return parseResponse(body)
}

// parseResponse extracts the first text part of the first candidate.
func parseResponse(body []byte) provpkg.CallResult {
	var resp generateContentResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return terminal(fmt.Errorf("%w: unmarshaling gemini response: %w", provpkg.ErrInvalidResponsePart, err))
	}
	if len(resp.Candidates) == 0 {
		var blockReason string
		if resp.PromptFeedback != nil {
			blockReason = resp.PromptFeedback.BlockReason
		}
		return terminal(fmt.Errorf("%w: no candidates (block reason %q)", provpkg.ErrEmptyCandidate, blockReason))
	}
	cand := resp.Candidates[0]
	if len(cand.Content.Parts) == 0 {
		return terminal(fmt.Errorf("%w: no parts (finish reason %q)", provpkg.ErrEmptyCandidate, cand.FinishReason))
	}
	text := cand.Content.Parts[0].Text
	if text == nil {
		return terminal(fmt.Errorf("%w: first part is not text", provpkg.ErrInvalidResponsePart))
	}
	return provpkg.CallResult{Outcome: provpkg.OutcomeSuccess, Text: *text}
}

func terminal(err error) provpkg.CallResult {
	return provpkg.CallResult{Outcome: provpkg.OutcomeTerminal, Err: backoff.Permanent(err)}
}

// endpoint returns the generateContent URL. Like the Google SDKs, a model
// name without a resource prefix (e.g. "tunedModels/x") is a base model.
func (t *transport) endpoint() string {
	name := t.model
	if !strings.Contains(name, "/") {
		name = modelResourcePrefix + name
	}
	return t.baseURL + "/" + name + generateContentMethod
}

func (t *transport) buildRequest(prompt string, imageData []byte) generateContentRequest {
	parts := []part{{Text: &prompt}}
	if len(imageData) > 0 {
		parts = append(parts, part{InlineData: &blob{MIMEType: imageMIMEType, Data: imageData}})
	}
	req := generateContentRequest{Contents: []content{{Role: userRole, Parts: parts}}}
	if t.responseSchema != nil {
		req.GenerationConfig = &generationConfig{
			ResponseMIMEType: jsonMIMEType,
			ResponseSchema:   t.responseSchema,
		}
	}
	return req
}

// ProviderOption configures a Gemini provider at construction time.
type ProviderOption func(*providerConfig)

type providerConfig struct {
	model          string
	baseURL        string
	clock          provpkg.PoolClock
	responseSchema *schema
	client         *http.Client
	httpTimeout    time.Duration
	tracer         Tracer
	logger         *slog.Logger
}

func withClock(clk provpkg.PoolClock) ProviderOption {
	return func(c *providerConfig) { c.clock = clk }
}

// WithModel sets the Gemini model. Empty values are ignored.
func WithModel(model string) ProviderOption {
	return func(c *providerConfig) {
		if model != "" {
			c.model = model
		}
	}
}

// WithBaseURL overrides the API base URL, including the version path
// (default DefaultBaseURL). Empty values are ignored.
func WithBaseURL(u string) ProviderOption {
	return func(c *providerConfig) {
		if u != "" {
			c.baseURL = strings.TrimSuffix(u, "/")
		}
	}
}

// WithHTTPClient injects the *http.Client used for API calls, e.g. an
// llmvcr recorder's client for cassette record/replay.
func WithHTTPClient(client *http.Client) ProviderOption {
	return func(c *providerConfig) { c.client = client }
}

// WithHTTPClientTimeout sets the per-request timeout. The client passed to
// WithHTTPClient is copied, not modified.
func WithHTTPClientTimeout(d time.Duration) ProviderOption {
	return func(c *providerConfig) { c.httpTimeout = d }
}

// WithTracer sets the tracer used by the transport
func WithTracer(tr Tracer) ProviderOption {
	return func(c *providerConfig) { c.tracer = tr }
}

// WithResponseSchema configures Gemini's GenerationConfig with a JSON-schema
func WithResponseSchema(s map[string]any) ProviderOption {
	converted := convertJSONSchema(s)
	return func(c *providerConfig) { c.responseSchema = converted }
}

// WithLogger sets the *slog.Logger used by the transport.
func WithLogger(l *slog.Logger) ProviderOption {
	return func(c *providerConfig) { c.logger = l }
}

// Preflight is a no-op for Gemini
func (t *transport) Preflight(_ context.Context) error {
	t.logger.Warn("gemini preflight: not configured (skipping model-catalog / vision-support check)")
	return nil
}

// NewProvider builds a Gemini provpkg.Provider that rotates over apiKeys.
func NewProvider(apiKeys []string, opts ...ProviderOption) provpkg.Provider[string] {
	cfg := providerConfig{model: DefaultModel, baseURL: DefaultBaseURL}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	clock := cfg.clock
	if clock == nil {
		clock = provpkg.RealPoolClock{}
	}
	tr := cfg.tracer
	if tr == nil {
		tr = trace.NewNoOpTracer()
	}
	lg := cfg.logger
	if lg == nil {
		lg = slog.New(slog.DiscardHandler)
	}
	return &transport{
		model:          cfg.model,
		baseURL:        cfg.baseURL,
		apiKeys:        provpkg.NewKeyPoolWithClock(apiKeys, clock),
		responseSchema: cfg.responseSchema,
		client:         httpClient(cfg.client, cfg.httpTimeout),
		tracer:         tr,
		logger:         lg,
	}
}

// httpClient returns the client to use. It works on a copy so a
// caller-supplied client is never mutated, applies timeout, and restricts
// redirects to the original origin before deferring to the caller's
// CheckRedirect.
func httpClient(client *http.Client, timeout time.Duration) *http.Client {
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
