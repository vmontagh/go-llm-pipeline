// Package analyzer provides the Template Method and Builder patterns for LLM-based analysis.
package analyzer

import (
	"errors"
)

// Event defines the contract for any event emitted by an Analyzer.
type Event interface {
	EventType() string
	ItemID() string
	Message() string
	IsError() bool
}

var (
	// ErrParsing is returned when the analyzer output cannot be parsed.
	ErrParsing = errors.New("failed to parse analyzer output")
	// ErrRetryableResponse is returned when the LLM response is retryable.
	ErrRetryableResponse = errors.New("retryable LLM response")
	// ErrMalformedResponse is returned when the API response is malformed.
	ErrMalformedResponse = errors.New("malformed response returned")
)
