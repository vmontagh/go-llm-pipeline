package analyzer

import "github.com/vmontagh/go-llm-pipeline/pkg/provider"

// This file re-exports the provider sentinel errors so that analyzer
// callers that historically used `analyzer.ErrX` can keep doing so.
var (
	ErrMissingAPIKey       = provider.ErrMissingAPIKey
	ErrAllKeysCoolingDown  = provider.ErrAllKeysCoolingDown
	ErrQuotaExceeded       = provider.ErrQuotaExceeded
	ErrRateLimitExceeded   = provider.ErrRateLimitExceeded
	ErrEmptyCandidate      = provider.ErrEmptyCandidate
	ErrInvalidResponsePart = provider.ErrInvalidResponsePart
	ErrUnexpectedStatus    = provider.ErrUnexpectedStatus
)
