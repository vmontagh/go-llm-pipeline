package vectorstore

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// entry is one stored item plus its cached unit-length vector for scoring.
type entry struct {
	item       Item
	normalized Vector
}

// memoryStoreConfig holds the options applied by NewMemoryStore.
type memoryStoreConfig struct {
	initialCapacity int
}

// MemoryStoreOption customizes a MemoryStore at construction time.
type MemoryStoreOption func(*memoryStoreConfig)

// WithInitialCapacity preallocates room for n items, avoiding rehashing when
// the approximate size is known up front. Non-positive values are ignored.
func WithInitialCapacity(n int) MemoryStoreOption {
	return func(c *memoryStoreConfig) {
		if n > 0 {
			c.initialCapacity = n
		}
	}
}

// MemoryStore is a concurrency-safe, in-memory vector store. It holds every
// vector in a map keyed by Item.ID and scans all items on each query, which
// suits tests, small batches, and transient workflows. The zero value is not
// usable; construct one with NewMemoryStore.
type MemoryStore struct {
	mu    sync.RWMutex
	dim   int
	items map[string]entry
}

// NewMemoryStore returns an empty MemoryStore. The store fixes its vector
// dimension on the first successful Add and rejects later vectors of a
// different dimension.
func NewMemoryStore(opts ...MemoryStoreOption) *MemoryStore {
	var cfg memoryStoreConfig
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return &MemoryStore{
		items: make(map[string]entry, cfg.initialCapacity),
	}
}

// Len returns the number of items currently stored.
func (s *MemoryStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.items)
}

// Add inserts item, replacing any existing item with the same ID. The stored
// item is a deep copy, so mutating item after Add does not affect the store.
func (s *MemoryStore) Add(ctx context.Context, item Item) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("vectorstore: add cancelled: %w", err)
	}
	if item.ID == "" {
		return fmt.Errorf("adding item: %w", ErrEmptyID)
	}
	normalized, err := normalize(item.Vector)
	if err != nil {
		return fmt.Errorf("adding item %q: %w", item.ID, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("vectorstore: add cancelled: %w", err)
	}
	if s.dim == 0 {
		s.dim = len(item.Vector)
	} else if len(item.Vector) != s.dim {
		return fmt.Errorf("adding item %q: %w: got %d, want %d", item.ID, ErrDimensionMismatch, len(item.Vector), s.dim)
	}
	s.items[item.ID] = entry{item: copyItem(item), normalized: normalized}
	return nil
}

// Nearest returns up to limit neighbors ordered by descending cosine
// similarity, breaking ties by ascending Item.ID. Returned items are deep
// copies. It returns ErrInvalidLimit when limit is not positive.
func (s *MemoryStore) Nearest(ctx context.Context, vector Vector, limit int) ([]Neighbor, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("querying nearest: %w", err)
	}
	if limit <= 0 {
		return nil, fmt.Errorf("querying nearest: %w: %d", ErrInvalidLimit, limit)
	}
	query, err := normalize(vector)
	if err != nil {
		return nil, fmt.Errorf("querying nearest: %w", err)
	}

	neighbors, err := s.snapshot(ctx, query, len(vector))
	if err != nil {
		return nil, err
	}

	// Sorting only touches the defensive copies in neighbors, so it runs after
	// the read lock is released to avoid blocking concurrent Add calls.
	sort.Slice(neighbors, func(i, j int) bool {
		if neighbors[i].Score != neighbors[j].Score {
			return neighbors[i].Score > neighbors[j].Score
		}
		return neighbors[i].Item.ID < neighbors[j].Item.ID
	})
	if len(neighbors) > limit {
		neighbors = neighbors[:limit]
	}
	return neighbors, nil
}

// snapshot scores every stored item against the normalized query under the read
// lock and returns freshly copied neighbors. It enforces the store's dimension
// and honors cancellation, including on an empty store where the scan loop would
// otherwise never run. Sorting is deliberately left to the caller so it happens
// after the lock is released.
func (s *MemoryStore) snapshot(ctx context.Context, query Vector, dim int) ([]Neighbor, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("querying nearest: %w", err)
	}
	if s.dim != 0 && dim != s.dim {
		return nil, fmt.Errorf("querying nearest: %w: got %d, want %d", ErrDimensionMismatch, dim, s.dim)
	}

	neighbors := make([]Neighbor, 0, len(s.items))
	for _, e := range s.items {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("querying nearest: scan cancelled: %w", err)
		}
		neighbors = append(neighbors, Neighbor{
			Item:  copyItem(e.item),
			Score: cosine(query, e.normalized),
		})
	}
	return neighbors, nil
}
