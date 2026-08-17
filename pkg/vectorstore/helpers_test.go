package vectorstore_test

import "math"

// nan returns a quiet NaN for building invalid test vectors.
func nan() float64 { return math.NaN() }
