package observ_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/vmontagh/go-llm-pipeline/pkg/observ"
)

func TestMetricsSnapshot(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ops  func(m *observ.Metrics)
		want map[string]int64
	}{
		{
			name: "empty registry",
			ops:  func(*observ.Metrics) {},
			want: map[string]int64{},
		},
		{
			name: "inc creates and increments",
			ops: func(m *observ.Metrics) {
				m.Inc("hits")
				m.Inc("hits")
				m.Inc("misses")
			},
			want: map[string]int64{"hits": 2, "misses": 1},
		},
		{
			name: "add accumulates",
			ops: func(m *observ.Metrics) {
				m.Add("bytes", 10)
				m.Add("bytes", 5)
			},
			want: map[string]int64{"bytes": 15},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := observ.NewMetrics()
			tt.ops(m)
			if diff := cmp.Diff(tt.want, m.Snapshot()); diff != "" {
				t.Errorf("Snapshot() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestMetricsValue(t *testing.T) {
	t.Parallel()
	m := observ.NewMetrics()
	m.Add("known", 7)

	if got, want := m.Value("known"), int64(7); got != want {
		t.Errorf("Value(%q) = %d, want %d", "known", got, want)
	}
	if got, want := m.Value("unknown"), int64(0); got != want {
		t.Errorf("Value(%q) = %d, want %d", "unknown", got, want)
	}
}

func TestMetricsHandler(t *testing.T) {
	t.Parallel()
	m := observ.NewMetrics()
	m.Add("misses", 3)
	m.Add("hits", 1)

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", http.NoBody))

	if got, want := rec.Code, http.StatusOK; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	if got, want := rec.Header().Get("Content-Type"), "application/json"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}

	var got map[string]int64
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	want := map[string]int64{"hits": 1, "misses": 3}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("handler body mismatch (-want +got):\n%s", diff)
	}
}

func TestMetricsConcurrentInc(t *testing.T) {
	t.Parallel()
	m := observ.NewMetrics()

	const goroutines, perGoroutine = 16, 1000
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			for range perGoroutine {
				m.Inc("counter")
			}
		}()
	}
	wg.Wait()

	if got, want := m.Value("counter"), int64(goroutines*perGoroutine); got != want {
		t.Errorf("Value(counter) = %d, want %d", got, want)
	}
}
