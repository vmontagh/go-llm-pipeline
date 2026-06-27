package trace

import "context"

// NoOpTracer implements the Tracer interface but does nothing.
type NoOpTracer struct{}

// NewNoOpTracer creates a new NoOpTracer.
func NewNoOpTracer() Tracer {
	return &NoOpTracer{}
}

// StartTask implements Tracer.StartTask without performing any actual tracing.
func (t *NoOpTracer) StartTask(ctx context.Context, _ string) (taskCtx context.Context, task interface{ End() }) {
	return ctx, &noOpTask{}
}

// StartRegion implements Tracer.StartRegion without performing any actual tracing.
func (t *NoOpTracer) StartRegion(_ context.Context, _ string) interface{ End() } {
	return &noOpRegion{}
}

type noOpTask struct{}

func (t *noOpTask) End() {}

type noOpRegion struct{}

func (r *noOpRegion) End() {}
