package sqlitestore

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/vmontagh/go-llm-pipeline/pkg/vectorstore"
)

// float64Bytes is the width in bytes of one little-endian IEEE-754 float64.
const float64Bytes = 8

// encodeVector packs v as little-endian IEEE-754 float64 values. Encoding copies
// each component into a fresh buffer, so the stored bytes are inherently
// independent of the caller's slice.
func encodeVector(v vectorstore.Vector) []byte {
	buf := make([]byte, len(v)*float64Bytes)
	for i, x := range v {
		binary.LittleEndian.PutUint64(buf[i*float64Bytes:], math.Float64bits(x))
	}
	return buf
}

// decodeVector reverses encodeVector. It returns [ErrCorruptVector] when the
// blob length is not a whole number of float64 values.
func decodeVector(b []byte) (vectorstore.Vector, error) {
	if len(b)%float64Bytes != 0 {
		return nil, fmt.Errorf("%w: length %d not a multiple of %d", ErrCorruptVector, len(b), float64Bytes)
	}
	v := make(vectorstore.Vector, len(b)/float64Bytes)
	for i := range v {
		v[i] = math.Float64frombits(binary.LittleEndian.Uint64(b[i*float64Bytes:]))
	}
	return v, nil
}
