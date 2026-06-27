package analyzer

import (
	"context"
)

// Logger defines the logging interface used by the Template and handleProviderError.
type Logger interface {
	Debugf(format string, v ...any)
	Warnf(format string, v ...any)
}

// Tracer defines the tracing interface used by the Template and handleProviderError.
type Tracer interface {
	StartRegion(ctx context.Context, name string) interface{ End() }
}
