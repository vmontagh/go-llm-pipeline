package analyzer_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/vmontagh/go-llm-pipeline/pkg/analyzer"
)

// TestMalformedTypeUnion covers U1: a malformed type keyword (empty union,
// non-string union member, or a non-string/non-array value) is a schema
// violation rather than silently skipping type validation.
func TestMalformedTypeUnion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		schema  map[string]any
		data    string
		wantErr bool
	}{
		{
			name:    "union with non-string member",
			schema:  map[string]any{analyzer.PropType: []any{"string", 1}},
			data:    `"x"`,
			wantErr: true,
		},
		{
			name:    "empty union",
			schema:  map[string]any{analyzer.PropType: []any{}},
			data:    `"x"`,
			wantErr: true,
		},
		{
			name:    "non-string non-array type",
			schema:  map[string]any{analyzer.PropType: 123},
			data:    `"x"`,
			wantErr: true,
		},
		{
			name:   "valid single type",
			schema: map[string]any{analyzer.PropType: analyzer.TypeString},
			data:   `"x"`,
		},
		{
			name:   "valid union accepts string",
			schema: map[string]any{analyzer.PropType: []any{analyzer.TypeString, analyzer.TypeNull}},
			data:   `"x"`,
		},
		{
			name:   "valid union accepts null",
			schema: map[string]any{analyzer.PropType: []any{analyzer.TypeString, analyzer.TypeNull}},
			data:   `null`,
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

// TestUnusableNumericKeywords covers U2: a present-but-unusable length or bound
// keyword (over-magnitude or non-numeric) fails closed as a schema violation
// rather than silently dropping the constraint.
func TestUnusableNumericKeywords(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		schema  map[string]any
		data    string
		wantErr bool
	}{
		{
			name:    "over-magnitude minLength",
			schema:  map[string]any{analyzer.PropType: analyzer.TypeString, analyzer.PropMinLength: json.Number("1e400")},
			data:    `"x"`,
			wantErr: true,
		},
		{
			name:    "non-numeric minLength",
			schema:  map[string]any{analyzer.PropType: analyzer.TypeString, analyzer.PropMinLength: "x"},
			data:    `"x"`,
			wantErr: true,
		},
		{
			name:    "over-magnitude minimum",
			schema:  map[string]any{analyzer.PropType: analyzer.TypeInteger, analyzer.PropMinimum: json.Number("1e400")},
			data:    `5`,
			wantErr: true,
		},
		{
			name:   "large but valid minimum enforced exactly",
			schema: map[string]any{analyzer.PropType: analyzer.TypeInteger, analyzer.PropMinimum: json.Number("9007199254740993")},
			data:   `9007199254740992`,
			wantErr: true,
		},
		{
			name:   "large but valid minimum accepts equal",
			schema: map[string]any{analyzer.PropType: analyzer.TypeInteger, analyzer.PropMinimum: json.Number("9007199254740993")},
			data:   `9007199254740993`,
		},
		{
			name:   "normal minLength unchanged",
			schema: map[string]any{analyzer.PropType: analyzer.TypeString, analyzer.PropMinLength: json.Number("2")},
			data:   `"ab"`,
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

// TestStringOptionPattern covers U3: a json:",string" numeric/bool field gains a
// grammar pattern so a non-decodable string is a schema violation with
// field-level feedback rather than an opaque decode failure.
func TestStringOptionPattern(t *testing.T) {
	t.Parallel()

	got, err := schemaOfField(t, reflect.TypeOf(0), "field,string", "")
	if err != nil {
		t.Fatalf("SchemaOf() error: %v", err)
	}
	field := prop(got, "field")
	if field[analyzer.PropType] != analyzer.TypeString {
		t.Errorf("type = %v, want string", field[analyzer.PropType])
	}
	if field[analyzer.PropPattern] != `^-?[0-9]+$` {
		t.Errorf("pattern = %v, want signed-int grammar", field[analyzer.PropPattern])
	}

	if err := analyzer.Validate(field, []byte(`"abc"`)); !errors.Is(err, analyzer.ErrSchemaValidation) {
		t.Errorf(`Validate("abc") error = %v, want ErrSchemaValidation`, err)
	}
	if err := analyzer.Validate(field, []byte(`"123"`)); err != nil {
		t.Errorf(`Validate("123") error = %v, want nil`, err)
	}
}

// TestStringOptionPatternBool covers U3 for a bool field and the Extract
// round-trip so a schema-valid quoted literal still decodes.
func TestStringOptionPatternBool(t *testing.T) {
	t.Parallel()

	got, err := schemaOfField(t, reflect.TypeOf(false), "field,string", "")
	if err != nil {
		t.Fatalf("SchemaOf() error: %v", err)
	}
	field := prop(got, "field")
	if err := analyzer.Validate(field, []byte(`"true"`)); err != nil {
		t.Errorf(`Validate("true") error = %v, want nil`, err)
	}
	if err := analyzer.Validate(field, []byte(`"yes"`)); !errors.Is(err, analyzer.ErrSchemaValidation) {
		t.Errorf(`Validate("yes") error = %v, want ErrSchemaValidation`, err)
	}

	type quoted struct {
		N int `json:"n,string"`
	}
	a := &scriptedAnalyzer{responses: []string{`{"n":"123"}`}}
	out, err := analyzer.Extract[quoted](t.Context(), a, analyzer.ExtractRequest{Prompt: "x"})
	if err != nil {
		t.Fatalf("Extract[quoted] error: %v", err)
	}
	if out.N != 123 {
		t.Errorf("Extract[quoted].N = %d, want 123", out.N)
	}
}

// TestFixedArrayLength covers U4: a Go array derives exact-length bounds so a
// response with the wrong element count is a schema violation, while slices stay
// unbounded.
func TestFixedArrayLength(t *testing.T) {
	t.Parallel()

	arrSchema, err := schemaOfField(t, reflect.TypeOf([2]int{}), "field", "")
	if err != nil {
		t.Fatalf("SchemaOf([2]int) error: %v", err)
	}
	field := prop(arrSchema, "field")
	if diff := cmp.Diff(json.Number("2"), field[analyzer.PropMinItems]); diff != "" {
		t.Errorf("minItems mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(json.Number("2"), field[analyzer.PropMaxItems]); diff != "" {
		t.Errorf("maxItems mismatch (-want +got):\n%s", diff)
	}

	tests := []struct {
		name    string
		data    string
		wantErr bool
	}{
		{name: "too few", data: `[1]`, wantErr: true},
		{name: "too many", data: `[1,2,3]`, wantErr: true},
		{name: "exact", data: `[1,2]`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := analyzer.Validate(field, []byte(tc.data))
			if tc.wantErr {
				if !errors.Is(err, analyzer.ErrSchemaValidation) {
					t.Errorf("Validate(%s) error = %v, want ErrSchemaValidation", tc.data, err)
				}
				return
			}
			if err != nil {
				t.Errorf("Validate(%s) error = %v, want nil", tc.data, err)
			}
		})
	}
}

// TestSliceUnbounded covers U4: a slice carries no item-count bounds, so any
// length validates.
func TestSliceUnbounded(t *testing.T) {
	t.Parallel()

	got, err := schemaOfField(t, reflect.TypeOf([]int(nil)), "field", "")
	if err != nil {
		t.Fatalf("SchemaOf([]int) error: %v", err)
	}
	field := prop(got, "field")
	if _, present := field[analyzer.PropMinItems]; present {
		t.Errorf("slice schema unexpectedly carries minItems")
	}
	if _, present := field[analyzer.PropMaxItems]; present {
		t.Errorf("slice schema unexpectedly carries maxItems")
	}
	if err := analyzer.Validate(field, []byte(`[1,2,3,4]`)); err != nil {
		t.Errorf("Validate([1,2,3,4]) error = %v, want nil", err)
	}
}

// TestNestedFixedArrayLength covers U4: a fixed-size array nested as a slice
// element inherits the exact-length bound.
func TestNestedFixedArrayLength(t *testing.T) {
	t.Parallel()

	got, err := schemaOfField(t, reflect.TypeOf([][2]int{}), "field", "")
	if err != nil {
		t.Fatalf("SchemaOf([][2]int) error: %v", err)
	}
	field := prop(got, "field")
	elem, ok := field[analyzer.PropItems].(map[string]any)
	if !ok {
		t.Fatalf("items missing or not a schema: %v", field[analyzer.PropItems])
	}
	if diff := cmp.Diff(json.Number("2"), elem[analyzer.PropMaxItems]); diff != "" {
		t.Errorf("element maxItems mismatch (-want +got):\n%s", diff)
	}
	if err := analyzer.Validate(field, []byte(`[[1,2],[3]]`)); !errors.Is(err, analyzer.ErrSchemaValidation) {
		t.Errorf("Validate([[1,2],[3]]) error = %v, want ErrSchemaValidation", err)
	}
	if err := analyzer.Validate(field, []byte(`[[1,2],[3,4]]`)); err != nil {
		t.Errorf("Validate([[1,2],[3,4]]) error = %v, want nil", err)
	}
}
