// Package ollama implements the transport half of the analyzer Template
// Method using a locally running Ollama instance with a vision-capable
// model. It also provides an Embedder that produces embedding vectors from
// the same Ollama instance via the /api/embed endpoint.
package ollama

import (
	"context"
	"encoding/base64"
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

	provpkg "github.com/vmontagh/go-llm-pipeline/pkg/provider"
	"github.com/vmontagh/go-llm-pipeline/pkg/trace"
)

const providerLabel = "ollama"
const traceRegionName = "Analyze_Ollama"
const callRegionName = "Call_Ollama"

type Tracer interface {
	StartRegion(ctx context.Context, name string) interface{ End() }
}

type endpointKeyPool = provpkg.KeyPool[struct{}]

const (
	defaultTemperature = 0.3
	defaultNumPredict  = 1024
	defaultNumCtx      = 8192
)

const formatJSONSentinel = "json"
const doneReasonLength = "length"

const (
	generatePath = "/api/generate"
	chatPath     = "/api/chat"
)

var ErrOutputLimitExhausted = errors.New("output generation limit exhausted")
var ErrContextWindowExhausted = errors.New("context window exhausted")

type Request struct {
	Model     string         `json:"model"`
	Prompt    string         `json:"prompt"`
	Images    []string       `json:"images"`
	Stream    bool           `json:"stream"`
	Format    any            `json:"format,omitempty"`
	Options   map[string]any `json:"options,omitempty"`
	KeepAlive string         `json:"keep_alive,omitempty"`
	// Think toggles the model's reasoning ("thinking") phase. Nil omits the
	// field so Ollama applies the model's default behavior.
	Think *bool `json:"think,omitempty"`
}

// chatRequest is the /api/chat body. Its fields match Request, except
// Messages replaces Prompt and Images.
type chatRequest struct {
	Model     string         `json:"model"`
	Messages  []chatMessage  `json:"messages"`
	Stream    bool           `json:"stream"`
	Format    any            `json:"format,omitempty"`
	Options   map[string]any `json:"options,omitempty"`
	KeepAlive string         `json:"keep_alive,omitempty"`
	Think     *bool          `json:"think,omitempty"`
}

type chatMessage struct {
	Role    string   `json:"role"`
	Content string   `json:"content"`
	Images  []string `json:"images,omitempty"`
}

// ChatMessage is the reply message of the /api/chat endpoint.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Response struct {
	// Message is set by /api/chat in place of Response.
	Message         *ChatMessage `json:"message,omitempty"`
	Response        string       `json:"response"`
	Done            bool         `json:"done"`
	DoneReason      string       `json:"done_reason,omitempty"`
	PromptEvalCount int          `json:"prompt_eval_count,omitempty"`
	EvalCount       int          `json:"eval_count,omitempty"`
	TotalDuration   int64        `json:"total_duration,omitempty"`
}

// text returns the model output of either endpoint.
func (r *Response) text() string {
	if r.Message != nil {
		return r.Message.Content
	}
	return r.Response
}

type transport struct {
	host           string
	model          string
	keepAlive      string
	numCtx         int
	numPredict     int
	think          *bool
	contextLength  int
	responseSchema any
	client         *http.Client
	endpoint       *endpointKeyPool
	logger         *slog.Logger
	tracer         Tracer
}

func (t *transport) Name() string            { return "ollama/" + t.model }
func (t *transport) Model() string           { return t.model }
func (t *transport) ProviderLabel() string   { return providerLabel }
func (t *transport) TraceRegionName() string { return traceRegionName }
func (t *transport) Keys() *endpointKeyPool  { return t.endpoint }
func (t *transport) ContextLength() int      { return t.contextLength }

func (t *transport) ClassifyError(in provpkg.ClassifyInput, _ provpkg.RetryContext) provpkg.ErrorTier {
	return classifyTier(in.StatusCode, in.Err)
}

// classifyTier maps an HTTP status code (0 when no response was received) and
// an optional transport error to the retry tier shared by the analyzer
// transport and the Embedder.
func classifyTier(statusCode int, err error) provpkg.ErrorTier {
	switch {
	case statusCode == 0 && err != nil:
		return provpkg.TierShortTransient
	case statusCode == 0:
		return provpkg.TierTerminal
	case statusCode == http.StatusTooManyRequests:
		return provpkg.TierShortRateLimit
	case statusCode >= http.StatusInternalServerError:
		return provpkg.TierShortTransient
	default:
		return provpkg.TierTerminal
	}
}

func (t *transport) EnrichError(_ provpkg.ErrorTier, rawErr error, _ []byte) error {
	return rawErr
}

func (t *transport) Call(ctx context.Context, _ *provpkg.Lease[struct{}], prompt string, imageData []byte) provpkg.CallResult {
	defer t.tracer.StartRegion(ctx, callRegionName).End()
	var imageBase64 string
	if len(imageData) > 0 {
		imageBase64 = base64.StdEncoding.EncodeToString(imageData)
	}
	reqData := buildRequest(&buildRequestParams{
		model:          t.model,
		prompt:         prompt,
		imageBase64:    imageBase64,
		keepAlive:      t.keepAlive,
		numCtx:         t.numCtx,
		numPredict:     t.numPredict,
		think:          t.think,
		responseSchema: t.responseSchema,
	})
	return t.post(ctx, generatePath, reqData)
}

// CallMessages implements provpkg.MessageCaller through the /api/chat
// endpoint, which takes native roles.
func (t *transport) CallMessages(ctx context.Context, _ *provpkg.Lease[struct{}], msgs []provpkg.Message) provpkg.CallResult {
	defer t.tracer.StartRegion(ctx, callRegionName).End()
	return t.post(ctx, chatPath, t.chatRequest(msgs, false))
}

// Stream implements provpkg.Streamer through /api/chat, which streams one
// JSON object per line.
func (t *transport) Stream(ctx context.Context, _ *provpkg.Lease[struct{}], msgs []provpkg.Message) (iter.Seq2[provpkg.Chunk, error], provpkg.CallResult) {
	defer t.tracer.StartRegion(ctx, callRegionName).End()
	req := t.jsonRequest(chatPath, t.chatRequest(msgs, true))
	resp, err := sendJSON(ctx, req)
	if err == nil && resp.StatusCode == http.StatusOK {
		return t.chunks(ctx, resp.Body), provpkg.CallResult{Outcome: provpkg.OutcomeSuccess}
	}
	var res jsonPostResult
	if err == nil {
		res, err = readJSONResult(ctx, t.logger, resp)
	}
	// A status other than 200 always fails.
	failed, _ := failedResult(res, err)
	return nil, failed
}

// chunks yields the text of each streamed line, and the usage on the last.
func (t *transport) chunks(ctx context.Context, body io.ReadCloser) iter.Seq2[provpkg.Chunk, error] {
	return func(yield func(provpkg.Chunk, error) bool) {
		defer closeBody(ctx, t.logger, body)
		dec := json.NewDecoder(body)
		for {
			// The decoder may hold buffered lines that a canceled read would not stop.
			if err := ctx.Err(); err != nil {
				yield(provpkg.Chunk{}, fmt.Errorf("reading stream: %w", err))
				return
			}
			var line streamLine
			if err := dec.Decode(&line); err != nil {
				if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
					err = fmt.Errorf("%w: %w", provpkg.ErrIncompleteStream, err)
				}
				yield(provpkg.Chunk{}, fmt.Errorf("reading stream: %w", err))
				return
			}
			switch {
			case line.Error != "":
				yield(provpkg.Chunk{}, fmt.Errorf("%w: %s", provpkg.ErrIncompleteStream, line.Error))
				return
			case !line.Done:
				if !yield(provpkg.Chunk{Text: line.text()}, nil) {
					return
				}
				continue
			}
			t.logMeta(&line.Response)
			if err := t.lengthError(&line.Response); err != nil {
				yield(provpkg.Chunk{}, err)
				return
			}
			usage := &provpkg.Usage{Input: line.PromptEvalCount, Output: line.EvalCount}
			yield(provpkg.Chunk{Text: line.text(), Usage: usage}, nil)
			return
		}
	}
}

// streamLine is one line of a stream; Error is set when the model fails
// mid-stream.
type streamLine struct {
	Response
	Error string `json:"error"`
}

func (t *transport) chatRequest(msgs []provpkg.Message, stream bool) *chatRequest {
	r := buildRequest(&buildRequestParams{
		model:          t.model,
		keepAlive:      t.keepAlive,
		numCtx:         t.numCtx,
		numPredict:     t.numPredict,
		think:          t.think,
		responseSchema: t.responseSchema,
	})
	return &chatRequest{
		Model:     r.Model,
		Messages:  chatMessages(msgs),
		Stream:    stream,
		Format:    r.Format,
		Options:   r.Options,
		KeepAlive: r.KeepAlive,
		Think:     r.Think,
	}
}

// chatMessages joins each message's text parts with newlines, since an
// Ollama chat message holds one content string.
func chatMessages(msgs []provpkg.Message) []chatMessage {
	out := make([]chatMessage, 0, len(msgs))
	for _, m := range msgs {
		cm := chatMessage{Role: m.Role.String()}
		var texts []string
		for _, p := range m.Parts {
			if len(p.Image) > 0 {
				cm.Images = append(cm.Images, base64.StdEncoding.EncodeToString(p.Image))
				continue
			}
			texts = append(texts, p.Text)
		}
		cm.Content = strings.Join(texts, "\n")
		out = append(out, cm)
	}
	return out
}

// post sends body to path and turns the reply into a CallResult.
func (t *transport) post(ctx context.Context, path string, body any) provpkg.CallResult {
	res, err := doJSONPost(ctx, t.jsonRequest(path, body))
	if failed, ok := failedResult(res, err); ok {
		return failed
	}
	var ollamaResp Response
	if err := json.Unmarshal(res.body, &ollamaResp); err != nil {
		return provpkg.CallResult{
			Outcome: provpkg.OutcomeTerminal,
			Err:     backoff.Permanent(fmt.Errorf("%w: parsing response: %w", provpkg.ErrInvalidResponsePart, err)),
		}
	}
	t.logMeta(&ollamaResp)
	if err := t.lengthError(&ollamaResp); err != nil {
		return provpkg.CallResult{Outcome: provpkg.OutcomeTerminal, Err: backoff.Permanent(err)}
	}
	return provpkg.CallResult{Outcome: provpkg.OutcomeSuccess, Text: ollamaResp.text()}
}

func (t *transport) jsonRequest(path string, body any) jsonPostRequest {
	return jsonPostRequest{client: t.client, logger: t.logger, host: t.host, path: path, body: body}
}

// failedResult reports whether a request failed, and how, from its error
// and status.
func failedResult(res jsonPostResult, err error) (provpkg.CallResult, bool) {
	switch {
	case errors.Is(err, errRequestNotSent):
		return provpkg.CallResult{Outcome: provpkg.OutcomeTerminal, Err: backoff.Permanent(err)}, true
	case err != nil:
		return provpkg.CallResult{Outcome: provpkg.OutcomeProviderError, Err: err, StatusCode: res.statusCode}, true
	case res.statusCode == http.StatusTooManyRequests || res.statusCode >= http.StatusInternalServerError:
		return provpkg.CallResult{
			Outcome:    provpkg.OutcomeProviderError,
			Err:        fmt.Errorf("%w: %d %s", provpkg.ErrUnexpectedStatus, res.statusCode, string(res.body)),
			StatusCode: res.statusCode,
			Body:       res.body,
			RetryAfter: res.retryAfter,
		}, true
	case res.statusCode != http.StatusOK:
		return provpkg.CallResult{
			Outcome: provpkg.OutcomeTerminal,
			Err:     backoff.Permanent(fmt.Errorf("%w: %d %s", provpkg.ErrUnexpectedStatus, res.statusCode, string(res.body))),
		}, true
	default:
		return provpkg.CallResult{}, false
	}
}

func (t *transport) logMeta(r *Response) {
	t.logger.Debug("ollama call meta",
		"model", t.model,
		"done", r.Done,
		"done_reason", r.DoneReason,
		"prompt_eval_count", r.PromptEvalCount,
		"eval_count", r.EvalCount,
		"total_duration_ms", r.TotalDuration/1e6,
		"response_len", len(r.text()),
	)
}

// lengthError explains a reply cut off by the output or context limit, or
// returns nil.
func (t *transport) lengthError(r *Response) error {
	if r.DoneReason != doneReasonLength {
		return nil
	}
	limit := t.numPredict
	if limit <= 0 {
		limit = defaultNumPredict
	}
	if r.EvalCount >= limit {
		return fmt.Errorf(
			"%w: model used %d eval tokens (model=%s, num_predict=%d); the model tried to generate a response longer than allowed, increase ollama.num_predict in config.yaml",
			ErrOutputLimitExhausted, r.EvalCount, t.model, limit,
		)
	}

	var hint string
	remaining := t.contextLength - r.PromptEvalCount
	if t.contextLength > 0 && remaining > 0 && r.EvalCount >= remaining {
		hint = "the response was too long to fit in the remaining context window; try lowering ollama.num_predict, or reduce prompt/EXIF size, or use a model with a larger context window"
	} else {
		hint = "reduce EXIF data via llm_context_budgets or llm_max_exif_bytes in config.yaml, or use a model with a larger context window"
	}
	return fmt.Errorf(
		"%w: model used %d prompt tokens + %d eval tokens = %d total (model=%s, context_length=%d); %s",
		ErrContextWindowExhausted, r.PromptEvalCount, r.EvalCount, r.PromptEvalCount+r.EvalCount, t.model, t.contextLength, hint,
	)
}

type buildRequestParams struct {
	model          string
	prompt         string
	imageBase64    string
	keepAlive      string
	numCtx         int
	numPredict     int
	think          *bool
	responseSchema any
}

func buildRequest(p *buildRequestParams) *Request {
	model := p.model
	prompt := p.prompt
	imageBase64 := p.imageBase64
	keepAlive := p.keepAlive
	numCtx := p.numCtx
	numPredict := p.numPredict
	responseSchema := p.responseSchema

	var format any = formatJSONSentinel
	if responseSchema != nil {
		format = responseSchema
	}

	predictLimit := defaultNumPredict
	if numPredict > 0 {
		predictLimit = numPredict
	}

	req := &Request{
		Model:     model,
		Prompt:    prompt,
		Stream:    false,
		Format:    format,
		KeepAlive: keepAlive,
		Think:     p.think,
		Options: map[string]any{
			"temperature": defaultTemperature,
			"num_predict": predictLimit,
		},
	}
	req.Options["num_ctx"] = defaultNumCtx
	if numCtx > 0 {
		req.Options["num_ctx"] = numCtx
	}
	if imageBase64 != "" {
		req.Images = []string{imageBase64}
	}
	return req
}

func (t *transport) String() string {
	return fmt.Sprintf("ollama.Analyzer{host=%s, model=%s}", t.host, t.model)
}

type ProviderOption func(*providerConfig)

type providerConfig struct {
	keepAlive        string
	numCtx           int
	numPredict       int
	think            *bool
	maxRetries       int
	retryBaseBackoff time.Duration
	retryMaxBackoff  time.Duration
	responseSchema   any
	client           *http.Client
	httpTimeout      time.Duration
	logger           *slog.Logger
	tracer           Tracer
}

func WithKeepAlive(keepAlive string) ProviderOption {
	return func(c *providerConfig) { c.keepAlive = keepAlive }
}

func WithNumCtx(numCtx int) ProviderOption {
	return func(c *providerConfig) { c.numCtx = numCtx }
}

func WithNumPredict(numPredict int) ProviderOption {
	return func(c *providerConfig) { c.numPredict = numPredict }
}

// WithThink explicitly enables or disables the model's reasoning ("thinking")
// phase on /api/generate requests. Without this option the "think" field is
// omitted and Ollama applies the model's default. Disabling thinking is
// usually faster for perception tasks such as image tagging.
func WithThink(think bool) ProviderOption {
	return func(c *providerConfig) { c.think = &think }
}

// WithMaxRetries caps how many times the Embedder retries a transient or
// rate-limited /api/embed response before giving up. Zero disables retries
// (a single attempt); negative values are treated as zero. It is ignored by
// the generation transport, whose retries are governed by the analyzer engine.
func WithMaxRetries(maxRetries int) ProviderOption {
	return func(c *providerConfig) { c.maxRetries = maxRetries }
}

// WithRetryBackoff tunes the Embedder's exponential backoff between retries of
// a transient or rate-limited /api/embed response. base is the delay before the
// first retry and doubles each attempt; maxDelay caps the delay (maxDelay <= 0
// means uncapped). A non-positive base falls back to the default. A server-
// supplied Retry-After still takes precedence when it is longer. It is ignored
// by the generation transport.
func WithRetryBackoff(base, maxDelay time.Duration) ProviderOption {
	return func(c *providerConfig) {
		c.retryBaseBackoff = base
		c.retryMaxBackoff = maxDelay
	}
}

func WithResponseSchema(schema any) ProviderOption {
	return func(c *providerConfig) { c.responseSchema = schema }
}

func WithHTTPClient(client *http.Client) ProviderOption {
	return func(c *providerConfig) { c.client = client }
}

func WithHTTPClientTimeout(d time.Duration) ProviderOption {
	return func(c *providerConfig) { c.httpTimeout = d }
}

// WithLogger sets the *slog.Logger used by the transport.
func WithLogger(l *slog.Logger) ProviderOption {
	return func(c *providerConfig) { c.logger = l }
}

func WithTracer(tr Tracer) ProviderOption {
	return func(c *providerConfig) { c.tracer = tr }
}

// clientWithTimeout returns the HTTP client to use for requests. A caller-
// supplied client is shallow-copied before a timeout is applied so the caller's
// (often shared) client is never mutated; a nil client yields a fresh one.
func clientWithTimeout(client *http.Client, timeout time.Duration) *http.Client {
	if client == nil {
		client = &http.Client{}
	} else {
		clone := *client
		client = &clone
	}
	if timeout > 0 {
		client.Timeout = timeout
	}
	return client
}

func NewProvider(host, model string, opts ...ProviderOption) provpkg.Provider[struct{}] {
	cfg := providerConfig{}
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
	client := clientWithTimeout(cfg.client, cfg.httpTimeout)
	return &transport{
		host:           host,
		model:          model,
		keepAlive:      cfg.keepAlive,
		numCtx:         cfg.numCtx,
		numPredict:     cfg.numPredict,
		think:          cfg.think,
		responseSchema: cfg.responseSchema,
		client:         client,
		endpoint:       provpkg.NewKeyPool([]struct{}{{}}),
		logger:         cfg.logger,
		tracer:         cfg.tracer,
	}
}
