package vectorstore_test

import (
	"math"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/pkg/vectorstore"
)

func TestNormalize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		vector  vectorstore.Vector
		want    vectorstore.Vector
		wantErr error
	}{
		{
			name:   "already unit",
			vector: vectorstore.Vector{1, 0},
			want:   vectorstore.Vector{1, 0},
		},
		{
			name:   "scaled to unit length",
			vector: vectorstore.Vector{3, 4},
			want:   vectorstore.Vector{0.6, 0.8},
		},
		{
			name:    "empty",
			vector:  vectorstore.Vector{},
			wantErr: vectorstore.ErrEmptyVector,
		},
		{
			name:    "non-finite component",
			vector:  vectorstore.Vector{nan(), 1},
			wantErr: vectorstore.ErrInvalidVector,
		},
		{
			name:    "infinite component",
			vector:  vectorstore.Vector{math.Inf(1), 1},
			wantErr: vectorstore.ErrInvalidVector,
		},
		{
			name:    "zero magnitude",
			vector:  vectorstore.Vector{0, 0},
			wantErr: vectorstore.ErrInvalidVector,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := vectorstore.Normalize(tc.vector)
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("Normalize(%v) error mismatch (-want +got):\n%s", tc.vector, diff)
			}
			if tc.wantErr != nil {
				return
			}
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateApprox(0, 1e-9)); diff != "" {
				t.Errorf("Normalize(%v) = %v, want %v (-want +got):\n%s", tc.vector, got, tc.want, diff)
			}
			if l := length(got); math.Abs(l-1) > 1e-9 {
				t.Errorf("Normalize(%v) length = %v, want 1", tc.vector, l)
			}
		})
	}
}

func TestCosine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		a       vectorstore.Vector
		b       vectorstore.Vector
		want    float64
		wantErr error
	}{
		{
			name: "identical",
			a:    vectorstore.Vector{1, 2},
			b:    vectorstore.Vector{1, 2},
			want: 1,
		},
		{
			name: "identical unnormalized different magnitudes",
			a:    vectorstore.Vector{1, 2},
			b:    vectorstore.Vector{2, 4},
			want: 1,
		},
		{
			name: "antipodal",
			a:    vectorstore.Vector{1, 0},
			b:    vectorstore.Vector{-1, 0},
			want: -1,
		},
		{
			name: "orthogonal",
			a:    vectorstore.Vector{1, 0},
			b:    vectorstore.Vector{0, 1},
			want: 0,
		},
		{
			name:    "unequal length",
			a:       vectorstore.Vector{1, 0},
			b:       vectorstore.Vector{1, 0, 0},
			wantErr: vectorstore.ErrDimensionMismatch,
		},
		{
			name:    "empty input",
			a:       vectorstore.Vector{},
			b:       vectorstore.Vector{},
			wantErr: vectorstore.ErrEmptyVector,
		},
		{
			name:    "zero magnitude first input",
			a:       vectorstore.Vector{0, 0},
			b:       vectorstore.Vector{1, 0},
			wantErr: vectorstore.ErrInvalidVector,
		},
		{
			name:    "non-finite first input",
			a:       vectorstore.Vector{nan(), 0},
			b:       vectorstore.Vector{1, 0},
			wantErr: vectorstore.ErrInvalidVector,
		},
		{
			name:    "zero magnitude second input",
			a:       vectorstore.Vector{1, 0},
			b:       vectorstore.Vector{0, 0},
			wantErr: vectorstore.ErrInvalidVector,
		},
		{
			name:    "non-finite second input",
			a:       vectorstore.Vector{1, 0},
			b:       vectorstore.Vector{nan(), 0},
			wantErr: vectorstore.ErrInvalidVector,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := vectorstore.Cosine(tc.a, tc.b)
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("Cosine(%v, %v) error mismatch (-want +got):\n%s", tc.a, tc.b, diff)
			}
			if tc.wantErr != nil {
				return
			}
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateApprox(0, 1e-9)); diff != "" {
				t.Errorf("Cosine(%v, %v) = %v, want %v (-want +got):\n%s", tc.a, tc.b, got, tc.want, diff)
			}
		})
	}
}

func TestCosineUnit(t *testing.T) {
	t.Parallel()

	// invSqrt2 is the unit-length component of a 45-degree vector, used to build
	// already-normalized inputs.
	const invSqrt2 = 0.7071067811865476

	tests := []struct {
		name    string
		a       vectorstore.Vector
		b       vectorstore.Vector
		want    float64
		wantErr error
	}{
		{
			name: "identical",
			a:    vectorstore.Vector{1, 0},
			b:    vectorstore.Vector{1, 0},
			want: 1,
		},
		{
			name: "antipodal",
			a:    vectorstore.Vector{1, 0},
			b:    vectorstore.Vector{-1, 0},
			want: -1,
		},
		{
			name: "orthogonal",
			a:    vectorstore.Vector{1, 0},
			b:    vectorstore.Vector{0, 1},
			want: 0,
		},
		{
			name: "forty-five degrees",
			a:    vectorstore.Vector{1, 0},
			b:    vectorstore.Vector{invSqrt2, invSqrt2},
			want: invSqrt2,
		},
		{
			name:    "unequal length",
			a:       vectorstore.Vector{1, 0},
			b:       vectorstore.Vector{1, 0, 0},
			wantErr: vectorstore.ErrDimensionMismatch,
		},
		{
			name:    "empty",
			a:       vectorstore.Vector{},
			b:       vectorstore.Vector{},
			wantErr: vectorstore.ErrEmptyVector,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := vectorstore.CosineUnit(tc.a, tc.b)
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("CosineUnit(%v, %v) error mismatch (-want +got):\n%s", tc.a, tc.b, diff)
			}
			if tc.wantErr != nil {
				return
			}
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateApprox(0, 1e-9)); diff != "" {
				t.Errorf("CosineUnit(%v, %v) = %v, want %v (-want +got):\n%s", tc.a, tc.b, got, tc.want, diff)
			}
			// For unit-length inputs, CosineUnit matches Cosine exactly.
			cosineGot, cosineErr := vectorstore.Cosine(tc.a, tc.b)
			if cosineErr != nil {
				t.Fatalf("Cosine(%v, %v) = %v, want nil", tc.a, tc.b, cosineErr)
			}
			if diff := cmp.Diff(cosineGot, got, cmpopts.EquateApprox(0, 1e-9)); diff != "" {
				t.Errorf("CosineUnit vs Cosine for unit inputs mismatch (-Cosine +CosineUnit):\n%s", diff)
			}
		})
	}
}

// length returns the Euclidean length of v, used to assert that Normalize
// produces unit vectors.
func length(v vectorstore.Vector) float64 {
	var sum float64
	for _, x := range v {
		sum += x * x
	}
	return math.Sqrt(sum)
}
