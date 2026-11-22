package analyzer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"

	"github.com/vmontagh/go-llm-pipeline/pkg/provider"
	"github.com/vmontagh/go-llm-pipeline/pkg/trace"
)

const llmStartedEventType = "LLM_STARTED"

var ErrUnknownCallOutcome = errors.New("analyzer: provider returned unknown CallOutcome")
var ErrNilParser = errors.New("analyzer: NewTemplate called with nil parser")
var ErrNilProvider = errors.New("analyzer: NewTemplate called with nil provider")

type Template[T any, R any] struct {
	provider provider.Provider[T]
	parser   func(string) (R, error)

	logger          *slog.Logger
	tracer          Tracer
	policy          RetryPolicy
	maxElapsedTime  time.Duration
	maxParseRetries int
	backOff         backoff.BackOff
	shortCooldown   time.Duration
	scrubFn         func(string) string
	maxAssetBytes   int64
}

type templateConfig struct {
	logger          *slog.Logger
	tracer          Tracer
	policy          RetryPolicy
	maxElapsedTime  time.Duration
	maxParseRetries int
	backOff         backoff.BackOff
	shortCooldown   time.Duration
	scrubFn         func(string) string
	maxAssetBytes   int64
}

const defaultShortCooldownRetryThreshold = 5 * time.Second

type TemplateConfigurator interface {
	applyTemplate(*templateConfig)
}

type templateConfiguratorFunc func(*templateConfig)

func (f templateConfiguratorFunc) applyTemplate(c *templateConfig) { f(c) }

// WithTemplateLogger sets the *slog.Logger used for debug/warn messages.
func WithTemplateLogger(l *slog.Logger) TemplateConfigurator {
	return templateConfiguratorFunc(func(c *templateConfig) { c.logger = l })
}

func WithTemplateTracer(t Tracer) TemplateConfigurator {
	return templateConfiguratorFunc(func(c *templateConfig) { c.tracer = t })
}

func WithTemplateRetryPolicy(p RetryPolicy) TemplateConfigurator {
	return templateConfiguratorFunc(func(c *templateConfig) { c.policy = p })
}

func WithTemplateMaxElapsedTime(d time.Duration) TemplateConfigurator {
	return templateConfiguratorFunc(func(c *templateConfig) { c.maxElapsedTime = d })
}

func WithTemplateMaxParseRetries(r int) TemplateConfigurator {
	return templateConfiguratorFunc(func(c *templateConfig) { c.maxParseRetries = r })
}

func WithTemplateBackOff(b backoff.BackOff) TemplateConfigurator {
	return templateConfiguratorFunc(func(c *templateConfig) { c.backOff = b })
}

func WithTemplateShortCooldownRetryThreshold(d time.Duration) TemplateConfigurator {
	return templateConfiguratorFunc(func(c *templateConfig) { c.shortCooldown = d })
}

// WithTemplateScrubFunc sets a function applied to prompt and response text before they are
// emitted as debug log messages. Use it to redact PII or secrets (e.g. via pkg/scrub.Scrubber.Scrub)
// while still retaining full detail for local debugging. The scrub function is never applied to
// the values passed to the provider or parser.
func WithTemplateScrubFunc(fn func(string) string) TemplateConfigurator {
	return templateConfiguratorFunc(func(c *templateConfig) { c.scrubFn = fn })
}

// WithTemplateMaxAssetBytes bounds how many bytes AnalyzeAsset reads from an
// Asset payload. A value of zero disables the bound; a negative value is
// ignored. Defaults to DefaultMaxAssetBytes.
func WithTemplateMaxAssetBytes(n int64) TemplateConfigurator {
	return templateConfiguratorFunc(func(c *templateConfig) {
		if n >= 0 {
			c.maxAssetBytes = n
		}
	})
}

func NewTemplate[T any, R any](p provider.Provider[T], parser func(string) (R, error), opts ...TemplateConfigurator) (*Template[T, R], error) {
	if p == nil {
		return nil, ErrNilProvider
	}
	if parser == nil {
		return nil, ErrNilParser
	}
	cfg := templateConfig{
		logger:        slog.New(slog.DiscardHandler),
		tracer:        trace.NewNoOpTracer(),
		policy:        DefaultRetryPolicy(),
		shortCooldown: defaultShortCooldownRetryThreshold,
		scrubFn:       func(s string) string { return s },
		maxAssetBytes: DefaultMaxAssetBytes,
	}
	for _, o := range opts {
		if o != nil {
			o.applyTemplate(&cfg)
		}
	}
	return &Template[T, R]{
		provider:        p,
		parser:          parser,
		logger:          cfg.logger,
		tracer:          cfg.tracer,
		policy:          cfg.policy,
		maxElapsedTime:  cfg.maxElapsedTime,
		maxParseRetries: cfg.maxParseRetries,
		backOff:         cfg.backOff,
		shortCooldown:   cfg.shortCooldown,
		scrubFn:         cfg.scrubFn,
		maxAssetBytes:   cfg.maxAssetBytes,
	}, nil
}

func (t *Template[T, R]) Name() string { return t.provider.Name() }

func (t *Template[T, R]) Provider() provider.Provider[T] { return t.provider }

func (t *Template[T, R]) Analyze(
	ctx context.Context,
	prompt string,
	imageData []byte,
	jobID string,
	publish func(eventType, itemID, message string, isError bool),
) (R, error) {
	return t.run(ctx, &callOnceParams[T]{prompt: prompt, imageData: imageData, jobID: jobID, publish: publish})
}

// AnalyzeMessages is Analyze for a typed multi-turn conversation. The
// provider must implement provider.MessageCaller; otherwise it returns
// provider.ErrMessagesUnsupported.
func (t *Template[T, R]) AnalyzeMessages(
	ctx context.Context,
	msgs []provider.Message,
	jobID string,
	publish func(eventType, itemID, message string, isError bool),
) (R, error) {
	var zero R
	if err := provider.ValidateMessages(msgs); err != nil {
		return zero, fmt.Errorf("analyzing messages: %w", err)
	}
	mc, ok := provider.AsMessageCaller(t.provider)
	if !ok {
		return zero, fmt.Errorf("%w: %s", provider.ErrMessagesUnsupported, t.provider.ProviderLabel())
	}
	return t.run(ctx, &callOnceParams[T]{messages: msgs, caller: mc, jobID: jobID, publish: publish})
}

// run traces the analysis and retries callOnce until the parser accepts a
// response.
func (t *Template[T, R]) run(ctx context.Context, p *callOnceParams[T]) (R, error) {
	defer t.tracer.StartRegion(ctx, t.provider.TraceRegionName()).End()

	if p.publish != nil {
		p.publish(llmStartedEventType, p.jobID, "Started analysis using "+t.provider.ProviderLabel(), false)
	}

	retryStart := time.Now()
	attempt := 0
	return ExecuteWithRetries[R](ctx, &RetryConfig{
		ModelName:       t.provider.Model(),
		JobID:           p.jobID,
		Publish:         p.publish,
		Logger:          t.logger,
		MaxElapsedTime:  t.maxElapsedTime,
		MaxParseRetries: t.maxParseRetries,
		BackOff:         t.backOff,
	}, func() (string, error) {
		rc := provider.RetryContext{Attempt: attempt, Elapsed: time.Since(retryStart)}
		attempt++
		params := *p
		params.rc = rc
		return t.callOnce(ctx, &params)
	}, t.parser)
}

// AnalyzeAsset runs the analysis Template for a single Asset. It reads the
// asset payload (bounded by the configured maximum size and honoring ctx
// cancellation) and uses asset.ID to correlate published events. Textual assets
// (see MetadataContentType) are folded into the prompt; every other media type,
// including images, is forwarded to the provider as binary data. A nil payload
// is forwarded as no binary data.
func (t *Template[T, R]) AnalyzeAsset(
	ctx context.Context,
	prompt string,
	asset Asset,
	publish func(eventType, itemID, message string, isError bool),
) (R, error) {
	payload, err := asset.payloadBytes(ctx, t.maxAssetBytes)
	if err != nil {
		var zero R
		return zero, err
	}
	if isTextualContentType(asset.contentType()) {
		return t.Analyze(ctx, joinPromptPayload(prompt, payload), nil, asset.ID, publish)
	}
	return t.Analyze(ctx, prompt, payload, asset.ID, publish)
}

// joinPromptPayload appends a textual payload to prompt, separated by a blank
// line. Empty operands are handled without introducing stray separators.
func joinPromptPayload(prompt string, payload []byte) string {
	switch {
	case len(payload) == 0:
		return prompt
	case prompt == "":
		return string(payload)
	default:
		return prompt + "\n\n" + string(payload)
	}
}

// messagesText renders msgs as "role: text" lines for debug logs. Images
// are shown as "[image]".
func messagesText(msgs []provider.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(m.Role.String())
		b.WriteString(":")
		for _, part := range m.Parts {
			b.WriteString(" ")
			if len(part.Image) > 0 {
				b.WriteString("[image]")
				continue
			}
			b.WriteString(part.Text)
		}
		b.WriteString("\n")
	}
	return b.String()
}

type callOnceParams[T any] struct {
	prompt    string
	imageData []byte
	messages  []provider.Message // sent by caller, when set, instead of prompt and imageData
	caller    provider.MessageCaller[T]
	jobID     string
	publish   func(eventType, itemID, message string, isError bool)
	rc        provider.RetryContext
}

func (t *Template[T, R]) callOnce(ctx context.Context, p *callOnceParams[T]) (string, error) {
	prompt := p.prompt
	imageData := p.imageData
	jobID := p.jobID
	publish := p.publish
	rc := p.rc
	call := func(lease *provider.Lease[T]) provider.CallResult {
		return t.provider.Call(ctx, lease, prompt, imageData)
	}
	logText := prompt
	if p.caller != nil {
		call = func(lease *provider.Lease[T]) provider.CallResult {
			return p.caller.CallMessages(ctx, lease, p.messages)
		}
		logText = messagesText(p.messages)
	}
	lease, err := t.provider.Keys().Acquire(ctx)
	if err != nil {
		if errors.Is(err, provider.ErrMissingAPIKey) {
			return "", backoff.Permanent(fmt.Errorf("%w: %s", provider.ErrMissingAPIKey, t.provider.ProviderLabel()))
		}
		if errors.Is(err, provider.ErrAllKeysCoolingDown) {
			if minWait := t.provider.Keys().MinCooldown(); minWait <= t.shortCooldown {
				return "", err
			}
		}
		return "", backoff.Permanent(err)
	}

	scrubbed := t.scrubFn(logText)
	t.logger.DebugContext(ctx, "prompt sent",
		"jobID", jobID,
		"provider", t.provider.Name(),
		"len", len(logText),
		"body", strings.ReplaceAll(strings.TrimSpace(scrubbed), "\n", "\n\t"),
	)

	result := call(lease)

	scrubbedResp := t.scrubFn(result.Text)
	t.logger.DebugContext(ctx, "response received",
		"jobID", jobID,
		"provider", t.provider.Name(),
		"len", len(result.Text),
		"body", strings.ReplaceAll(strings.TrimSpace(scrubbedResp), "\n", "\n\t"),
	)

	switch result.Outcome {
	case provider.OutcomeSuccess:
		lease.Release()
		return result.Text, nil
	case provider.OutcomeTerminal:
		lease.Release()
		return "", result.Err
	case provider.OutcomeProviderError:
		if ctx.Err() != nil {
			lease.Release()
			return "", backoff.Permanent(fmt.Errorf("request canceled: %w", result.Err))
		}
		tier := ErrorTier(t.provider.ClassifyError(provider.ClassifyInput{
			Err:        result.Err,
			StatusCode: result.StatusCode,
			Body:       result.Body,
			RetryAfter: result.RetryAfter,
		}, rc))
		enriched := t.provider.EnrichError(provider.ErrorTier(tier), result.Err, result.Body)
		return "", handleProviderError(ctx, handleProviderErrorRequest[T]{
			Lease:      lease,
			Tier:       tier,
			Err:        enriched,
			Policy:     t.policy,
			RetryAfter: result.RetryAfter,
			RC:         rc,
			Info: providerErrorInfo{
				Provider:        t.provider.ProviderLabel(),
				Model:           t.provider.Model(),
				JobID:           jobID,
				CredentialIdx:   lease.Index,
				CredentialCount: t.provider.Keys().Len(),
				ResponseBody:    result.Body,
				Publish:         publish,
				Logger:          t.logger,
				Tracer:          t.tracer,
			},
		})
	default:
		lease.Release()
		return "", backoff.Permanent(fmt.Errorf("%w: provider %q outcome %d", ErrUnknownCallOutcome, t.provider.ProviderLabel(), result.Outcome))
	}
}
