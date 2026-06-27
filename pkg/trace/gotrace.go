// Package trace provides tracing functionality.
package trace

import (
	"context"
	gotrace "runtime/trace"
)

// Tracer defines the interface for tracing.
type Tracer interface {
	StartTask(ctx context.Context, name string) (context.Context, interface{ End() })
	StartRegion(ctx context.Context, name string) interface{ End() }
}

// GoTracer implements the Tracer interface using Go's built-in runtime/trace.
type GoTracer struct{}

// NewGoTracer returns a new Tracer implementation using runtime/trace.
func NewGoTracer() Tracer {
	return &GoTracer{}
}

// StartTask starts a new tracing task.
func (t *GoTracer) StartTask(ctx context.Context, name string) (taskCtx context.Context, task interface{ End() }) {
	taskCtx, raw := gotrace.NewTask(ctx, name)
	return taskCtx, &goTask{task: raw}
}

// StartRegion starts a new tracing region.
func (t *GoTracer) StartRegion(ctx context.Context, name string) interface{ End() } {
	region := gotrace.StartRegion(ctx, name)
	return &goRegion{region: region}
}

type goTask struct {
	task *gotrace.Task
}

func (t *goTask) End() {
	if t.task != nil {
		t.task.End()
	}
}

type goRegion struct {
	region *gotrace.Region
}

func (r *goRegion) End() {
	if r.region != nil {
		r.region.End()
	}
}
