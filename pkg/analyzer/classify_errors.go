package analyzer

import (
	"errors"

	"github.com/vmontagh/go-llm-pipeline/pkg/policy"
	"google.golang.org/grpc/codes"
)

// Code returns the canonical gRPC status code for err
func Code(err error) codes.Code {
	c := policy.Code(err)
	if c != codes.Internal {
		return c
	}
	switch {
	case errors.Is(err, ErrParsing), errors.Is(err, ErrMalformedResponse):
		return codes.InvalidArgument
	case errors.Is(err, ErrRetryableResponse):
		return codes.Unavailable
	}
	return codes.Internal
}

// IsRetryable reports whether err falls into a class that some retry loop is expected to handle.
func IsRetryable(err error) bool {
	switch Code(err) {
	case codes.ResourceExhausted, codes.Unavailable, codes.FailedPrecondition:
		return true
	default:
		return false
	}
}

// IsLongRetryable reports whether err belongs to a quota or rate-limit class.
func IsLongRetryable(err error) bool {
	c := Code(err)
	return c == codes.ResourceExhausted || c == codes.FailedPrecondition
}

// isAnalyzerQuotaClass reports whether err is in a quota or rate-limit family
func isAnalyzerQuotaClass(err error) bool {
	c := Code(err)
	return c == codes.ResourceExhausted || c == codes.FailedPrecondition
}
