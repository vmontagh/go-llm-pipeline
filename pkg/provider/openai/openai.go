// Package openai implements the transport half of the analyzer Template
// Method for the OpenAI chat-completions API.
package openai

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"iter"
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
	functionType             = "function"
	textContentType          = "text"
	// emptyArguments is sent for a tool call that has no arguments.
	emptyArguments        = "{}"
	insufficientQuotaType = "insufficient_quota"
)

// ChatGPTRequest represents a request to the OpenAI chat completions API.
type ChatGPTRequest struct {
	Model          string             `json:"model"`
	Messages       []RequestMessage   `json:"messages"`
	MaxTokens      int                `json:"max_tokens"`
	ResponseFormat *ResponseFormatDef `json:"response_format,omitempty"`
	Stream         bool               `json:"stream,omitempty"`
	StreamOptions  *StreamOptions     `json:"stream_options,omitempty"`
	Tools          []Tool             `json:"tools,omitempty"`
}

// Tool declares a function the model may call.
type Tool struct {
	Type     string      `json:"type"`
	Function FunctionDef `json:"function"`
}

// FunctionDef describes a callable function.
type FunctionDef struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Parameters is the JSON Schema of the arguments object.
	Parameters any `json:"parameters,omitempty"`
}

// ToolCall is a function call made by the model.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall names the function and holds its JSON arguments.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// StreamOptions configures a streamed reply.
type StreamOptions struct {
	// IncludeUsage asks for the token usage in an event after the text.
	IncludeUsage bool `json:"include_usage"`
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
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	Refusal   *string    `json:"refusal"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// RequestMessage represents a single message in a chat completion request.
// An assistant message that only calls tools has no Content; a tool message
// answers the call with ID ToolCallID.
type RequestMessage struct {
	Role       string     `json:"role"`
	Content    []Content  `json:"content,omitempty"`
	Refusal    *string    `json:"refusal"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// Content represents a part of the message content, such as text or an image URL.
type Content struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

// MarshalJSON keeps the text key of a text part even when the text is
// empty, as OpenAI requires it; an empty tool result is valid.
func (c Content) MarshalJSON() ([]byte, error) {
	type plain Content
	var v any = plain(c)
	if c.Type == textContentType {
		v = struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{Type: c.Type, Text: c.Text}
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encoding content: %w", err)
	}
	return out, nil
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
	return t.CallMessages(ctx, lease, []provpkg.Message{promptMessage(prompt, imageData)})
}

// promptMessage is the user message Call sends: the prompt, then the image.
func promptMessage(prompt string, imageData []byte) provpkg.Message {
	m := provpkg.TextMessage(provpkg.RoleUser, prompt)
	if len(imageData) > 0 {
		m.Parts = append(m.Parts, provpkg.Part{Image: imageData})
	}
	return m
}

// CallMessages implements provpkg.MessageCaller with native chat roles.
func (t *transport) CallMessages(ctx context.Context, lease *provpkg.Lease[string], msgs []provpkg.Message) provpkg.CallResult {
	return t.CallTools(ctx, lease, msgs, nil)
}

// CallTools implements provpkg.ToolCaller with OpenAI function tools.
func (t *transport) CallTools(ctx context.Context, lease *provpkg.Lease[string], msgs []provpkg.Message, tools []provpkg.ToolSpec) provpkg.CallResult {
	defer t.tracer.StartRegion(ctx, callRegionName).End()
	req := buildRequest(t.model, msgs, t.responseSchemaName, t.responseSchema)
	req.Tools = requestTools(tools)
	body, failed := t.send(ctx, lease, req)
	if body == nil {
		return failed
	}
	defer t.closeBody(body)

	bodyBytes, err := io.ReadAll(body)
	if err != nil {
		return readFailure(http.StatusOK, err)
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

	msg := response.Choices[0].Message
	return provpkg.CallResult{Outcome: provpkg.OutcomeSuccess, Text: msg.Content, ToolCalls: toolCalls(msg.ToolCalls)}
}

// requestTools declares tools as OpenAI functions.
func requestTools(tools []provpkg.ToolSpec) []Tool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]Tool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, Tool{Type: functionType, Function: FunctionDef{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  tool.Parameters,
		}})
	}
	return out
}

// toolCalls converts the model's function calls.
func toolCalls(calls []ToolCall) []provpkg.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]provpkg.ToolCall, 0, len(calls))
	for _, c := range calls {
		out = append(out, provpkg.ToolCall{ID: c.ID, Name: c.Function.Name, Arguments: c.Function.Arguments})
	}
	return out
}

// Stream implements provpkg.Streamer. It asks for the usage, which OpenAI
// sends in its own event after the text.
func (t *transport) Stream(ctx context.Context, lease *provpkg.Lease[string], msgs []provpkg.Message) (iter.Seq2[provpkg.Chunk, error], provpkg.CallResult) {
	defer t.tracer.StartRegion(ctx, callRegionName).End()
	req := buildRequest(t.model, msgs, t.responseSchemaName, t.responseSchema)
	req.Stream = true
	req.StreamOptions = &StreamOptions{IncludeUsage: true}
	body, failed := t.send(ctx, lease, req)
	if body == nil {
		return nil, failed
	}
	return t.chunks(ctx, body), provpkg.CallResult{Outcome: provpkg.OutcomeSuccess}
}

// streamEvent is one event of a streamed reply. Error is set when the
// request fails mid-stream.
type streamEvent struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *Usage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// streamDone is the data of the event that ends a stream.
const streamDone = "[DONE]"

// chunks yields the text of each event, and the usage in its own chunk.
func (t *transport) chunks(ctx context.Context, body io.ReadCloser) iter.Seq2[provpkg.Chunk, error] {
	return func(yield func(provpkg.Chunk, error) bool) {
		defer t.closeBody(body)
		for data, err := range httputil.SSEData(body) {
			// The reader may hold buffered events that a canceled read would not stop.
			if ctxErr := ctx.Err(); ctxErr != nil {
				yield(provpkg.Chunk{}, fmt.Errorf("reading stream: %w", ctxErr))
				return
			}
			if err != nil {
				yield(provpkg.Chunk{}, fmt.Errorf("reading stream: %w: %w", provpkg.ErrIncompleteStream, err))
				return
			}
			if string(data) == streamDone {
				return
			}
			c, err := parseEvent(data)
			if err != nil {
				yield(provpkg.Chunk{}, fmt.Errorf("reading stream: %w", err))
				return
			}
			if (c.Text != "" || c.Usage != nil) && !yield(c, nil) {
				return
			}
		}
		// A cancel that comes just before EOF is still the cause.
		yield(provpkg.Chunk{}, fmt.Errorf("reading stream: %w", cmp.Or(ctx.Err(), provpkg.ErrIncompleteStream)))
	}
}

// parseEvent turns one event into a chunk.
func parseEvent(data []byte) (provpkg.Chunk, error) {
	var ev streamEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		return provpkg.Chunk{}, fmt.Errorf("%w: unmarshaling stream event: %w", provpkg.ErrInvalidResponsePart, err)
	}
	if ev.Error != nil {
		return provpkg.Chunk{}, fmt.Errorf("%w: %s", provpkg.ErrIncompleteStream, ev.Error.Message)
	}
	var c provpkg.Chunk
	if len(ev.Choices) > 0 {
		c.Text = ev.Choices[0].Delta.Content
	}
	if ev.Usage != nil {
		c.Usage = &provpkg.Usage{Input: ev.Usage.PromptTokens, Output: ev.Usage.CompletionTokens}
	}
	return c, nil
}

// send posts req. It returns the open body on 200 OK, and otherwise a nil
// body and the failed result.
func (t *transport) send(ctx context.Context, lease *provpkg.Lease[string], req *ChatGPTRequest) (io.ReadCloser, provpkg.CallResult) {
	reqB, err := json.Marshal(req)
	if err != nil {
		return nil, provpkg.CallResult{
			Outcome: provpkg.OutcomeTerminal,
			Err:     backoff.Permanent(fmt.Errorf("marshaling vision request: %w", err)),
		}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+chatCompletionsPath, bytes.NewReader(reqB))
	if err != nil {
		return nil, provpkg.CallResult{Outcome: provpkg.OutcomeTerminal, Err: backoff.Permanent(err)}
	}

	httpReq.Header.Add("Authorization", authorizationHeaderValue+lease.Value)
	httpReq.Header.Add("Content-Type", "application/json")
	if t.organization != "" {
		httpReq.Header.Add("OpenAI-Organization", t.organization)
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, provpkg.CallResult{Outcome: provpkg.OutcomeProviderError, Err: err}
	}
	if resp.StatusCode == http.StatusOK {
		return resp.Body, provpkg.CallResult{}
	}
	defer t.closeBody(resp.Body)

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, readFailure(resp.StatusCode, err)
	}

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError {
		return nil, provpkg.CallResult{
			Outcome:    provpkg.OutcomeProviderError,
			Err:        fmt.Errorf("%w: HTTP %d", provpkg.ErrUnexpectedStatus, resp.StatusCode),
			StatusCode: resp.StatusCode,
			Body:       bodyBytes,
			RetryAfter: httputil.ParseRetryAfter(resp.Header.Get("Retry-After")),
		}
	}

	return nil, provpkg.CallResult{
		Outcome: provpkg.OutcomeTerminal,
		Err:     backoff.Permanent(fmt.Errorf("%w: %d %s", provpkg.ErrUnexpectedStatus, resp.StatusCode, string(bodyBytes))),
	}
}

// readFailure is the result of a reply whose body could not be read.
func readFailure(status int, err error) provpkg.CallResult {
	return provpkg.CallResult{
		Outcome:    provpkg.OutcomeProviderError,
		Err:        fmt.Errorf("reading response: %w", err),
		StatusCode: status,
	}
}

func (t *transport) closeBody(body io.Closer) {
	if err := body.Close(); err != nil {
		t.logger.Warn("close error", "err", err)
	}
}

// buildRequest constructs the chat-completions payload sent to OpenAI.
func buildRequest(model string, msgs []provpkg.Message, responseSchemaName string, responseSchema any) *ChatGPTRequest {
	req := &ChatGPTRequest{
		Model:     model,
		Messages:  requestMessages(msgs),
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

// requestMessages converts msgs. Each tool result becomes its own tool
// message, as OpenAI expects.
func requestMessages(msgs []provpkg.Message) []RequestMessage {
	out := make([]RequestMessage, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == provpkg.RoleTool {
			for _, p := range m.Parts {
				out = append(out, RequestMessage{
					Role:       m.Role.String(),
					Content:    []Content{{Type: textContentType, Text: p.ToolResult.Content}},
					ToolCallID: p.ToolResult.CallID,
				})
			}
			continue
		}
		rm := RequestMessage{Role: m.Role.String()}
		for _, p := range m.Parts {
			switch {
			case p.ToolCall != nil:
				rm.ToolCalls = append(rm.ToolCalls, ToolCall{ID: p.ToolCall.ID, Type: functionType, Function: FunctionCall{
					Name:      p.ToolCall.Name,
					Arguments: cmp.Or(p.ToolCall.Arguments, emptyArguments),
				}})
			case len(p.Image) > 0:
				data64 := base64.StdEncoding.EncodeToString(p.Image)
				rm.Content = append(rm.Content, Content{
					Type:     "image_url",
					ImageURL: &ImageURL{URL: fmt.Sprintf(imageDataURLFormat, data64)},
				})
			case p.Text != "":
				rm.Content = append(rm.Content, Content{Type: textContentType, Text: p.Text})
			}
		}
		if len(rm.Content) == 0 && len(rm.ToolCalls) == 0 {
			// A signature-only turn would otherwise have neither content nor calls.
			rm.Content = []Content{{Type: textContentType}}
		}
		out = append(out, rm)
	}
	return out
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
