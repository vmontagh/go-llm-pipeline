package vectorstore_test

import (
	"strconv"
	"sync"
	"testing"

	"github.com/vmontagh/go-llm-pipeline/pkg/vectorstore"
)

// TestMemoryStoreConcurrentAccess exercises concurrent Add and Nearest calls so
// the race detector can flag unsynchronized access. Run with `go test -race`.
func TestMemoryStoreConcurrentAccess(t *testing.T) {
	t.Parallel()

	s := vectorstore.NewMemoryStore(vectorstore.WithInitialCapacity(64))
	const workers = 8
	const perWorker = 50

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				id := strconv.Itoa(w) + "-" + strconv.Itoa(i)
				if err := s.Add(t.Context(), vectorstore.Item{ID: id, Vector: vectorstore.Vector{float64(w + 1), float64(i + 1)}}); err != nil {
					t.Errorf("Add(%q) = %v, want nil", id, err)
					return
				}
				if _, err := s.Nearest(t.Context(), vectorstore.Vector{1, 1}, 5); err != nil {
					t.Errorf("Nearest() = %v, want nil", err)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	if got, want := s.Len(), workers*perWorker; got != want {
		t.Fatalf("Len() = %d, want %d", got, want)
	}
}
