package vectorstore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/vmontagh/go-llm-pipeline/pkg/vectorstore"
)

func TestMemoryStoreDelete(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		seed        []vectorstore.Item
		id          string
		wantRemoved bool
		wantErr     error
		wantLen     int
	}{
		{
			name:        "removes existing item",
			seed:        []vectorstore.Item{{ID: "a", Vector: vectorstore.Vector{1, 0}}},
			id:          "a",
			wantRemoved: true,
			wantLen:     0,
		},
		{
			name:        "missing id is not an error",
			seed:        []vectorstore.Item{{ID: "a", Vector: vectorstore.Vector{1, 0}}},
			id:          "b",
			wantRemoved: false,
			wantLen:     1,
		},
		{
			name:    "empty id is rejected",
			id:      "",
			wantErr: vectorstore.ErrEmptyID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := vectorstore.NewMemoryStore()
			mustAdd(t, store, tt.seed...)

			removed, err := store.Delete(t.Context(), tt.id)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Delete(%q) error = %v, want %v", tt.id, err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			if removed != tt.wantRemoved {
				t.Errorf("Delete(%q) removed = %t, want %t", tt.id, removed, tt.wantRemoved)
			}
			if got := store.Len(); got != tt.wantLen {
				t.Errorf("Len() after Delete(%q) = %d, want %d", tt.id, got, tt.wantLen)
			}
		})
	}
}

func TestMemoryStoreDeleteRetainsDimension(t *testing.T) {
	t.Parallel()
	store := vectorstore.NewMemoryStore()
	mustAdd(t, store, vectorstore.Item{ID: "a", Vector: vectorstore.Vector{1, 0}})

	if _, err := store.Delete(t.Context(), "a"); err != nil {
		t.Fatalf("Delete(a) = %v, want nil", err)
	}

	// The store committed to dimension 2, so a mismatched vector must still be
	// rejected even though the store is now empty.
	err := store.Add(t.Context(), vectorstore.Item{ID: "b", Vector: vectorstore.Vector{1, 0, 0}})
	if !errors.Is(err, vectorstore.ErrDimensionMismatch) {
		t.Errorf("Add(mismatched) = %v, want %v", err, vectorstore.ErrDimensionMismatch)
	}
}

func TestMemoryStoreDeleteCancelled(t *testing.T) {
	t.Parallel()
	store := vectorstore.NewMemoryStore()
	mustAdd(t, store, vectorstore.Item{ID: "a", Vector: vectorstore.Vector{1, 0}})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := store.Delete(ctx, "a"); !errors.Is(err, context.Canceled) {
		t.Errorf("Delete(cancelled) = %v, want %v", err, context.Canceled)
	}
}
