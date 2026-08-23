package observ

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
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

			var lc net.ListenConfig
			l, err := lc.Listen(context.Background(), "tcp", tt.addr)
			if err != nil {
				t.Fatalf("failed to listen: %v", err)
			}

			errCh := make(chan error, 1)
			go func() {
				errCh <- srv.StartListener(l)
			}()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			stopErr := srv.Stop(ctx)
			if stopErr != nil {
				t.Fatalf("Stop failed: %v", stopErr)
			}

			err = <-errCh
			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("StartListener() error mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestServerHandleMountsMetrics(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	m.Add("hits", 3)
	m.Inc("misses")

	srv := NewServer("127.0.0.1:0")
	srv.Handle("/metrics", m.Handler())

	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.StartListener(l)
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if stopErr := srv.Stop(ctx); stopErr != nil {
			t.Errorf("Stop() = %v, want nil", stopErr)
		}
		if serveErr := <-errCh; serveErr != nil {
			t.Errorf("StartListener() = %v, want nil", serveErr)
		}
	})

	url := fmt.Sprintf("http://%s/metrics", l.Addr().String())
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, http.NoBody)
	if err != nil {
		t.Fatalf("NewRequestWithContext() = %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s = %v", url, err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			t.Errorf("Body.Close() = %v, want nil", closeErr)
		}
	}()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body = %v", err)
	}

	var got map[string]int64
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("Unmarshal(%q) = %v", body, err)
	}
	want := map[string]int64{"hits": 3, "misses": 1}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("GET /metrics body mismatch (-want +got):\n%s", diff)
	}
}
