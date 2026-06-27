package strategy

import (
	"context"
)

// Generator defines the contract for an LLM generator.
type Generator[R any] interface {
	Analyze(ctx context.Context, prompt string, imageData []byte, jobID string, publish func(eventType, itemID, message string, isError bool)) (R, error)
	Name() string
}

// JudgeResponse holds the structured output of a Judge evaluation.
type JudgeResponse struct {
	SelectedDraftIndex int    `json:"selected_draft_index" yaml:"selected_draft_index"`
	Approved           bool   `json:"approved" yaml:"approved"`
	Feedback           string `json:"feedback" yaml:"feedback"`
}

// Judge defines the contract for an LLM judge that evaluates drafts.
type Judge interface {
	Analyze(ctx context.Context, prompt string, imageData []byte, jobID string, publish func(eventType, itemID, message string, isError bool)) (*JudgeResponse, error)
	Name() string
}

// Logger defines the logging interface used by strategy.
type Logger interface {
	Debugf(format string, v ...any)
	Warnf(format string, v ...any)
}

// Tracer defines the tracing interface used by strategy.
type Tracer interface {
	StartRegion(ctx context.Context, name string) interface{ End() }
}

// noOpLogger is a logger that does nothing.
type noOpLogger struct{}

func (noOpLogger) Debugf(string, ...any) {}
func (noOpLogger) Warnf(string, ...any)  {}

// noOpTracer is a tracer that does nothing.
type noOpTracer struct{}

func (noOpTracer) StartRegion(ctx context.Context, name string) interface{ End() } {
	return noOpRegion{}
}

type noOpRegion struct{}

func (noOpRegion) End() {}
