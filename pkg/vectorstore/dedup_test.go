package vectorstore_test

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/vmontagh/go-llm-pipeline/pkg/vectorstore"
)

func TestFindDuplicates(t *testing.T) {
	t.Parallel()

	items := []vectorstore.Item{
		{ID: "a", Vector: vectorstore.Vector{1, 0}},
		{ID: "b", Vector: vectorstore.Vector{1, 0}},    // identical to a
		{ID: "c", Vector: vectorstore.Vector{0.99, 0}}, // near a and b
		{ID: "d", Vector: vectorstore.Vector{0, 1}},    // orthogonal
	}

	got, err := vectorstore.FindDuplicates(t.Context(), items, 0.999)
	if err != nil {
		t.Fatalf("FindDuplicates() = %v, want nil", err)
	}

	// c normalizes to the same direction as a and b, so all of a,b,c pair up.
	wantPairs := [][2]string{{"a", "b"}, {"a", "c"}, {"b", "c"}}
	gotPairs := make([][2]string, len(got))
	for i, d := range got {
		gotPairs[i] = [2]string{d.A.ID, d.B.ID}
	}
	if diff := cmp.Diff(wantPairs, gotPairs); diff != "" {
		t.Fatalf("duplicate pairs mismatch (-want +got):\n%s", diff)
	}
}

func TestFindDuplicatesDeterministicOrder(t *testing.T) {
	t.Parallel()

	// Deliberately unsorted input; output must be sorted by (A.ID, B.ID) with
	// A holding the smaller ID.
	items := []vectorstore.Item{
		{ID: "z", Vector: vectorstore.Vector{1, 0}},
		{ID: "m", Vector: vectorstore.Vector{1, 0}},
		{ID: "a", Vector: vectorstore.Vector{1, 0}},
	}

	got, err := vectorstore.FindDuplicates(t.Context(), items, 0.5)
	if err != nil {
		t.Fatalf("FindDuplicates() = %v, want nil", err)
	}
	wantPairs := [][2]string{{"a", "m"}, {"a", "z"}, {"m", "z"}}
	gotPairs := make([][2]string, len(got))
	for i, d := range got {
		gotPairs[i] = [2]string{d.A.ID, d.B.ID}
	}
	if diff := cmp.Diff(wantPairs, gotPairs); diff != "" {
		t.Fatalf("duplicate ordering mismatch (-want +got):\n%s", diff)
	}
}

func TestFindDuplicatesNoMatches(t *testing.T) {
	t.Parallel()

	items := []vectorstore.Item{
		{ID: "a", Vector: vectorstore.Vector{1, 0}},
		{ID: "b", Vector: vectorstore.Vector{0, 1}},
	}
	got, err := vectorstore.FindDuplicates(t.Context(), items, 0.9)
	if err != nil {
		t.Fatalf("FindDuplicates() = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf("FindDuplicates() = %+v, want no duplicates", got)
	}
}

func TestFindDuplicatesInvalidThreshold(t *testing.T) {
	t.Parallel()

	items := []vectorstore.Item{{ID: "a", Vector: vectorstore.Vector{1, 0}}}
	for _, threshold := range []float64{-1.5, 1.5} {
		_, err := vectorstore.FindDuplicates(t.Context(), items, threshold)
		if !errors.Is(err, vectorstore.ErrInvalidThreshold) {
			t.Fatalf("FindDuplicates(threshold=%v) = %v, want ErrInvalidThreshold", threshold, err)
		}
	}
}

func TestFindDuplicatesDimensionMismatch(t *testing.T) {
	t.Parallel()

	items := []vectorstore.Item{
		{ID: "a", Vector: vectorstore.Vector{1, 0}},
		{ID: "b", Vector: vectorstore.Vector{1, 0, 0}},
	}
	_, err := vectorstore.FindDuplicates(t.Context(), items, 0.9)
	if !errors.Is(err, vectorstore.ErrDimensionMismatch) {
		t.Fatalf("FindDuplicates(mismatched dims) = %v, want ErrDimensionMismatch", err)
	}
}

func TestFindDuplicatesInvalidItem(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		items []vectorstore.Item
		want  error
	}{
		{
			name:  "emptyIDSingleton",
			items: []vectorstore.Item{{Vector: vectorstore.Vector{1, 0}}},
			want:  vectorstore.ErrEmptyID,
		},
		{
			name:  "emptyVectorSingleton",
			items: []vectorstore.Item{{ID: "a"}},
			want:  vectorstore.ErrEmptyVector,
		},
		{
			name:  "zeroMagnitudeSingleton",
			items: []vectorstore.Item{{ID: "a", Vector: vectorstore.Vector{0, 0}}},
			want:  vectorstore.ErrInvalidVector,
		},
		{
			name: "duplicateID",
			items: []vectorstore.Item{
				{ID: "a", Vector: vectorstore.Vector{1, 0}},
				{ID: "a", Vector: vectorstore.Vector{0, 1}},
			},
			want: vectorstore.ErrDuplicateID,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := vectorstore.FindDuplicates(t.Context(), tc.items, 0.9)
			if !errors.Is(err, tc.want) {
				t.Fatalf("FindDuplicates(%s) = %v, want %v", tc.name, err, tc.want)
			}
		})
	}
}
