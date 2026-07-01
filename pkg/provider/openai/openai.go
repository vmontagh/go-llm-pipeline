// Package openai implements the transport half of the analyzer Template
// Method for the OpenAI chat-completions API.
package openai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/cenkalti/backoff/v4"

	"github.com/vmontagh/go-llm-pipeline/pkg/httputil"
	provpkg "github.com/vmontagh/go-llm-pipeline/pkg/provider"
	"github.com/vmontagh/go-llm-pipeline/pkg/trace"
)

// TypeJSONSchema is the OpenAI response_format type for JSON schema mode.
const TypeJSONSchema = "json_schema"

// Default model identifier and base URL used by the OpenAI provpkg.
const (
	DefaultModel   = "gpt-4o-mini"
	DefaultBaseURL = "https://api.openai.com/v1"
)

// providerLabel is the human-readable provider identifier emitted to
// ProviderErrorInfo.Provider and the LLM_STARTED publish message.
const providerLabel = "openai"

// traceRegionName is the trace region the Template opens around Analyze
// for this provpkg.
const traceRegionName = "Analyze_OpenAI"

// callRegionName is the trace region opened around each individual Call
// invocation by the transport.
const callRegionName = "Call_OpenAI"

// Tracer is the tracing interface required by the OpenAI transport.
type Tracer interface {
	StartRegion(ctx context.Context, name string) interface{ End() }
}

// API path and header constants used when building requests.
const (
	chatCompletionsPath      = "/chat/completions"
	authorizationHeaderValue = "Bearer "
	imageDataURLFormat       = "data:image/jpeg;base64,%s"
	insufficientQuotaType    = "insufficient_quota"
)

// ChatGPTRequest represents a request to the OpenAI chat completions API.
type ChatGPTRequest struct {
	Model          string             `json:"model"`
	Messages       []RequestMessage   `json:"messages"`
	MaxTokens      int                `json:"max_tokens"`
	ResponseFormat *ResponseFormatDef `json:"response_format,omitempty"`
}

// ResponseFormatDef declares the OpenAI `response_format` parameter
type ResponseFormatDef struct {
	Type       string         `json:"type"`
	JSONSchema *JSONSchemaDef `json:"json_schema,omitempty"`
}

// JSONSchemaDef wraps a JSON Schema
type JSONSchemaDef struct {
	Name   string `json:"name"`
	Strict bool   `json:"strict"`
	Schema any    `json:"schema"`
}

// ChatGPTResponse represents the response from the OpenAI chat completions API.
type ChatGPTResponse struct {
	ID                string   `json:"id"`
	Object            string   `json:"object"`
	Created           int64    `json:"created"`
	Model             string   `json:"model"`
	Choices           []Choice `json:"choices"`
	Usage             Usage    `json:"usage"`
	SystemFingerprint string   `json:"system_fingerprint"`
}

// Choice represents a single completion choice returned by the API.
type Choice struct {
	Index        int             `json:"index"`
	Message      ResponseMessage `json:"message"`
	Logprobs     *interface{}    `json:"logprobs"`
	FinishReason string          `json:"finish_reason"`
}

// ResponseMessage represents a message within a chat completion response.
type ResponseMessage struct {
	Role    string  `json:"role"`
	Content string  `json:"content"`
	Refusal *string `json:"refusal"`
}

// RequestMessage represents a single message in a chat completion request.
type RequestMessage struct {
	Role    string    `json:"role"`
	Content []Content `json:"content"`
	Refusal *string   `json:"refusal"`
}

// Content represents a part of the message content, such as text or an image URL.
type Content struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

// ImageURL holds the URL of an image included in a message content.
type ImageURL struct {
	URL string `json:"url"`
}

// Usage provides statistics about token usage for a completion request.
type Usage struct {
	PromptTokens            int           `json:"prompt_tokens"`
	CompletionTokens        int           `json:"completion_tokens"`
	TotalTokens             int           `json:"total_tokens"`
	PromptTokensDetails     *TokenDetails `json:"prompt_tokens_details"`
	CompletionTokensDetails *TokenDetails `json:"completion_tokens_details"`
}

// TokenDetails provides granular information about token usage.
type TokenDetails struct {
	CachedTokens             int `json:"cached_tokens"`
	AudioTokens              int `json:"audio_tokens"`
	ReasoningTokens          int `json:"reasoning_tokens,omitempty"`
	AcceptedPredictionTokens int `json:"accepted_prediction_tokens,omitempty"`
	RejectedPredictionTokens int `json:"rejected_prediction_tokens,omitempty"`
}

// VisionResponse represents the structured data returned by a vision model.
type VisionResponse struct {
	Heading string   `json:"heading"`
	Tags    []string `json:"tags"`
}

// APIError represents an error returned by the OpenAI API.
type APIError struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

// transport is the OpenAI implementation of provpkg.Provider[string].
type transport struct {
	apiKeys            *provpkg.KeyPool[string]
	model              string
	baseURL            string
	organization       string
	responseSchema     any
	responseSchemaName string
	client             *http.Client
	logger             *slog.Logger
	tracer             Tracer
}

// Name implements provpkg.Provider.
func (t *transport) Name() string { return "openai/" + t.model }

// Model implements provpkg.Provider.
func (t *transport) Model() string { return t.model }

// ProviderLabel implements provpkg.Provider.
func (t *transport) ProviderLabel() string { return providerLabel }

// TraceRegionName implements provpkg.Provider.
func (t *transport) TraceRegionName() string { return traceRegionName }

// Keys implements provpkg.Provider.
func (t *transport) Keys() *provpkg.KeyPool[string] { return t.apiKeys }

// ClassifyError maps an OpenAI provider error to an ErrorTier.
func (t *transport) ClassifyError(in provpkg.ClassifyInput, _ provpkg.RetryContext) provpkg.ErrorTier {
	switch {
	case in.StatusCode == 0 && in.Err != nil:
		return provpkg.TierShortTransient
	case in.StatusCode == 0:
		return provpkg.TierTerminal
	case in.StatusCode == http.StatusTooManyRequests:
		return t.classify429(in.Body)
	case in.StatusCode >= http.StatusInternalServerError:
		return provpkg.TierShortTransient
	default:
		return provpkg.TierTerminal
	}
}

// classify429 inspects the API error body
func (t *transport) classify429(body []byte) provpkg.ErrorTier {
	var apiErr APIError
	if err := json.Unmarshal(body, &apiErr); err != nil {
		t.logger.Debug("openai: malformed 429 body, defaulting to rate-limit tier", "err", err)
		return provpkg.TierShortRateLimit
	}
	if apiErr.Error.Type == insufficientQuotaType {
		return provpkg.TierLongTransientBounded
	}
	return provpkg.TierShortRateLimit
}

// EnrichError wraps the raw provider error
func (t *transport) EnrichError(tier provpkg.ErrorTier, rawErr error, _ []byte) error {
	switch tier {
	case provpkg.TierLongTransientBounded:
		return fmt.Errorf("%w: %w", provpkg.ErrQuotaExceeded, rawErr)
	case provpkg.TierShortRateLimit:
		return fmt.Errorf("%w: %w", provpkg.ErrRateLimitExceeded, rawErr)
	default:
		return rawErr
	}
}

// Call performs a single chat-completions POST against OpenAI
func (t *transport) Call(ctx context.Context, lease *provpkg.Lease[string], prompt string, imageData []byte) provpkg.CallResult {
	defer t.tracer.StartRegion(ctx, callRegionName).End()
	req := buildRequest(t.model, prompt, imageData, t.responseSchemaName, t.responseSchema)
	reqB, err := json.Marshal(req)
	if err != nil {
		return provpkg.CallResult{
			Outcome: provpkg.OutcomeTerminal,
			Err:     backoff.Permanent(fmt.Errorf("marshaling vision request: %w", err)),
		}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+chatCompletionsPath, bytes.NewReader(reqB))
	if err != nil {
		return provpkg.CallResult{Outcome: provpkg.OutcomeTerminal, Err: backoff.Permanent(err)}
	}

	httpReq.Header.Add("Authorization", authorizationHeaderValue+lease.Value)
	httpReq.Header.Add("Content-Type", "application/json")
	if t.organization != "" {
		httpReq.Header.Add("OpenAI-Organization", t.organization)
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return provpkg.CallResult{Outcome: provpkg.OutcomeProviderError, Err: err}
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.logger.Warn("close error", "err", cerr)
		}
	}()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return provpkg.CallResult{
			Outcome:    provpkg.OutcomeProviderError,
			Err:        fmt.Errorf("reading response: %w", err),
			StatusCode: resp.StatusCode,
		}
	}

	retryAfter := httputil.ParseRetryAfter(resp.Header.Get("Retry-After"))

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError {
		return provpkg.CallResult{
			Outcome:    provpkg.OutcomeProviderError,
			Err:        fmt.Errorf("%w: HTTP %d", provpkg.ErrUnexpectedStatus, resp.StatusCode),
			StatusCode: resp.StatusCode,
			Body:       bodyBytes,
			RetryAfter: retryAfter,
		}
	}

	if resp.StatusCode != http.StatusOK {
		return provpkg.CallResult{
			Outcome: provpkg.OutcomeTerminal,
			Err:     backoff.Permanent(fmt.Errorf("%w: %d %s", provpkg.ErrUnexpectedStatus, resp.StatusCode, string(bodyBytes))),
		}
	}

	var response ChatGPTResponse
	if err := json.Unmarshal(bodyBytes, &response); err != nil {
		return provpkg.CallResult{
			Outcome: provpkg.OutcomeTerminal,
			Err:     backoff.Permanent(fmt.Errorf("%w: unmarshaling gpt response: %w", provpkg.ErrInvalidResponsePart, err)),
		}
	}

	if len(response.Choices) == 0 {
		t.logger.Error("GPT response is not recognizable", "body", string(bodyBytes))
		return provpkg.CallResult{
			Outcome: provpkg.OutcomeTerminal,
			Err:     backoff.Permanent(fmt.Errorf("%w: %+v", provpkg.ErrEmptyCandidate, response)),
		}
	}

	return provpkg.CallResult{Outcome: provpkg.OutcomeSuccess, Text: response.Choices[0].Message.Content}
}

// buildRequest constructs the chat-completions payload sent to OpenAI.
func buildRequest(model, prompt string, imageData []byte, responseSchemaName string, responseSchema any) *ChatGPTRequest {
	contents := []Content{{Type: "text", Text: prompt}}
	if len(imageData) > 0 {
		data64 := base64.StdEncoding.EncodeToString(imageData)
		contents = append(contents, Content{
			Type:     "image_url",
			ImageURL: &ImageURL{URL: fmt.Sprintf(imageDataURLFormat, data64)},
		})
	}
	req := &ChatGPTRequest{
		Model:     model,
		Messages:  []RequestMessage{{Role: "user", Content: contents}},
		MaxTokens: provpkg.DefaultMaxTokens,
	}
	if responseSchema != nil {
		req.ResponseFormat = &ResponseFormatDef{
			Type: TypeJSONSchema,
			JSONSchema: &JSONSchemaDef{
				Name:   responseSchemaName,
				Strict: true,
				Schema: responseSchema,
			},
		}
	}
	return req
}

// ProviderOption configures an OpenAI provider at construction time.
type ProviderOption func(*providerConfig)

type providerConfig struct {
	model              string
	baseURL            string
	organization       string
	responseSchema     any
	responseSchemaName string
	client             *http.Client
	httpTimeout        time.Duration
	logger             *slog.Logger
	tracer             Tracer
}

// WithModel sets the model used by the assembled analyzer.
func WithModel(model string) ProviderOption {
	return func(c *providerConfig) {
		if model != "" {
			c.model = model
		}
	}
}

// WithBaseURL overrides the base URL for the OpenAI API.
func WithBaseURL(u string) ProviderOption {
	return func(c *providerConfig) {
		if u != "" {
			c.baseURL = u
		}
	}
}

// WithOrganization sets the OpenAI-Organization header
func WithOrganization(org string) ProviderOption {
	return func(c *providerConfig) { c.organization = org }
}

// WithResponseSchema configures OpenAI's `response_format` parameter
func WithResponseSchema(name string, schema any) ProviderOption {
	return func(c *providerConfig) {
		c.responseSchemaName = name
		c.responseSchema = schema
	}
}

// WithHTTPClient injects a custom *http.Client.
func WithHTTPClient(client *http.Client) ProviderOption {
	return func(c *providerConfig) { c.client = client }
}

// WithHTTPClientTimeout sets the HTTP client timeout
func WithHTTPClientTimeout(d time.Duration) ProviderOption {
	return func(c *providerConfig) { c.httpTimeout = d }
}

// WithLogger sets the *slog.Logger used by the transport.
func WithLogger(l *slog.Logger) ProviderOption {
	return func(c *providerConfig) { c.logger = l }
}

// WithTracer sets the tracer used by the transport
func WithTracer(tr Tracer) ProviderOption {
	return func(c *providerConfig) { c.tracer = tr }
}

// Preflight is a no-op for OpenAI
func (t *transport) Preflight(_ context.Context) error {
	t.logger.Warn("openai preflight: not configured (skipping model-catalog / key-liveness check)")
	return nil
}

// NewProvider builds an OpenAI provpkg.Provider configured with the given API keys and options.
func NewProvider(apiKeys []string, opts ...ProviderOption) provpkg.Provider[string] {
	cfg := providerConfig{
		model:   DefaultModel,
		baseURL: DefaultBaseURL,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	if cfg.logger == nil {
		cfg.logger = slog.New(slog.DiscardHandler)
	}
	if cfg.tracer == nil {
		cfg.tracer = trace.NewNoOpTracer()
	}
	client := cfg.client
	if client == nil {
		client = &http.Client{}
	}
	if cfg.httpTimeout > 0 {
		client.Timeout = cfg.httpTimeout
	}
	return &transport{
		apiKeys:            provpkg.NewKeyPool(apiKeys),
		model:              cfg.model,
		baseURL:            cfg.baseURL,
		organization:       cfg.organization,
		responseSchema:     cfg.responseSchema,
		responseSchemaName: cfg.responseSchemaName,
		client:             client,
		logger:             cfg.logger,
		tracer:             cfg.tracer,
	}
}
