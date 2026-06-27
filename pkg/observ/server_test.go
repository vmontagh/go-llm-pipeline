package observ

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestServer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		addr    string
		wantErr error
	}{
		{
			name:    "start and stop on ephemeral port",
			addr:    "127.0.0.1:0",
			wantErr: nil, // server.go ignores http.ErrServerClosed and returns nil
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := NewServer(tt.addr)

			errCh := make(chan error, 1)
			go func() {
				errCh <- srv.Start()
			}()

			time.Sleep(50 * time.Millisecond)

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			stopErr := srv.Stop(ctx)
			if stopErr != nil {
				t.Fatalf("Stop failed: %v", stopErr)
			}

			err := <-errCh
			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Start() error mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
