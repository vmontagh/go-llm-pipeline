package trace

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestNoOpTracer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		execute   func(tracer Tracer)
		wantPanic bool
	}{
		{
			name: testTaskName,
			execute: func(tracer Tracer) {
				_, task := tracer.StartTask(context.Background(), "task1")
				task.End()
			},
			wantPanic: false,
		},
		{
			name: testRegionName,
			execute: func(tracer Tracer) {
				region := tracer.StartRegion(context.Background(), "region1")
				region.End()
			},
			wantPanic: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tracer := NewNoOpTracer()

			var gotPanic bool
			func() {
				defer func() {
					if r := recover(); r != nil {
						gotPanic = true
					}
				}()
				tt.execute(tracer)
			}()

			if diff := cmp.Diff(tt.wantPanic, gotPanic); diff != "" {
				t.Errorf("Panic state mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
