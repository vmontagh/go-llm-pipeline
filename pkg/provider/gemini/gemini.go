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
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
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
	streamContentMethod   = ":streamGenerateContent?alt=sse"
	authHeaderName        = "X-Goog-Api-Key"
	jsonMIMEType          = "application/json"
	imageMIMEType         = "image/jpeg"
	userRole              = "user"
	modelRole             = "model"
	// finishMaxTokens is the finish reason of a reply cut off at the output
	// token limit.
	finishMaxTokens = "MAX_TOKENS"
	// emptyArgs stands in for missing or unusable call arguments; Gemini
	// takes an object.
	emptyArgs = "{}"
	// resultKey holds a tool result in the function response object.
	resultKey = "result"
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
	SystemInstruction *content          `json:"systemInstruction,omitempty"`
	Contents          []content         `json:"contents"`
	Tools             []tool            `json:"tools,omitempty"`
	GenerationConfig  *generationConfig `json:"generationConfig,omitempty"`
}

// tool groups the functions the model may call.
type tool struct {
	FunctionDeclarations []functionDeclaration `json:"functionDeclarations"`
}

// functionDeclaration declares one function. Its JSON Schema is sent as is.
type functionDeclaration struct {
	Name                 string         `json:"name"`
	Description          string         `json:"description,omitempty"`
	ParametersJSONSchema map[string]any `json:"parametersJsonSchema,omitempty"`
}

type content struct {
	Role  string `json:"role,omitempty"`
	Parts []part `json:"parts"`
}

// part is one piece of content. Text is a pointer so that an empty text part
// can be told apart from a non-text part (e.g. inline data).
type part struct {
	Text             *string           `json:"text,omitempty"`
	InlineData       *blob             `json:"inlineData,omitempty"`
	FunctionCall     *functionCall     `json:"functionCall,omitempty"`
	FunctionResponse *functionResponse `json:"functionResponse,omitempty"`
	// Thought marks the model's reasoning, which is not part of the reply.
	Thought          bool   `json:"thought,omitempty"`
	ThoughtSignature string `json:"thoughtSignature,omitempty"`
}

// functionCall is the model's request to call a function.
type functionCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

// functionResponse is a function's result. Response must be an object.
type functionResponse struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
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
	m := provpkg.TextMessage(provpkg.RoleUser, prompt)
	if len(imageData) > 0 {
		m.Parts = append(m.Parts, provpkg.Part{Image: imageData})
	}
	return t.CallMessages(ctx, lease, []provpkg.Message{m})
}

// CallMessages implements provpkg.MessageCaller. System messages become the
// system instruction and assistant turns use the "model" role.
func (t *transport) CallMessages(ctx context.Context, lease *provpkg.Lease[string], msgs []provpkg.Message) provpkg.CallResult {
	defer t.tracer.StartRegion(ctx, callRegionName).End()
	body, failed, ok := t.generate(ctx, lease, t.buildRequest(msgs))
	if !ok {
		return failed
	}
	return parseResponse(body)
}

// CallTools implements provpkg.ToolCaller with Gemini function calling.
func (t *transport) CallTools(ctx context.Context, lease *provpkg.Lease[string], msgs []provpkg.Message, tools []provpkg.ToolSpec) provpkg.CallResult {
	defer t.tracer.StartRegion(ctx, callRegionName).End()
	req := t.buildRequest(msgs)
	req.Tools = requestTools(tools)
	body, failed, ok := t.generate(ctx, lease, req)
	if !ok {
		return failed
	}
	return parseToolResponse(body)
}

// generate posts req to generateContent and reads the reply. ok is false
// when the call failed, as failed describes.
func (t *transport) generate(ctx context.Context, lease *provpkg.Lease[string], req generateContentRequest) (body []byte, failed provpkg.CallResult, ok bool) {
	rc, failed := t.send(ctx, lease, req, generateContentMethod)
	if rc == nil {
		return nil, failed, false
	}
	defer t.closeBody(rc)
	body, err := io.ReadAll(rc)
	if err != nil {
		return nil, readFailure(http.StatusOK, nil, err), false
	}
	return body, provpkg.CallResult{}, true
}

// Stream implements provpkg.Streamer. Each event repeats the running
// usage, so only the last chunk carries it.
func (t *transport) Stream(ctx context.Context, lease *provpkg.Lease[string], msgs []provpkg.Message) (iter.Seq2[provpkg.Chunk, error], provpkg.CallResult) {
	defer t.tracer.StartRegion(ctx, callRegionName).End()
	body, failed := t.send(ctx, lease, t.buildRequest(msgs), streamContentMethod)
	if body == nil {
		return nil, failed
	}
	return t.chunks(ctx, body), provpkg.CallResult{Outcome: provpkg.OutcomeSuccess}
}

// streamEvent is one event of a streamed reply. Error is set when the
// request fails mid-stream.
type streamEvent struct {
	generateContentResponse
	UsageMetadata *struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		ThoughtsTokenCount   int `json:"thoughtsTokenCount"`
	} `json:"usageMetadata"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// chunks yields the text of each event. The event with a finish reason ends
// the reply and carries the usage. Like Call, a reply without text fails.
func (t *transport) chunks(ctx context.Context, body io.ReadCloser) iter.Seq2[provpkg.Chunk, error] {
	return func(yield func(provpkg.Chunk, error) bool) {
		defer t.closeBody(body)
		gotText := false
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
			ev, err := parseEvent(data)
			if err != nil {
				yield(provpkg.Chunk{}, fmt.Errorf("reading stream: %w", err))
				return
			}
			gotText = gotText || ev.hasText
			if ev.done && !gotText {
				yield(provpkg.Chunk{}, fmt.Errorf("reading stream: %w: no text", provpkg.ErrEmptyCandidate))
				return
			}
			if ev.done {
				yield(ev.chunk, nil)
				return
			}
			if ev.chunk.Text != "" && !yield(ev.chunk, nil) {
				return
			}
		}
		// A cancel that comes just before EOF is still the cause.
		yield(provpkg.Chunk{}, fmt.Errorf("reading stream: %w", cmp.Or(ctx.Err(), provpkg.ErrIncompleteStream)))
	}
}

// parsedEvent is one stream event turned into a chunk.
type parsedEvent struct {
	chunk provpkg.Chunk
	// hasText is set when the event has a text part, even an empty one.
	hasText bool
	// done is set when the event ends the reply.
	done bool
}

// parseEvent turns one event into a chunk.
func parseEvent(data []byte) (parsedEvent, error) {
	var p parsedEvent
	var ev streamEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		return p, fmt.Errorf("%w: unmarshaling stream event: %w", provpkg.ErrInvalidResponsePart, err)
	}
	if ev.Error != nil {
		return p, fmt.Errorf("%w: %s", provpkg.ErrIncompleteStream, ev.Error.Message)
	}
	if len(ev.Candidates) == 0 {
		if ev.PromptFeedback != nil && ev.PromptFeedback.BlockReason != "" {
			return p, fmt.Errorf("%w: no candidates (block reason %q)", provpkg.ErrEmptyCandidate, ev.PromptFeedback.BlockReason)
		}
		return p, nil
	}
	cand := ev.Candidates[0]
	var text strings.Builder
	for _, part := range cand.Content.Parts {
		if part.Text == nil {
			return p, fmt.Errorf("%w: part is not text", provpkg.ErrInvalidResponsePart)
		}
		text.WriteString(*part.Text)
	}
	p.chunk.Text = text.String()
	p.hasText = len(cand.Content.Parts) > 0
	if cand.FinishReason == "" {
		return p, nil
	}
	if u := ev.UsageMetadata; u != nil {
		p.chunk.Usage = &provpkg.Usage{Input: u.PromptTokenCount, Output: u.CandidatesTokenCount + u.ThoughtsTokenCount}
	}
	p.done = true
	return p, nil
}

// send posts req to method. It returns the open body on 200 OK, and
// otherwise a nil body and the failed result.
func (t *transport) send(ctx context.Context, lease *provpkg.Lease[string], req generateContentRequest, method string) (io.ReadCloser, provpkg.CallResult) {
	reqB, err := json.Marshal(req)
	if err != nil {
		return nil, terminal(fmt.Errorf("marshaling gemini request: %w", err))
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint(method), bytes.NewReader(reqB))
	if err != nil {
		return nil, terminal(fmt.Errorf("building gemini request: %w", err))
	}
	httpReq.Header.Set(authHeaderName, lease.Value)
	httpReq.Header.Set("Content-Type", jsonMIMEType)

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, provpkg.CallResult{Outcome: provpkg.OutcomeProviderError, Err: fmt.Errorf("calling gemini: %w", err)}
	}
	if resp.StatusCode == http.StatusOK {
		return resp.Body, provpkg.CallResult{}
	}
	defer t.closeBody(resp.Body)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, readFailure(resp.StatusCode, resp.Header, err)
	}
	return nil, provpkg.CallResult{
		Outcome:    provpkg.OutcomeProviderError,
		Err:        newAPIError(resp.StatusCode, body),
		StatusCode: resp.StatusCode,
		Body:       body,
		RetryAfter: httputil.ParseRetryAfter(resp.Header.Get("Retry-After")),
	}
}

// readFailure is the result of a reply whose body could not be read. It
// still honors the server's cooldown on a 429 or 503.
func readFailure(status int, header http.Header, err error) provpkg.CallResult {
	return provpkg.CallResult{
		Outcome:    provpkg.OutcomeProviderError,
		Err:        fmt.Errorf("reading gemini response: %w", err),
		StatusCode: status,
		RetryAfter: httputil.ParseRetryAfter(header.Get("Retry-After")),
	}
}

func (t *transport) closeBody(body io.Closer) {
	if err := body.Close(); err != nil {
		t.logger.Warn("close error", "err", err)
	}
}

// parseResponse extracts the first text part of the first candidate.
func parseResponse(body []byte) provpkg.CallResult {
	cand, failed, ok := firstCandidate(body)
	if !ok {
		return failed
	}
	text := cand.Content.Parts[0].Text
	if text == nil {
		return terminal(fmt.Errorf("%w: first part is not text", provpkg.ErrInvalidResponsePart))
	}
	return provpkg.CallResult{Outcome: provpkg.OutcomeSuccess, Text: *text}
}

// parseToolResponse returns the function calls of the first candidate, in
// order, and its text without the model's thoughts. Parts also keeps the
// signatures of other parts, such as thoughts, so they can be sent back.
func parseToolResponse(body []byte) provpkg.CallResult {
	cand, failed, ok := firstCandidate(body)
	if !ok {
		return failed
	}
	// A cut-off turn may hold half its calls, and running them has effects.
	if cand.FinishReason == finishMaxTokens {
		return terminal(fmt.Errorf("%w: reply cut off (finish reason %q)", provpkg.ErrInvalidResponsePart, cand.FinishReason))
	}
	res := provpkg.CallResult{Outcome: provpkg.OutcomeSuccess}
	var text strings.Builder
	hasText := false
	// Parts keeps the reply in order with its signatures, which Gemini wants
	// back intact.
	for _, p := range cand.Content.Parts {
		switch {
		case p.FunctionCall != nil:
			c := p.FunctionCall
			call := provpkg.ToolCall{ID: c.ID, Name: c.Name, Arguments: string(c.Args)}
			res.ToolCalls = append(res.ToolCalls, call)
			res.Parts = append(res.Parts, provpkg.Part{ToolCall: &call, Signature: p.ThoughtSignature})
		case p.Text != nil && !p.Thought:
			hasText = true
			text.WriteString(*p.Text)
			// An empty text part is kept only for its signature.
			if *p.Text != "" || p.ThoughtSignature != "" {
				res.Parts = append(res.Parts, provpkg.Part{Text: *p.Text, Signature: p.ThoughtSignature})
			}
		case p.ThoughtSignature != "":
			res.Parts = append(res.Parts, provpkg.Part{Signature: p.ThoughtSignature})
		}
	}
	if !hasText && len(res.ToolCalls) == 0 {
		return terminal(fmt.Errorf("%w: no function call or answer text", provpkg.ErrInvalidResponsePart))
	}
	res.Text = text.String()
	return res
}

// firstCandidate decodes body and returns its first candidate, which has
// parts. ok is false when there is none, as failed describes.
func firstCandidate(body []byte) (cand candidate, failed provpkg.CallResult, ok bool) {
	var resp generateContentResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return cand, terminal(fmt.Errorf("%w: unmarshaling gemini response: %w", provpkg.ErrInvalidResponsePart, err)), false
	}
	if len(resp.Candidates) == 0 {
		var blockReason string
		if resp.PromptFeedback != nil {
			blockReason = resp.PromptFeedback.BlockReason
		}
		return cand, terminal(fmt.Errorf("%w: no candidates (block reason %q)", provpkg.ErrEmptyCandidate, blockReason)), false
	}
	cand = resp.Candidates[0]
	if len(cand.Content.Parts) == 0 {
		return cand, terminal(fmt.Errorf("%w: no parts (finish reason %q)", provpkg.ErrEmptyCandidate, cand.FinishReason)), false
	}
	return cand, provpkg.CallResult{}, true
}

func terminal(err error) provpkg.CallResult {
	return provpkg.CallResult{Outcome: provpkg.OutcomeTerminal, Err: backoff.Permanent(err)}
}

// endpoint returns the URL of method. Like the Google SDKs, a model name
// without a resource prefix (e.g. "tunedModels/x") is a base model.
func (t *transport) endpoint(method string) string {
	name := t.model
	if !strings.Contains(name, "/") {
		name = modelResourcePrefix + name
	}
	return t.baseURL + "/" + name + method
}

func (t *transport) buildRequest(msgs []provpkg.Message) generateContentRequest {
	var req generateContentRequest
	for _, m := range msgs {
		parts := make([]part, 0, len(m.Parts))
		for _, p := range m.Parts {
			parts = append(parts, requestPart(p))
		}
		switch m.Role {
		case provpkg.RoleSystem:
			if req.SystemInstruction == nil {
				req.SystemInstruction = &content{}
			}
			req.SystemInstruction.Parts = append(req.SystemInstruction.Parts, parts...)
		case provpkg.RoleAssistant:
			req.Contents = append(req.Contents, content{Role: modelRole, Parts: parts})
		default:
			// Gemini takes tool results in user turns.
			req.Contents = append(req.Contents, content{Role: userRole, Parts: parts})
		}
	}
	if t.responseSchema != nil {
		req.GenerationConfig = &generationConfig{
			ResponseMIMEType: jsonMIMEType,
			ResponseSchema:   t.responseSchema,
		}
	}
	return req
}

// requestPart converts one message part.
func requestPart(p provpkg.Part) part {
	switch {
	case len(p.Image) > 0:
		return part{InlineData: &blob{MIMEType: imageMIMEType, Data: p.Image}}
	case p.ToolCall != nil:
		c := p.ToolCall
		return part{
			FunctionCall:     &functionCall{ID: c.ID, Name: c.Name, Args: argsObject(c.Arguments)},
			ThoughtSignature: p.Signature,
		}
	case p.ToolResult != nil:
		r := p.ToolResult
		return part{FunctionResponse: &functionResponse{ID: r.CallID, Name: r.Name, Response: toolResponse(r.Content)}}
	default:
		return part{Text: &p.Text, ThoughtSignature: p.Signature}
	}
}

// toolResponse wraps a tool result in the object Gemini requires. JSON
// content is sent as a value, anything else, such as an error, as a string.
// argsObject returns args as a JSON object. Malformed or non-object arguments,
// which the model may send and the analyzer reports back, become an empty
// object, since the API takes only an object.
func argsObject(args string) json.RawMessage {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(args), &obj); err != nil || obj == nil {
		return json.RawMessage(emptyArgs)
	}
	return json.RawMessage(args)
}

func toolResponse(result string) map[string]any {
	if json.Valid([]byte(result)) {
		return map[string]any{resultKey: json.RawMessage(result)}
	}
	return map[string]any{resultKey: result}
}

// requestTools declares tools as Gemini functions.
func requestTools(tools []provpkg.ToolSpec) []tool {
	if len(tools) == 0 {
		return nil
	}
	decls := make([]functionDeclaration, 0, len(tools))
	for _, spec := range tools {
		decls = append(decls, functionDeclaration{Name: spec.Name, Description: spec.Description, ParametersJSONSchema: spec.Parameters})
	}
	return []tool{{FunctionDeclarations: decls}}
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
