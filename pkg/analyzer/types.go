package analyzer

import (
"context"
)

// Tracer defines the tracing interface used by the Template and handleProviderError.
type Tracer interface {
StartRegion(ctx context.Context, name string) interface{ End() }
}
