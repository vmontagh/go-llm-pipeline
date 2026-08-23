package vectorstore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/vmontagh/go-llm-pipeline/pkg/vectorstore"
	"github.com/vmontagh/go-llm-pipeline/pkg/vectorstore/vectorstoretest"
)

func mustAdd(t *testing.T, s *vectorstore.MemoryStore, items ...vectorstore.Item) {
	t.Helper()
	for _, item := range items {
		if err := s.Add(t.Context(), item); err != nil {
			t.Fatalf("Add(%q) = %v, want nil", item.ID, err)
		}
	}
}

// Compile-time assertion that MemoryStore satisfies the conformance contract.
// The Store interface lives in vectorstoretest, its sole consumer.
var _ vectorstoretest.Store = (*vectorstore.MemoryStore)(nil)

// TestMemoryStoreConformance verifies MemoryStore against the shared Store
// contract. The behavior-agnostic cases (Add/Nearest round-trips, ordering,
// validation, upsert, defensive copies, and cancellation) live in the
// conformance harness so the forthcoming persistent backend reuses them.
func TestMemoryStoreConformance(t *testing.T) {
	t.Parallel()
	vectorstoretest.RunStoreConformance(t, func(tb testing.TB) vectorstoretest.Store {
		tb.Helper()
		return vectorstore.NewMemoryStore()
	})
}

// TestMemoryStoreLenAfterUpsert verifies the concrete MemoryStore.Len counter,
// which the interface-only conformance harness cannot observe: re-adding the
// same ID replaces in place and must leave Len at 1, guarding against a counter
// that increments on upsert.
func TestMemoryStoreLenAfterUpsert(t *testing.T) {
	t.Parallel()
	s := vectorstore.NewMemoryStore()
	mustAdd(t, s,
		vectorstore.Item{ID: "a", Vector: vectorstore.Vector{1, 0}, Metadata: map[string]string{"v": "1"}},
		vectorstore.Item{ID: "a", Vector: vectorstore.Vector{0, 1}, Metadata: map[string]string{"v": "2"}},
	)
	if got := s.Len(); got != 1 {
		t.Errorf("Len() after upsert = %d, want 1", got)
	}
}

func TestMemoryStoreNearestScoreEndpoints(t *testing.T) {
	t.Parallel()

	// Identical and opposite vectors must report exactly ±1 despite the small
	// error independent normalization introduces.
	s := vectorstore.NewMemoryStore()
	mustAdd(t, s,
		vectorstore.Item{ID: "same", Vector: vectorstore.Vector{1, 1}},
		vectorstore.Item{ID: "opposite", Vector: vectorstore.Vector{-1, -1}},
	)

	got, err := s.Nearest(t.Context(), vectorstore.Vector{1, 1}, 2)
	if err != nil {
		t.Fatalf("Nearest() = %v, want nil", err)
	}
	want := map[string]float64{"same": 1, "opposite": -1}
	for _, n := range got {
		if diff := cmp.Diff(want[n.Item.ID], n.Score); diff != "" {
			t.Errorf("Nearest() score for %q mismatch (-want +got):\n%s", n.Item.ID, diff)
		}
	}
}

func TestMemoryStoreGet(t *testing.T) {
	t.Parallel()

	item := vectorstore.Item{ID: "a", Vector: vectorstore.Vector{1, 0}, Metadata: map[string]string{"k": "v"}}

	tests := []struct {
		name    string
		id      string
		ctx     func(t *testing.T) context.Context
		want    vectorstore.Item
		wantOK  bool
		wantErr error
	}{
		{
			name:   "present id",
			id:     "a",
			want:   item,
			wantOK: true,
		},
		{
			name:   "unknown id is not an error",
			id:     "missing",
			want:   vectorstore.Item{},
			wantOK: false,
		},
		{
			name:    "empty id",
			id:      "",
			wantErr: vectorstore.ErrEmptyID,
		},
		{
			name: "cancelled context",
			id:   "a",
			ctx: func(t *testing.T) context.Context {
				t.Helper()
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				return ctx
			},
			wantErr: context.Canceled,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := vectorstore.NewMemoryStore()
			mustAdd(t, s, item)

			ctx := t.Context()
			if tc.ctx != nil {
				ctx = tc.ctx(t)
			}

			got, ok, err := s.Get(ctx, tc.id)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Get(%q) error = %v, want %v", tc.id, err, tc.wantErr)
			}
			if tc.wantErr != nil {
				return
			}
			if ok != tc.wantOK {
				t.Errorf("Get(%q) ok = %t, want %t", tc.id, ok, tc.wantOK)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Get(%q) item mismatch (-want +got):\n%s", tc.id, diff)
			}
		})
	}
}

func TestMemoryStoreGetDefensiveCopy(t *testing.T) {
	t.Parallel()
	s := vectorstore.NewMemoryStore()
	want := vectorstore.Item{ID: "a", Vector: vectorstore.Vector{1, 0}, Metadata: map[string]string{"k": "v"}}
	mustAdd(t, s, want)

	got, ok, err := s.Get(t.Context(), "a")
	if err != nil || !ok {
		t.Fatalf("Get() = (ok %t, err %v), want (true, nil)", ok, err)
	}
	got.Vector[0] = 999
	got.Metadata["k"] = "mutated"

	again, ok, err := s.Get(t.Context(), "a")
	if err != nil || !ok {
		t.Fatalf("Get() = (ok %t, err %v), want (true, nil)", ok, err)
	}
	if diff := cmp.Diff(want, again); diff != "" {
		t.Errorf("stored item mutated via returned alias (-want +got):\n%s", diff)
	}
}
