package vectorstore_test

import (
	"math"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// nan returns a quiet NaN for building invalid test vectors.
func nan() float64 { return math.NaN() }

// cmpEqErr compares errors by identity via errors.Is.
func cmpEqErr() cmp.Option { return cmpopts.EquateErrors() }
