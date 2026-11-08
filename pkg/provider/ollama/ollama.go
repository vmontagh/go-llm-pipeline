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
	"log/slog"
	"net/http"
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

type Response struct {
	Response        string `json:"response"`
	Done            bool   `json:"done"`
	DoneReason      string `json:"done_reason,omitempty"`
	PromptEvalCount int    `json:"prompt_eval_count,omitempty"`
	EvalCount       int    `json:"eval_count,omitempty"`
	TotalDuration   int64  `json:"total_duration,omitempty"`
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

	res, err := doJSONPost(ctx, jsonPostRequest{
		client: t.client,
		logger: t.logger,
		host:   t.host,
		path:   "/api/generate",
		body:   reqData,
	})
	if err != nil {
		if errors.Is(err, errRequestNotSent) {
			return provpkg.CallResult{
				Outcome: provpkg.OutcomeTerminal,
				Err:     backoff.Permanent(err),
			}
		}
		return provpkg.CallResult{
			Outcome:    provpkg.OutcomeProviderError,
			Err:        err,
			StatusCode: res.statusCode,
		}
	}
	bodyBytes := res.body

	if res.statusCode == http.StatusTooManyRequests || res.statusCode >= http.StatusInternalServerError {
		return provpkg.CallResult{
			Outcome:    provpkg.OutcomeProviderError,
			Err:        fmt.Errorf("%w: %d %s", provpkg.ErrUnexpectedStatus, res.statusCode, string(bodyBytes)),
			StatusCode: res.statusCode,
			Body:       bodyBytes,
			RetryAfter: res.retryAfter,
		}
	}

	if res.statusCode != http.StatusOK {
		return provpkg.CallResult{
			Outcome: provpkg.OutcomeTerminal,
			Err:     backoff.Permanent(fmt.Errorf("%w: %d %s", provpkg.ErrUnexpectedStatus, res.statusCode, string(bodyBytes))),
		}
	}

	var ollamaResp Response
	if err := json.Unmarshal(bodyBytes, &ollamaResp); err != nil {
		return provpkg.CallResult{
			Outcome: provpkg.OutcomeTerminal,
			Err:     backoff.Permanent(fmt.Errorf("%w: parsing response: %w", provpkg.ErrInvalidResponsePart, err)),
		}
	}
	t.logger.Debug("ollama call meta",
		"model", t.model,
		"done", ollamaResp.Done,
		"done_reason", ollamaResp.DoneReason,
		"prompt_eval_count", ollamaResp.PromptEvalCount,
		"eval_count", ollamaResp.EvalCount,
		"total_duration_ms", ollamaResp.TotalDuration/1e6,
		"response_len", len(ollamaResp.Response),
	)

	if ollamaResp.DoneReason == doneReasonLength {
		total := ollamaResp.PromptEvalCount + ollamaResp.EvalCount

		limit := t.numPredict
		if limit <= 0 {
			limit = defaultNumPredict
		}

		if ollamaResp.EvalCount >= limit {
			return provpkg.CallResult{
				Outcome: provpkg.OutcomeTerminal,
				Err: backoff.Permanent(fmt.Errorf(
					"%w: model used %d eval tokens (model=%s, num_predict=%d); the model tried to generate a response longer than allowed, increase ollama.num_predict in config.yaml",
					ErrOutputLimitExhausted, ollamaResp.EvalCount, t.model, limit,
				)),
			}
		}

		var hint string
		remaining := t.contextLength - ollamaResp.PromptEvalCount
		if t.contextLength > 0 && remaining > 0 && ollamaResp.EvalCount >= remaining {
			hint = "the response was too long to fit in the remaining context window; try lowering ollama.num_predict, or reduce prompt/EXIF size, or use a model with a larger context window"
		} else {
			hint = "reduce EXIF data via llm_context_budgets or llm_max_exif_bytes in config.yaml, or use a model with a larger context window"
		}

		return provpkg.CallResult{
			Outcome: provpkg.OutcomeTerminal,
			Err: backoff.Permanent(fmt.Errorf(
				"%w: model used %d prompt tokens + %d eval tokens = %d total (model=%s, context_length=%d); %s",
				ErrContextWindowExhausted, ollamaResp.PromptEvalCount, ollamaResp.EvalCount, total, t.model, t.contextLength, hint,
			)),
		}
	}
	return provpkg.CallResult{Outcome: provpkg.OutcomeSuccess, Text: ollamaResp.Response}
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
