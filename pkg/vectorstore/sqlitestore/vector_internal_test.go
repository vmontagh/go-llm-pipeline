package sqlitestore

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/vmontagh/go-llm-pipeline/pkg/vectorstore"
)

func TestEncodeDecodeVectorRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		vec  vectorstore.Vector
	}{
		{name: "single", vec: vectorstore.Vector{1}},
		{name: "pair", vec: vectorstore.Vector{3, 4}},
		{name: "negatives and fractions", vec: vectorstore.Vector{-1.5, 0.25, 1024.5}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := decodeVector(encodeVector(tc.vec))
			if err != nil {
				t.Fatalf("decodeVector(encodeVector(%v)) = %v, want nil", tc.vec, err)
			}
			if diff := cmp.Diff(tc.vec, got); diff != "" {
				t.Errorf("round-trip mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDecodeVectorCorrupt(t *testing.T) {
	t.Parallel()

	// A 3-byte blob is not a whole number of 8-byte float64 values.
	_, err := decodeVector([]byte{1, 2, 3})
	if !errors.Is(err, ErrCorruptVector) {
		t.Errorf("decodeVector(short blob) = %v, want ErrCorruptVector", err)
	}
}
