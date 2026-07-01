package analyzer

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/cenkalti/backoff/v4"

	"github.com/vmontagh/go-llm-pipeline/pkg/provider"
)

const handleProviderErrorRegion = "HandleProviderError"

func escalateTier(tier ErrorTier, err error, rc provider.RetryContext, policy RetryPolicy) ErrorTier {
	thresholdMet := func(t ErrorTier) bool {
		tp := policy.Lookup(t)
		if tp.EscalateAfterAttempts > 0 && rc.Attempt >= tp.EscalateAfterAttempts {
			return true
		}
		if tp.EscalateAfter > 0 && rc.Elapsed >= tp.EscalateAfter {
			return true
		}
		return false
	}
	switch tier {
	case TierShortTransient, TierShortRateLimit:
		if thresholdMet(tier) {
			return TierLongTransientBounded
		}
	case TierLongTransientBounded:
		if isAnalyzerQuotaClass(err) && thresholdMet(tier) {
			return TierLongTransientInfinite
		}
	}
	return tier
}

func classifyAndCooldown[T any](lease *provider.Lease[T], tier ErrorTier, policy RetryPolicy, retryAfter time.Duration, err error) error {
	tp := policy.Lookup(tier)
	lease.MarkCooldown(tp, retryAfter)
	wrapped := WithCode(TierCode(tier, err), err)
	switch tier {
	case TierShortTransient, TierShortRateLimit:
		return wrapped
	default:
		return backoff.Permanent(wrapped)
	}
}

type providerErrorInfo struct {
	Provider        string
	Model           string
	JobID           string
	CredentialIdx   int
	CredentialCount int
	ResponseBody    []byte
	Publish         func(eventType, itemID, message string, isError bool)
	Logger          *slog.Logger
	Tracer          Tracer
}

type handleProviderErrorRequest[T any] struct {
	Lease      *provider.Lease[T]
	Tier       ErrorTier
	Err        error
	Policy     RetryPolicy
	RetryAfter time.Duration
	RC         provider.RetryContext
	Info       providerErrorInfo
}

func handleProviderError[T any](ctx context.Context, req handleProviderErrorRequest[T]) error {
	defer req.Info.Tracer.StartRegion(ctx, handleProviderErrorRegion).End()

	tier := escalateTier(req.Tier, req.Err, req.RC, req.Policy)
	tp := req.Policy.Lookup(tier)
	req.Lease.MarkCooldown(tp, req.RetryAfter)

	effectiveCooldown := tp.Cooldown
	if tp.HonorRetryAfter && req.RetryAfter > effectiveCooldown {
		effectiveCooldown = req.RetryAfter
	}

	switch tier {
	case TierLongTransientInfinite, TierLongTransientBounded, TierShortRateLimit:
		msg := fmt.Sprintf("%s credential %d/%d cooled for %v after rate-limit/quota: %v",
			req.Info.Provider, req.Info.CredentialIdx+1, req.Info.CredentialCount, effectiveCooldown, req.Err)
		if len(req.Info.ResponseBody) > 0 {
			msg = fmt.Sprintf("%s: %s", msg, string(req.Info.ResponseBody))
		}
		if req.Info.Publish != nil {
			req.Info.Publish(EventLLMRetrying, req.Info.JobID, msg, false)
		}
		req.Info.Logger.WarnContext(ctx, "credential cooled after rate-limit/quota",
			"provider", req.Info.Provider,
			"credential", req.Info.CredentialIdx+1,
			"total", req.Info.CredentialCount,
			"cooldown", effectiveCooldown,
			"err", req.Err,
		)
	case TierShortTransient:
		msg := fmt.Sprintf("Retrying %s due to transient error: %v", req.Info.Model, req.Err)
		if req.Info.Publish != nil {
			req.Info.Publish(EventLLMRetrying, req.Info.JobID, msg, true)
		}
		req.Info.Logger.DebugContext(ctx, "retrying due to transient error",
			"model", req.Info.Model,
			"err", req.Err,
		)
	case TierTerminal:
		req.Info.Logger.DebugContext(ctx, "terminal error",
			"provider", req.Info.Provider,
			"credential", req.Info.CredentialIdx+1,
			"total", req.Info.CredentialCount,
			"err", req.Err,
		)
	}

	wrapped := WithCode(TierCode(tier, req.Err), req.Err)
	switch tier {
	case TierShortTransient, TierShortRateLimit:
		return wrapped
	default:
		return backoff.Permanent(wrapped)
	}
}
