package observ

import (
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
)

// Metrics is a small, dependency-free registry of named monotonic counters. It
// is safe for concurrent use: counters are stored behind atomics so hot-path
// increments never block one another, and the registry map is guarded by a
// mutex only when a new counter name is first seen.
//
// Metrics is intended to be created in main() and injected into the components
// that report to it, keeping package-level mutable state out of those
// components. The zero value is not usable; construct one with NewMetrics.
type Metrics struct {
	mu       sync.RWMutex
	counters map[string]*atomic.Int64
}

// NewMetrics returns an empty counter registry ready for use.
func NewMetrics() *Metrics {
	return &Metrics{counters: make(map[string]*atomic.Int64)}
}

// counter returns the atomic backing name, creating it on first use.
func (m *Metrics) counter(name string) *atomic.Int64 {
	m.mu.RLock()
	c, ok := m.counters[name]
	m.mu.RUnlock()
	if ok {
		return c
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	// Re-check under the write lock: another goroutine may have created it
	// between releasing the read lock and acquiring the write lock.
	if c, ok := m.counters[name]; ok {
		return c
	}
	c = new(atomic.Int64)
	m.counters[name] = c
	return c
}

// Add increases the counter named name by delta.
func (m *Metrics) Add(name string, delta int64) {
	m.counter(name).Add(delta)
}

// Inc increases the counter named name by one.
func (m *Metrics) Inc(name string) {
	m.counter(name).Add(1)
}

// Value returns the current value of the counter named name, or zero if no
// counter with that name has been recorded.
func (m *Metrics) Value(name string) int64 {
	m.mu.RLock()
	c, ok := m.counters[name]
	m.mu.RUnlock()
	if !ok {
		return 0
	}
	return c.Load()
}

// Snapshot returns a point-in-time copy of every counter keyed by name. The
// returned map is owned by the caller and safe to mutate.
func (m *Metrics) Snapshot() map[string]int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]int64, len(m.counters))
	for name, c := range m.counters {
		out[name] = c.Load()
	}
	return out
}

// Handler returns an http.Handler that serves the current snapshot as a JSON
// object of name to value. encoding/json marshals string-keyed maps with sorted
// keys, so the output is deterministic. Mount it on the observability server to
// expose counters over HTTP.
func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body, err := json.MarshalIndent(m.Snapshot(), "", "  ")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// A write error means the client connection is already gone; there is
		// nothing further the handler can do about it.
		if _, err := w.Write(body); err != nil {
			return
		}
	})
}
