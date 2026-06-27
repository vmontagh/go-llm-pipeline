// Package gemini implements the transport half of the analyzer Template
// Method using Google's Gemini models.
package gemini

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/cenkalti/backoff/v4"
	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/googleapi"

	provpkg "github.com/vmontagh/go-llm-pipeline/pkg/provider"
	"github.com/vmontagh/go-llm-pipeline/pkg/trace"
)

// DefaultModel is the Gemini model used when NewProvider is invoked
const DefaultModel = "gemini-3.5-flash"

// providerLabel is the human-readable provider identifier
const providerLabel = "gemini"

// traceRegionName is the trace region the Template opens around Analyze
const traceRegionName = "Analyze_Gemini"

// callRegionName is the trace region opened around each individual Call
const callRegionName = "Call_Gemini"

// jsonMIMEType is the MIME type set on GenerationConfig.ResponseMIMEType
const jsonMIMEType = "application/json"

// Tracer is the tracing interface required by the Gemini transport.
type Tracer interface {
	StartRegion(ctx context.Context, name string) interface{ End() }
}

// Logger is the minimal logging interface used by the Gemini transport
type Logger interface {
	Warnf(format string, v ...any)
}

type noOpLogger struct{}

func (noOpLogger) Warnf(string, ...any) {}

type preflighter interface {
	Preflight(ctx context.Context) error
}

type provider interface {
	Name() string
	Model() string
	ProviderLabel() string
	TraceRegionName() string
	Keys() *provpkg.KeyPool[*genai.Client]
	ClassifyError(provpkg.ClassifyInput, provpkg.RetryContext) provpkg.ErrorTier
	EnrichError(provpkg.ClassifyInput, error, []byte) error
	Call(ctx context.Context, lease *provpkg.Lease[*genai.Client], prompt string, imageData []byte) provpkg.CallResult
	preflighter
}

// transport is the Gemini implementation of provpkg.Provider[*genai.Client].
type transport struct {
	model          string
	clients        *provpkg.KeyPool[*genai.Client]
	responseSchema *genai.Schema
	tracer         Tracer
	logger         Logger
}

// Name implements provpkg.Provider.
func (t *transport) Name() string { return t.model }

// Model implements provpkg.Provider.
func (t *transport) Model() string { return t.model }

// ProviderLabel implements provpkg.Provider.
func (t *transport) ProviderLabel() string { return providerLabel }

// TraceRegionName implements provpkg.Provider.
func (t *transport) TraceRegionName() string { return traceRegionName }

// Keys implements provpkg.Provider.
func (t *transport) Keys() *provpkg.KeyPool[*genai.Client] { return t.clients }

// ClassifyError maps a Gemini provider error to a provpkg.ErrorTier.
func (t *transport) ClassifyError(in provpkg.ClassifyInput, _ provpkg.RetryContext) provpkg.ErrorTier {
	var gerr *googleapi.Error
	if !errors.As(in.Err, &gerr) {
		return provpkg.TierTerminal
	}
	switch gerr.Code {
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

// Call performs a single GenerateContent attempt against Gemini
func (t *transport) Call(ctx context.Context, lease *provpkg.Lease[*genai.Client], prompt string, imageData []byte) provpkg.CallResult {
	defer t.tracer.StartRegion(ctx, callRegionName).End()
	parts := []genai.Part{genai.Text(prompt)}
	if len(imageData) > 0 {
		parts = append(parts, genai.ImageData("jpeg", imageData))
	}

	client := lease.Value
	model := client.GenerativeModel(t.model)
	if t.responseSchema != nil {
		model.GenerationConfig = genai.GenerationConfig{
			ResponseMIMEType: jsonMIMEType,
			ResponseSchema:   t.responseSchema,
		}
	}
	resp, err := model.GenerateContent(ctx, parts...)
	if err != nil {
		return provpkg.CallResult{Outcome: provpkg.OutcomeProviderError, Err: err}
	}
	if len(resp.Candidates) == 0 {
		return provpkg.CallResult{
			Outcome: provpkg.OutcomeTerminal,
			Err:     backoff.Permanent(fmt.Errorf("%w: %+v", provpkg.ErrEmptyCandidate, resp)),
		}
	}
	cand := resp.Candidates[0]
	if len(cand.Content.Parts) == 0 {
		return provpkg.CallResult{
			Outcome: provpkg.OutcomeTerminal,
			Err:     backoff.Permanent(fmt.Errorf("%w: %+v", provpkg.ErrEmptyCandidate, cand)),
		}
	}
	part := cand.Content.Parts[0]
	text, ok := part.(genai.Text)
	if !ok {
		return provpkg.CallResult{
			Outcome: provpkg.OutcomeTerminal,
			Err:     backoff.Permanent(fmt.Errorf("%w: %T", provpkg.ErrInvalidResponsePart, part)),
		}
	}
	return provpkg.CallResult{Outcome: provpkg.OutcomeSuccess, Text: string(text)}
}

// ProviderOption configures a Gemini provider at construction time.
type ProviderOption func(*providerConfig)

type providerConfig struct {
	clock          provpkg.PoolClock
	responseSchema *genai.Schema
	tracer         Tracer
	logger         Logger
}

func withClock(clk provpkg.PoolClock) ProviderOption {
	return func(c *providerConfig) { c.clock = clk }
}

// WithTracer sets the tracer used by the transport
func WithTracer(tr Tracer) ProviderOption {
	return func(c *providerConfig) { c.tracer = tr }
}

// WithResponseSchema configures Gemini's GenerationConfig with a JSON-schema
func WithResponseSchema(schema map[string]any) ProviderOption {
	converted := convertJSONSchema(schema)
	return func(c *providerConfig) { c.responseSchema = converted }
}

// WithLogger sets the logger used by the transport
func WithLogger(l Logger) ProviderOption {
	return func(c *providerConfig) { c.logger = l }
}

// Preflight is a no-op for Gemini
func (t *transport) Preflight(_ context.Context) error {
	t.logger.Warnf("gemini preflight: not configured (skipping model-catalog / vision-support check)")
	return nil
}

// NewProvider builds a Gemini provpkg.Provider configured with the given model and credential pool.
func NewProvider(model string, clients []*genai.Client, opts ...ProviderOption) provpkg.Provider[*genai.Client] {
	cfg := providerConfig{}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	if model == "" {
		model = DefaultModel
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
		lg = noOpLogger{}
	}
	return &transport{
		model:          model,
		clients:        provpkg.NewKeyPoolWithClock(clients, clock),
		responseSchema: cfg.responseSchema,
		tracer:         tr,
		logger:         lg,
	}
}
