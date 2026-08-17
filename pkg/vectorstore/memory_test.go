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

// neighborIDs projects neighbors to their IDs for order-sensitive comparison.
func neighborIDs(neighbors []vectorstore.Neighbor) []string {
	if len(neighbors) == 0 {
		return nil
	}
	out := make([]string, len(neighbors))
	for i, n := range neighbors {
		out[i] = n.Item.ID
	}
	return out
}

func TestMemoryStoreAdd(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		seed   []vectorstore.Item
		item   vectorstore.Item
		cancel bool
		want   error
	}{
		{
			name: "valid",
			item: vectorstore.Item{ID: "a", Vector: vectorstore.Vector{1, 0}},
		},
		{
			name: "empty id",
			item: vectorstore.Item{Vector: vectorstore.Vector{1, 0}},
			want: vectorstore.ErrEmptyID,
		},
		{
			name: "empty vector",
			item: vectorstore.Item{ID: "a"},
			want: vectorstore.ErrEmptyVector,
		},
		{
			name: "nan component",
			item: vectorstore.Item{ID: "a", Vector: vectorstore.Vector{nan(), 0}},
			want: vectorstore.ErrInvalidVector,
		},
		{
			name: "zero magnitude",
			item: vectorstore.Item{ID: "a", Vector: vectorstore.Vector{0, 0}},
			want: vectorstore.ErrInvalidVector,
		},
		{
			name: "dimension mismatch",
			seed: []vectorstore.Item{{ID: "a", Vector: vectorstore.Vector{1, 0}}},
			item: vectorstore.Item{ID: "b", Vector: vectorstore.Vector{1, 0, 0}},
			want: vectorstore.ErrDimensionMismatch,
		},
		{
			name:   "cancelled context",
			item:   vectorstore.Item{ID: "a", Vector: vectorstore.Vector{1, 0}},
			cancel: true,
			want:   context.Canceled,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := vectorstore.NewMemoryStore()
			mustAdd(t, s, tc.seed...)

			ctx := t.Context()
			if tc.cancel {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			if err := s.Add(ctx, tc.item); !errors.Is(err, tc.want) {
				t.Errorf("Add(%+v) = %v, want %v", tc.item, err, tc.want)
			}
		})
	}
}

func TestMemoryStoreUpsert(t *testing.T) {
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
	if diff := cmp.Diff(1.0, got[0].Score); diff != "" {
		t.Errorf("upserted vector score mismatch (-want +got):\n%s", diff)
	}
	if got, want := got[0].Item.Metadata["v"], "2"; got != want {
		t.Errorf("upserted metadata v = %q, want %q", got, want)
	}
}

func TestMemoryStoreNearest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		items []vectorstore.Item
		query vectorstore.Vector
		limit int
		want  []string
	}{
		{
			name: "ordered by descending similarity",
			items: []vectorstore.Item{
				{ID: "east", Vector: vectorstore.Vector{1, 0}},
				{ID: "north", Vector: vectorstore.Vector{0, 1}},
				{ID: "west", Vector: vectorstore.Vector{-1, 0}},
			},
			query: vectorstore.Vector{2, 0},
			limit: 3,
			want:  []string{"east", "north", "west"},
		},
		{
			name: "ties broken by ascending id",
			items: []vectorstore.Item{
				{ID: "zebra", Vector: vectorstore.Vector{1, 0}},
				{ID: "alpha", Vector: vectorstore.Vector{1, 0}},
			},
			query: vectorstore.Vector{1, 0},
			limit: 2,
			want:  []string{"alpha", "zebra"},
		},
		{
			name: "limit truncates results",
			items: []vectorstore.Item{
				{ID: "a", Vector: vectorstore.Vector{1, 0}},
				{ID: "b", Vector: vectorstore.Vector{0, 1}},
			},
			query: vectorstore.Vector{1, 0},
			limit: 1,
			want:  []string{"a"},
		},
		{
			name: "limit larger than store returns all",
			items: []vectorstore.Item{
				{ID: "a", Vector: vectorstore.Vector{1, 0}},
				{ID: "b", Vector: vectorstore.Vector{0, 1}},
			},
			query: vectorstore.Vector{1, 0},
			limit: 10,
			want:  []string{"a", "b"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := vectorstore.NewMemoryStore()
			mustAdd(t, s, tc.items...)

			got, err := s.Nearest(t.Context(), tc.query, tc.limit)
			if err != nil {
				t.Fatalf("Nearest(%s) = %v, want nil", tc.name, err)
			}
			if diff := cmp.Diff(tc.want, neighborIDs(got)); diff != "" {
				t.Errorf("Nearest(%s) id order mismatch (-want +got):\n%s", tc.name, diff)
			}
		})
	}
}

func TestMemoryStoreNearestErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		seed   []vectorstore.Item
		query  vectorstore.Vector
		limit  int
		cancel bool
		want   error
	}{
		{
			name:  "zero limit",
			seed:  []vectorstore.Item{{ID: "a", Vector: vectorstore.Vector{1, 0}}},
			query: vectorstore.Vector{1, 0},
			limit: 0,
			want:  vectorstore.ErrInvalidLimit,
		},
		{
			name:  "negative limit",
			seed:  []vectorstore.Item{{ID: "a", Vector: vectorstore.Vector{1, 0}}},
			query: vectorstore.Vector{1, 0},
			limit: -1,
			want:  vectorstore.ErrInvalidLimit,
		},
		{
			name:  "dimension mismatch",
			seed:  []vectorstore.Item{{ID: "a", Vector: vectorstore.Vector{1, 0}}},
			query: vectorstore.Vector{1, 0, 0},
			limit: 1,
			want:  vectorstore.ErrDimensionMismatch,
		},
		{
			name:   "cancelled context",
			seed:   []vectorstore.Item{{ID: "a", Vector: vectorstore.Vector{1, 0}}},
			query:  vectorstore.Vector{1, 0},
			limit:  1,
			cancel: true,
			want:   context.Canceled,
		},
		{
			// An empty store scans nothing, so cancellation must be detected
			// before the scan loop or it would be silently ignored.
			name:   "cancelled context on empty store",
			query:  vectorstore.Vector{1, 0},
			limit:  1,
			cancel: true,
			want:   context.Canceled,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := vectorstore.NewMemoryStore()
			mustAdd(t, s, tc.seed...)

			ctx := t.Context()
			if tc.cancel {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			if _, err := s.Nearest(ctx, tc.query, tc.limit); !errors.Is(err, tc.want) {
				t.Errorf("Nearest(%s) = %v, want %v", tc.name, err, tc.want)
			}
		})
	}
}

func TestMemoryStoreDefensiveCopy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(in vectorstore.Item, out []vectorstore.Neighbor)
	}{
		{
			name: "input mutated after add",
			mutate: func(in vectorstore.Item, _ []vectorstore.Neighbor) {
				in.Vector[0] = 999
				in.Metadata["k"] = "mutated"
			},
		},
		{
			name: "output mutated after query",
			mutate: func(_ vectorstore.Item, out []vectorstore.Neighbor) {
				out[0].Item.Vector[0] = 999
				out[0].Item.Metadata["k"] = "mutated"
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := vectorstore.NewMemoryStore()
			in := vectorstore.Item{ID: "a", Vector: vectorstore.Vector{1, 0}, Metadata: map[string]string{"k": "v"}}
			mustAdd(t, s, in)

			out, err := s.Nearest(t.Context(), vectorstore.Vector{1, 0}, 1)
			if err != nil {
				t.Fatalf("Nearest() = %v, want nil", err)
			}
			tc.mutate(in, out)

			got, err := s.Nearest(t.Context(), vectorstore.Vector{1, 0}, 1)
			if err != nil {
				t.Fatalf("Nearest() = %v, want nil", err)
			}
			if diff := cmp.Diff(1.0, got[0].Score); diff != "" {
				t.Errorf("stored vector mutated via alias (-want +got):\n%s", diff)
			}
			if got, want := got[0].Item.Metadata["k"], "v"; got != want {
				t.Errorf("stored metadata k = %q, want %q", got, want)
			}
		})
	}
}
