package analyzer

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/cenkalti/backoff/v4"
)

// retryConfig holds the parameters for executeWithRetries.
type retryConfig struct {
	ModelName       string
	JobID           string
	Publish         func(eventType, itemID, message string, isError bool)
	Logger          *slog.Logger
	MaxElapsedTime  time.Duration
	MaxParseRetries int
	BackOff         backoff.BackOff // Optional: for injecting logical time or max retries in tests
	Timer           backoff.Timer   // Optional: for injecting logical time timer in tests
}

func executeWithRetries[R any](
	ctx context.Context,
	cfg *retryConfig,
	generator func() (string, error),
	parser func(string) (R, error),
) (R, error) {

	if cfg == nil {
		cfg = &retryConfig{}
	}

	var parsedContent R
	parseAttempts := 0

	if cfg.MaxElapsedTime <= 0 {
		cfg.MaxElapsedTime = 2 * time.Minute
	}
	if cfg.MaxParseRetries <= 0 {
		cfg.MaxParseRetries = 3
	}

	operation := func() error {
		// 1. Generate text using the specific LLM integration
		content, err := generator()
		if err != nil {
			// The generator itself must wrap non-retryable errors in backoff.Permanent(err)
			return err
		}

		// 2. Parse the output text
		res, parseErr := parser(content)
		if parseErr != nil {
			parseAttempts++
			if parseAttempts >= cfg.MaxParseRetries {
				return backoff.Permanent(fmt.Errorf("parsing %s response after %d attempts: %w", cfg.ModelName, parseAttempts, parseErr))
			}
			if cfg.Publish != nil {
				cfg.Publish(eventLLMRetrying, cfg.JobID, fmt.Sprintf("Retrying %s due to parse error: %v", cfg.ModelName, parseErr), true)
			}
			if cfg.Logger != nil {
				cfg.Logger.Warn("retrying due to parse failure",
					"model", cfg.ModelName,
					"attempt", parseAttempts,
					"max", cfg.MaxParseRetries,
					"err", parseErr,
				)
			}
			return fmt.Errorf("parsing %s response: %w", cfg.ModelName, parseErr)
		}

		parsedContent = res
		return nil
	}

	// 3. Configure the backoff loop
	var b backoff.BackOff
	if cfg.BackOff != nil {
		b = cfg.BackOff
	} else {
		expBackoff := backoff.NewExponentialBackOff()
		expBackoff.MaxElapsedTime = cfg.MaxElapsedTime
		b = expBackoff
	}

	bCtx := backoff.WithContext(b, ctx)

	var err error
	if cfg.Timer != nil {
		err = backoff.RetryNotifyWithTimer(operation, bCtx, nil, cfg.Timer)
	} else {
		err = backoff.Retry(operation, bCtx)
	}

	if err != nil {
		var zero R
		return zero, fmt.Errorf("executing operation: %w", err)
	}

	return parsedContent, nil
}
