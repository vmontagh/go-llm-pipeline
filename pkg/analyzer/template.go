package analyzer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"

	"github.com/vmontagh/go-llm-pipeline/pkg/provider"
	"github.com/vmontagh/go-llm-pipeline/pkg/logger"
	"github.com/vmontagh/go-llm-pipeline/pkg/trace"
)

const llmStartedEventType = "LLM_STARTED"

var ErrUnknownCallOutcome = errors.New("analyzer: provider returned unknown CallOutcome")
var ErrNilParser = errors.New("analyzer: NewTemplate called with nil parser")

type Template[T any, R any] struct {
	provider provider.Provider[T]
	parser   func(string) (R, error)

	logger          Logger
	tracer          Tracer
	policy          RetryPolicy
	maxElapsedTime  time.Duration
	maxParseRetries int
	backOff         backoff.BackOff
	shortCooldown   time.Duration
}

type templateConfig struct {
	logger          Logger
	tracer          Tracer
	policy          RetryPolicy
	maxElapsedTime  time.Duration
	maxParseRetries int
	backOff         backoff.BackOff
	shortCooldown   time.Duration
}

const defaultShortCooldownRetryThreshold = 5 * time.Second

type TemplateConfigurator interface {
	applyTemplate(*templateConfig)
}

type templateConfiguratorFunc func(*templateConfig)

func (f templateConfiguratorFunc) applyTemplate(c *templateConfig) { f(c) }

func WithTemplateLogger(l Logger) TemplateConfigurator {
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

func NewTemplate[T any, R any](p provider.Provider[T], parser func(string) (R, error), opts ...TemplateConfigurator) (*Template[T, R], error) {
	if parser == nil {
		return nil, ErrNilParser
	}
	cfg := templateConfig{
		logger:        logger.NewNoOpLogger(),
		tracer:        trace.NewNoOpTracer(),
		policy:        DefaultRetryPolicy(),
		shortCooldown: defaultShortCooldownRetryThreshold,
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
	defer t.tracer.StartRegion(ctx, t.provider.TraceRegionName()).End()

	if publish != nil {
		publish(llmStartedEventType, jobID, "Started analysis using "+t.provider.ProviderLabel(), false)
	}

	retryStart := time.Now()
	attempt := 0
	return ExecuteWithRetries[R](ctx, &RetryConfig{
		ModelName:       t.provider.Model(),
		JobID:           jobID,
		Publish:         publish,
		Logger:          t.logger,
		MaxElapsedTime:  t.maxElapsedTime,
		MaxParseRetries: t.maxParseRetries,
		BackOff:         t.backOff,
	}, func() (string, error) {
		rc := provider.RetryContext{Attempt: attempt, Elapsed: time.Since(retryStart)}
		attempt++
		return t.callOnce(ctx, &callOnceParams{
			prompt:    prompt,
			imageData: imageData,
			jobID:     jobID,
			publish:   publish,
			rc:        rc,
		})
	}, t.parser)
}

type callOnceParams struct {
	prompt    string
	imageData []byte
	jobID     string
	publish   func(eventType, itemID, message string, isError bool)
	rc        provider.RetryContext
}

func (t *Template[T, R]) callOnce(ctx context.Context, p *callOnceParams) (string, error) {
	prompt := p.prompt
	imageData := p.imageData
	jobID := p.jobID
	publish := p.publish
	rc := p.rc
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

	t.logger.Debugf("[%s] %s prompt sent (len=%d):\n\t%s", jobID, t.provider.Name(), len(prompt), strings.ReplaceAll(strings.TrimSpace(prompt), "\n", "\n\t"))

	result := t.provider.Call(ctx, lease, prompt, imageData)

	t.logger.Debugf("[%s] %s response received (len=%d):\n\t%s", jobID, t.provider.Name(), len(result.Text), strings.ReplaceAll(strings.TrimSpace(result.Text), "\n", "\n\t"))
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
