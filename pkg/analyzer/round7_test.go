package analyzer_test

import (
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"reflect"
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/vmontagh/go-llm-pipeline/pkg/analyzer"
)

// signedBounds returns the inclusive range of a signed integer of bits width as
// exact JSON-number literals, mirroring integerBounds so expectations stay
// architecture-correct rather than hardcoding 2^63.
func signedBounds(bits int) (lo, hi string) {
	one := big.NewInt(1)
	half := new(big.Int).Lsh(one, uint(bits-1))
	return new(big.Int).Neg(half).String(), new(big.Int).Sub(half, one).String()
}

// unsignedMax returns 2^bits-1 as a decimal string.
func unsignedMax(bits int) string {
	one := big.NewInt(1)
	return new(big.Int).Sub(new(big.Int).Lsh(one, uint(bits)), one).String()
}

// TestBoundIntersection covers T1: a min/max tag may only narrow the native
// width range, never widen it, so a schema-conforming value always decodes into T.
func TestBoundIntersection(t *testing.T) {
	t.Parallel()

	int8Min, int8Max := signedBounds(8)

	tests := []struct {
		name    string
		typ     reflect.Type
		tag     string
		key     string
		want    string
	}{
		{name: "max tag wider than native keeps native", typ: reflect.TypeOf(int8(0)), tag: "max=1000", key: analyzer.PropMaximum, want: int8Max},
		{name: "max tag narrower than native applies", typ: reflect.TypeOf(int8(0)), tag: "max=100", key: analyzer.PropMaximum, want: "100"},
		{name: "min tag wider than native keeps native", typ: reflect.TypeOf(int8(0)), tag: "min=-1000", key: analyzer.PropMinimum, want: int8Min},
		{name: "min tag narrower than native applies", typ: reflect.TypeOf(int16(0)), tag: "min=5", key: analyzer.PropMinimum, want: "5"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaOfField(t, tc.typ, "field", tc.tag)
			if err != nil {
				t.Fatalf("SchemaOf() error: %v", err)
			}
			if diff := cmp.Diff(json.Number(tc.want), prop(got, "field")[tc.key]); diff != "" {
				t.Errorf("%s bound mismatch (-want +got):\n%s", tc.key, diff)
			}
		})
	}
}

// TestFloat64Bounds covers T2: float64 carries representable bounds so an
// out-of-range value is a schema violation rather than an opaque decode failure.
func TestFloat64Bounds(t *testing.T) {
	t.Parallel()

	got, err := schemaOfField(t, reflect.TypeOf(float64(0)), "field", "")
	if err != nil {
		t.Fatalf("SchemaOf() error: %v", err)
	}
	wantMax := json.Number(strconv.FormatFloat(math.MaxFloat64, 'g', -1, 64))
	wantMin := json.Number(strconv.FormatFloat(-math.MaxFloat64, 'g', -1, 64))
	field := prop(got, "field")
	if diff := cmp.Diff(wantMax, field[analyzer.PropMaximum]); diff != "" {
		t.Errorf("maximum mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(wantMin, field[analyzer.PropMinimum]); diff != "" {
		t.Errorf("minimum mismatch (-want +got):\n%s", diff)
	}

	schema := map[string]any{
		analyzer.PropType:    analyzer.TypeNumber,
		analyzer.PropMinimum: wantMin,
		analyzer.PropMaximum: wantMax,
	}
	if err := analyzer.Validate(schema, []byte("1e400")); !errors.Is(err, analyzer.ErrSchemaValidation) {
		t.Errorf("Validate(1e400) error = %v, want ErrSchemaValidation", err)
	}
	if err := analyzer.Validate(schema, []byte("1.5")); err != nil {
		t.Errorf("Validate(1.5) error = %v, want nil", err)
	}
}

type shadowedInner struct {
	X chan int
}

// shadowedOuter promotes shadowedInner.X (a chan, unsupported) but shadows it
// with a direct X of a supported type, which encoding/json keeps.
type shadowedOuter struct {
	shadowedInner
	X int
}

type ambiguousA struct {
	X chan int
}

type ambiguousB struct {
	X chan int
}

// ambiguousOuter promotes X from two embeddings at the same depth with no tag,
// so encoding/json drops the name entirely.
type ambiguousOuter struct {
	ambiguousA
	ambiguousB
	Y int
}

// TestDominanceBeforeDerivation covers T3: schemas are derived only for the
// fields encoding/json keeps, so a shadowed or ambiguous unsupported-type field
// never fails derivation.
func TestDominanceBeforeDerivation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     any
		wantName  string
		wantType  string
		wantAbsent string
	}{
		{name: "direct field shadows promoted chan", value: shadowedOuter{}, wantName: "X", wantType: analyzer.TypeInteger},
		{name: "ambiguous chan pair dropped", value: ambiguousOuter{}, wantName: "Y", wantType: analyzer.TypeInteger, wantAbsent: "X"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := analyzer.SchemaOf(tc.value)
			if err != nil {
				t.Fatalf("SchemaOf() error: %v", err)
			}
			field := prop(got, tc.wantName)
			if diff := cmp.Diff(tc.wantType, field[analyzer.PropType]); diff != "" {
				t.Errorf("%s type mismatch (-want +got):\n%s", tc.wantName, diff)
			}
			if tc.wantAbsent != "" {
				if prop(got, tc.wantAbsent) != nil {
					t.Errorf("property %q present, want absent", tc.wantAbsent)
				}
			}
		})
	}
}

// TestValuedFlagDirective covers T4: the bare-flag directives required/optional
// reject a valued form so a typo cannot be silently dropped.
func TestValuedFlagDirective(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		tag     string
		wantErr error
	}{
		{name: "required with value rejected", tag: "required=true", wantErr: analyzer.ErrSchemaDerivation},
		{name: "optional with value rejected", tag: "optional=false", wantErr: analyzer.ErrSchemaDerivation},
		{name: "bare required accepted", tag: "required"},
		{name: "bare optional accepted", tag: "optional"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := schemaOfField(t, reflect.TypeOf(""), "field", tc.tag)
			if tc.wantErr == nil {
				if err != nil {
					t.Errorf("SchemaOf() error = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("SchemaOf() error = %v, want errors.Is %v", err, tc.wantErr)
			}
		})
	}
}

// TestEmptyEnumInvalid covers T5: a present-but-empty enum is an invalid schema
// that permits no value, while an absent enum leaves the instance unconstrained.
func TestEmptyEnumInvalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		schema  map[string]any
		data    string
		wantErr bool
	}{
		{
			name:    "empty enum rejects any value",
			schema:  map[string]any{analyzer.PropType: analyzer.TypeString, analyzer.PropEnum: []any{}},
			data:    `"x"`,
			wantErr: true,
		},
		{
			name:   "absent enum unconstrained",
			schema: map[string]any{analyzer.PropType: analyzer.TypeString},
			data:   `"x"`,
		},
		{
			name:   "non-empty enum accepts member",
			schema: map[string]any{analyzer.PropType: analyzer.TypeString, analyzer.PropEnum: []any{"x"}},
			data:   `"x"`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := analyzer.Validate(tc.schema, []byte(tc.data))
			if tc.wantErr {
				if !errors.Is(err, analyzer.ErrSchemaValidation) {
					t.Errorf("Validate() error = %v, want ErrSchemaValidation", err)
				}
				return
			}
			if err != nil {
				t.Errorf("Validate() error = %v, want nil", err)
			}
		})
	}
}

// TestPlatformSizedBounds covers T6: platform-sized kinds derive bounds from
// strconv.IntSize, so expectations are derived the same way rather than assuming
// a fixed width.
func TestPlatformSizedBounds(t *testing.T) {
	t.Parallel()

	intMin, intMax := signedBounds(strconv.IntSize)
	uintMax := unsignedMax(strconv.IntSize)

	tests := []struct {
		name    string
		typ     reflect.Type
		wantMin string
		wantMax string
	}{
		{name: "int uses platform width", typ: reflect.TypeOf(int(0)), wantMin: intMin, wantMax: intMax},
		{name: "uint uses platform width", typ: reflect.TypeOf(uint(0)), wantMin: "0", wantMax: uintMax},
		{name: "uintptr stays unsigned platform width", typ: reflect.TypeOf(uintptr(0)), wantMin: "0", wantMax: uintMax},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaOfField(t, tc.typ, "field", "")
			if err != nil {
				t.Fatalf("SchemaOf() error: %v", err)
			}
			field := prop(got, "field")
			if diff := cmp.Diff(json.Number(tc.wantMin), field[analyzer.PropMinimum]); diff != "" {
				t.Errorf("minimum mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(json.Number(tc.wantMax), field[analyzer.PropMaximum]); diff != "" {
				t.Errorf("maximum mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
