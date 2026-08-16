package vectorstore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/vmontagh/go-llm-pipeline/pkg/vectorstore"
)

func mustAdd(t *testing.T, s *vectorstore.MemoryStore, items ...vectorstore.Item) {
	t.Helper()
	for _, item := range items {
		if err := s.Add(t.Context(), item); err != nil {
			t.Fatalf("Add(%q) = %v, want nil", item.ID, err)
		}
	}
}

func TestMemoryStoreAddValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		item vectorstore.Item
		want error
	}{
		{
			name: "valid",
			item: vectorstore.Item{ID: "a", Vector: vectorstore.Vector{1, 0}},
		},
		{
			name: "emptyID",
			item: vectorstore.Item{Vector: vectorstore.Vector{1, 0}},
			want: vectorstore.ErrEmptyID,
		},
		{
			name: "emptyVector",
			item: vectorstore.Item{ID: "a"},
			want: vectorstore.ErrEmptyVector,
		},
		{
			name: "nanComponent",
			item: vectorstore.Item{ID: "a", Vector: vectorstore.Vector{nan(), 0}},
			want: vectorstore.ErrInvalidVector,
		},
		{
			name: "zeroMagnitude",
			item: vectorstore.Item{ID: "a", Vector: vectorstore.Vector{0, 0}},
			want: vectorstore.ErrInvalidVector,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := vectorstore.NewMemoryStore()
			got := s.Add(t.Context(), tc.item)
			if !cmp.Equal(got, tc.want, cmpEqErr()) {
				t.Fatalf("Add(%+v) = %v, want %v", tc.item, got, tc.want)
			}
		})
	}
}

func TestMemoryStoreAddDimensionMismatch(t *testing.T) {
	t.Parallel()

	s := vectorstore.NewMemoryStore()
	mustAdd(t, s, vectorstore.Item{ID: "a", Vector: vectorstore.Vector{1, 0}})

	err := s.Add(t.Context(), vectorstore.Item{ID: "b", Vector: vectorstore.Vector{1, 0, 0}})
	if !errors.Is(err, vectorstore.ErrDimensionMismatch) {
		t.Fatalf("Add(mismatched dim) = %v, want ErrDimensionMismatch", err)
	}
}

func TestMemoryStoreAddUpsert(t *testing.T) {
	t.Parallel()

	s := vectorstore.NewMemoryStore()
	mustAdd(t, s,
		vectorstore.Item{ID: "a", Vector: vectorstore.Vector{1, 0}},
		vectorstore.Item{ID: "a", Vector: vectorstore.Vector{0, 1}, Metadata: map[string]string{"v": "2"}},
	)

	if got, want := s.Len(), 1; got != want {
		t.Fatalf("Len() = %d, want %d", got, want)
	}

	got, err := s.Nearest(t.Context(), vectorstore.Vector{0, 1}, 1)
	if err != nil {
		t.Fatalf("Nearest() = %v, want nil", err)
	}
	if len(got) != 1 || got[0].Item.Metadata["v"] != "2" {
		t.Fatalf("Nearest() = %+v, want upserted item with metadata v=2", got)
	}
	if diff := cmp.Diff(1.0, got[0].Score); diff != "" {
		t.Fatalf("upserted vector score mismatch (-want +got):\n%s", diff)
	}
}

func TestMemoryStoreNearestOrdering(t *testing.T) {
	t.Parallel()

	s := vectorstore.NewMemoryStore()
	mustAdd(t, s,
		vectorstore.Item{ID: "east", Vector: vectorstore.Vector{1, 0}},
		vectorstore.Item{ID: "north", Vector: vectorstore.Vector{0, 1}},
		vectorstore.Item{ID: "west", Vector: vectorstore.Vector{-1, 0}},
	)

	got, err := s.Nearest(t.Context(), vectorstore.Vector{2, 0}, 3)
	if err != nil {
		t.Fatalf("Nearest() = %v, want nil", err)
	}

	wantIDs := []string{"east", "north", "west"}
	gotIDs := make([]string, len(got))
	for i, n := range got {
		gotIDs[i] = n.Item.ID
	}
	if diff := cmp.Diff(wantIDs, gotIDs); diff != "" {
		t.Fatalf("Nearest() id order mismatch (-want +got):\n%s", diff)
	}
	if got[0].Score <= got[1].Score || got[1].Score <= got[2].Score {
		t.Fatalf("Nearest() scores not strictly descending: %v", []float64{got[0].Score, got[1].Score, got[2].Score})
	}
}

func TestMemoryStoreNearestTieBreakByID(t *testing.T) {
	t.Parallel()

	s := vectorstore.NewMemoryStore()
	// Both vectors are identical to the query, so scores tie and ID breaks it.
	mustAdd(t, s,
		vectorstore.Item{ID: "zebra", Vector: vectorstore.Vector{1, 0}},
		vectorstore.Item{ID: "alpha", Vector: vectorstore.Vector{1, 0}},
	)

	got, err := s.Nearest(t.Context(), vectorstore.Vector{1, 0}, 2)
	if err != nil {
		t.Fatalf("Nearest() = %v, want nil", err)
	}
	wantIDs := []string{"alpha", "zebra"}
	gotIDs := []string{got[0].Item.ID, got[1].Item.ID}
	if diff := cmp.Diff(wantIDs, gotIDs); diff != "" {
		t.Fatalf("tie-break order mismatch (-want +got):\n%s", diff)
	}
}

func TestMemoryStoreNearestLimit(t *testing.T) {
	t.Parallel()

	s := vectorstore.NewMemoryStore()
	mustAdd(t, s,
		vectorstore.Item{ID: "a", Vector: vectorstore.Vector{1, 0}},
		vectorstore.Item{ID: "b", Vector: vectorstore.Vector{0, 1}},
	)

	got, err := s.Nearest(t.Context(), vectorstore.Vector{1, 0}, 1)
	if err != nil {
		t.Fatalf("Nearest() = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("Nearest(limit=1) returned %d results, want 1", len(got))
	}

	// A limit larger than the store returns only what exists.
	got, err = s.Nearest(t.Context(), vectorstore.Vector{1, 0}, 10)
	if err != nil {
		t.Fatalf("Nearest() = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("Nearest(limit=10) returned %d results, want 2", len(got))
	}
}

func TestMemoryStoreNearestInvalidLimit(t *testing.T) {
	t.Parallel()

	s := vectorstore.NewMemoryStore()
	mustAdd(t, s, vectorstore.Item{ID: "a", Vector: vectorstore.Vector{1, 0}})

	for _, limit := range []int{0, -1} {
		_, err := s.Nearest(t.Context(), vectorstore.Vector{1, 0}, limit)
		if !errors.Is(err, vectorstore.ErrInvalidLimit) {
			t.Fatalf("Nearest(limit=%d) = %v, want ErrInvalidLimit", limit, err)
		}
	}
}

func TestMemoryStoreNearestDimensionMismatch(t *testing.T) {
	t.Parallel()

	s := vectorstore.NewMemoryStore()
	mustAdd(t, s, vectorstore.Item{ID: "a", Vector: vectorstore.Vector{1, 0}})

	_, err := s.Nearest(t.Context(), vectorstore.Vector{1, 0, 0}, 1)
	if !errors.Is(err, vectorstore.ErrDimensionMismatch) {
		t.Fatalf("Nearest(wrong dim) = %v, want ErrDimensionMismatch", err)
	}
}

func TestMemoryStoreInputDefensiveCopy(t *testing.T) {
	t.Parallel()

	s := vectorstore.NewMemoryStore()
	vec := vectorstore.Vector{1, 0}
	meta := map[string]string{"k": "v"}
	mustAdd(t, s, vectorstore.Item{ID: "a", Vector: vec, Metadata: meta})

	// Mutate the caller's inputs after Add; the store must be unaffected.
	vec[0] = 999
	meta["k"] = "mutated"

	got, err := s.Nearest(t.Context(), vectorstore.Vector{1, 0}, 1)
	if err != nil {
		t.Fatalf("Nearest() = %v, want nil", err)
	}
	if diff := cmp.Diff(1.0, got[0].Score); diff != "" {
		t.Fatalf("stored vector was mutated via input alias (-want +got):\n%s", diff)
	}
	if got[0].Item.Metadata["k"] != "v" {
		t.Fatalf("stored metadata mutated via input alias: %v", got[0].Item.Metadata)
	}
}

func TestMemoryStoreOutputDefensiveCopy(t *testing.T) {
	t.Parallel()

	s := vectorstore.NewMemoryStore()
	mustAdd(t, s, vectorstore.Item{ID: "a", Vector: vectorstore.Vector{1, 0}, Metadata: map[string]string{"k": "v"}})

	got, err := s.Nearest(t.Context(), vectorstore.Vector{1, 0}, 1)
	if err != nil {
		t.Fatalf("Nearest() = %v, want nil", err)
	}
	got[0].Item.Vector[0] = 999
	got[0].Item.Metadata["k"] = "mutated"

	again, err := s.Nearest(t.Context(), vectorstore.Vector{1, 0}, 1)
	if err != nil {
		t.Fatalf("Nearest() = %v, want nil", err)
	}
	if diff := cmp.Diff(1.0, again[0].Score); diff != "" {
		t.Fatalf("stored vector mutated via returned alias (-want +got):\n%s", diff)
	}
	if again[0].Item.Metadata["k"] != "v" {
		t.Fatalf("stored metadata mutated via returned alias: %v", again[0].Item.Metadata)
	}
}

func TestMemoryStoreAddCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	s := vectorstore.NewMemoryStore()
	err := s.Add(ctx, vectorstore.Item{ID: "a", Vector: vectorstore.Vector{1, 0}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Add(cancelled ctx) = %v, want context.Canceled", err)
	}
}

func TestMemoryStoreNearestCancelled(t *testing.T) {
	t.Parallel()

	s := vectorstore.NewMemoryStore()
	mustAdd(t, s, vectorstore.Item{ID: "a", Vector: vectorstore.Vector{1, 0}})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := s.Nearest(ctx, vectorstore.Vector{1, 0}, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Nearest(cancelled ctx) = %v, want context.Canceled", err)
	}
}

func TestMemoryStoreNearestCancelledEmptyStore(t *testing.T) {
	t.Parallel()

	// An empty store scans nothing, so cancellation must be detected before the
	// scan loop or it would be silently ignored.
	s := vectorstore.NewMemoryStore()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := s.Nearest(ctx, vectorstore.Vector{1, 0}, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Nearest(cancelled ctx, empty store) = %v, want context.Canceled", err)
	}
}
