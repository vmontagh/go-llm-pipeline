package analyzer_test

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/vmontagh/go-llm-pipeline/pkg/analyzer"
)

// TestSchemaOfMap covers F2: a map[string]T field derives an object schema whose
// additionalProperties carries the element schema, and Validate enforces the
// element type for every key.
func TestSchemaOfMap(t *testing.T) {
	t.Parallel()

	type counts struct {
		Scores map[string]int `json:"scores"`
	}
	type badKey struct {
		Scores map[int]int `json:"scores"`
	}

	t.Run("derives element schema", func(t *testing.T) {
		t.Parallel()
		got, err := analyzer.SchemaOf(counts{})
		if err != nil {
			t.Fatalf("SchemaOf(counts{}) error: %v", err)
		}
		intMin, intMax := signedBounds(strconv.IntSize)
		want := map[string]any{
			analyzer.PropType: analyzer.TypeObject,
			analyzer.PropAdditional: map[string]any{
				analyzer.PropType:    analyzer.TypeInteger,
				analyzer.PropMinimum: json.Number(intMin),
				analyzer.PropMaximum: json.Number(intMax),
			},
		}
		if diff := cmp.Diff(want, prop(got, "scores")); diff != "" {
			t.Errorf("scores schema mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("non-string key rejected", func(t *testing.T) {
		t.Parallel()
		if _, err := analyzer.SchemaOf(badKey{}); !errors.Is(err, analyzer.ErrSchemaDerivation) {
			t.Errorf("SchemaOf(badKey{}) error = %v, want ErrSchemaDerivation", err)
		}
	})

	t.Run("validates element type", func(t *testing.T) {
		t.Parallel()
		schema, err := analyzer.SchemaOf(counts{})
		if err != nil {
			t.Fatalf("SchemaOf(counts{}) error: %v", err)
		}
		tests := []struct {
			name    string
			doc     string
			wantErr error
		}{
			{name: "integer value accepted", doc: `{"scores":{"k":1}}`, wantErr: nil},
			{name: "string value rejected", doc: `{"scores":{"k":"x"}}`, wantErr: analyzer.ErrSchemaValidation},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				if err := analyzer.Validate(schema, []byte(tc.doc)); !errors.Is(err, tc.wantErr) {
					t.Errorf("Validate(%s) = %v, want errors.Is %v", tc.doc, err, tc.wantErr)
				}
			})
		}
	})
}

// TestSchemaOfDominantFields covers F3: JSON dominant-field resolution for
// duplicate and shadowed names.
func TestSchemaOfDominantFields(t *testing.T) {
	t.Parallel()

	type deepInner struct {
		X string `json:"x"`
	}
	type shadowOuter struct {
		deepInner
		X int `json:"x"` // shallower, shadows the embedded X
	}

	tests := []struct {
		name      string
		value     any
		wantProps []string
		checkType func(t *testing.T, schema map[string]any)
	}{
		{
			name:      "two direct fields with same json name are omitted",
			value:     reflect.New(dupTagStruct()).Elem().Interface(),
			wantProps: nil,
		},
		{
			name:      "direct field shadows embedded field",
			value:     shadowOuter{},
			wantProps: []string{"x"},
			checkType: func(t *testing.T, schema map[string]any) {
				if got := prop(schema, "x")[analyzer.PropType]; got != analyzer.TypeInteger {
					t.Errorf("x type = %v, want integer (shallow wins)", got)
				}
			},
		},
		{
			name: "same-depth tie resolved to the tagged field",
			value: struct {
				A string `json:"x"`
				B string
			}{},
			wantProps: []string{"B", "x"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			schema, err := analyzer.SchemaOf(tc.value)
			if err != nil {
				t.Fatalf("SchemaOf() error: %v", err)
			}
			if diff := cmp.Diff(tc.wantProps, propNames(schema)); diff != "" {
				t.Errorf("property names mismatch (-want +got):\n%s", diff)
			}
			if tc.checkType != nil {
				tc.checkType(t, schema)
			}
		})
	}
}

// TestSchemaOfLengthKeyword covers F5: minLength/maxLength must be non-negative
// integers, stored as json.Number.
func TestSchemaOfLengthKeyword(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		tag     string
		wantErr error
		want    map[string]any
	}{
		{
			name: "valid non-negative integer",
			tag:  "minLength=2",
			want: map[string]any{analyzer.PropType: analyzer.TypeString, analyzer.PropMinLength: json.Number("2")},
		},
		{name: "negative rejected", tag: "minLength=-1", wantErr: analyzer.ErrSchemaDerivation},
		{name: "fractional rejected", tag: "minLength=1.5", wantErr: analyzer.ErrSchemaDerivation},
		{name: "non-number rejected", tag: "minLength=NaN", wantErr: analyzer.ErrSchemaDerivation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaOfTagged(t, reflect.TypeOf(""), tc.tag)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Errorf("SchemaOf() error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("SchemaOf() error: %v", err)
			}
			if diff := cmp.Diff(tc.want, prop(got, "field")); diff != "" {
				t.Errorf("field schema mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestSchemaOfBoundStrict covers F7: a non-JSON numeric bound is rejected.
func TestSchemaOfBoundStrict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		tag     string
		wantErr error
	}{
		{name: "rational rejected", tag: "min=1/2", wantErr: analyzer.ErrSchemaDerivation},
		{name: "decimal accepted", tag: "min=1.5"},
		{name: "exponent accepted", tag: "min=1e3"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := schemaOfTagged(t, reflect.TypeOf(float64(0)), tc.tag)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("SchemaOf() error = %v, want errors.Is %v", err, tc.wantErr)
			}
		})
	}
}

// TestSchemaOfNumericEnumStrict covers F8: numeric enum members must be valid
// JSON numbers, and integer fields reject fractional members.
func TestSchemaOfNumericEnumStrict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		tag     string
		wantErr error
	}{
		{name: "fractional member on int field rejected", tag: "enum=1|2.5", wantErr: analyzer.ErrSchemaDerivation},
		{name: "rational member rejected", tag: "enum=1/2", wantErr: analyzer.ErrSchemaDerivation},
		{name: "integer members accepted", tag: "enum=1|2|3"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := schemaOfTagged(t, reflect.TypeOf(int(0)), tc.tag)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("SchemaOf() error = %v, want errors.Is %v", err, tc.wantErr)
			}
		})
	}
}

// TestSchemaOfEmbeddedRequiredness covers F9: fields promoted through an optional
// (pointer/omitempty) embedding must not be required.
func TestSchemaOfEmbeddedRequiredness(t *testing.T) {
	t.Parallel()

	type inner struct {
		City string `json:"city"`
	}
	type pointerEmbed struct {
		*inner
		Name string `json:"name"`
	}
	type omitemptyEmbed struct {
		inner `json:",omitempty"`
		Name  string `json:"name"`
	}
	type requiredEmbed struct {
		inner
		Name string `json:"name"`
	}

	tests := []struct {
		name         string
		value        any
		wantRequired []string
	}{
		{name: "pointer embed makes promoted field optional", value: pointerEmbed{}, wantRequired: []string{"name"}},
		{name: "omitempty embed makes promoted field optional", value: omitemptyEmbed{}, wantRequired: []string{"name"}},
		{name: "plain embed keeps promoted field required", value: requiredEmbed{}, wantRequired: []string{"city", "name"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			schema, err := analyzer.SchemaOf(tc.value)
			if err != nil {
				t.Fatalf("SchemaOf() error: %v", err)
			}
			got := requiredList(schema)
			if diff := cmp.Diff(tc.wantRequired, got); diff != "" {
				t.Errorf("required mismatch (-want +got):\n%s", diff)
			}
			// Omitting the optional promoted field must validate.
			if len(tc.wantRequired) == 1 {
				if err := analyzer.Validate(schema, []byte(`{"name":"n"}`)); err != nil {
					t.Errorf("Validate(without city) = %v, want nil", err)
				}
			}
		})
	}
}

// TestStringOptionByKind covers G1: json:",string" is honored only for the kinds
// encoding/json quotes; for other kinds the option is ignored so the real shape
// is derived and the response still decodes.
func TestStringOptionByKind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		typ      reflect.Type
		wantType string
	}{
		{name: "int coerced to string", typ: reflect.TypeOf(0), wantType: analyzer.TypeString},
		{name: "pointer to int coerced to string", typ: reflect.TypeOf((*int)(nil)), wantType: analyzer.TypeString},
		{name: "slice option ignored", typ: reflect.TypeOf([]int(nil)), wantType: analyzer.TypeArray},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaOfField(t, tc.typ, "field,string", "")
			if err != nil {
				t.Fatalf("SchemaOf() error: %v", err)
			}
			if diff := cmp.Diff(tc.wantType, prop(got, "field")[analyzer.PropType]); diff != "" {
				t.Errorf("field type mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestConstraintTypeMatch covers G2: a constraint keyword that cannot be enforced
// against the field's derived type is rejected.
func TestConstraintTypeMatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		typ     reflect.Type
		tag     string
		wantErr error
	}{
		{name: "min on string rejected", typ: reflect.TypeOf(""), tag: "min=5", wantErr: analyzer.ErrSchemaDerivation},
		{name: "minLength on int rejected", typ: reflect.TypeOf(0), tag: "minLength=3", wantErr: analyzer.ErrSchemaDerivation},
		{name: "pattern on number rejected", typ: reflect.TypeOf(1.5), tag: "pattern=^a$", wantErr: analyzer.ErrSchemaDerivation},
		{name: "min on int accepted", typ: reflect.TypeOf(0), tag: "min=5"},
		{name: "minLength on string accepted", typ: reflect.TypeOf(""), tag: "minLength=3"},
		{name: "pattern on string accepted", typ: reflect.TypeOf(""), tag: "pattern=^a$"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := schemaOfField(t, tc.typ, "field", tc.tag)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("SchemaOf() error = %v, want errors.Is %v", err, tc.wantErr)
			}
		})
	}
}

// TestConstraintStringOptionCompose covers G1+G2 interaction: once ,string makes
// an int field a string, a numeric bound on it is rejected.
func TestConstraintStringOptionCompose(t *testing.T) {
	t.Parallel()
	_, err := schemaOfField(t, reflect.TypeOf(0), "field,string", "min=5")
	if !errors.Is(err, analyzer.ErrSchemaDerivation) {
		t.Errorf("SchemaOf(int,string with min) error = %v, want ErrSchemaDerivation", err)
	}
}

// TestNullNumericConstraint covers G3: JSON null unmarshals into an empty
// json.Number and must be rejected rather than stored.
func TestNullNumericConstraint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		typ  reflect.Type
		tag  string
	}{
		{name: "min null", typ: reflect.TypeOf(0), tag: "min=null"},
		{name: "enum null member", typ: reflect.TypeOf(0), tag: "enum=null|1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := schemaOfField(t, tc.typ, "field", tc.tag)
			if !errors.Is(err, analyzer.ErrSchemaDerivation) {
				t.Errorf("SchemaOf() error = %v, want ErrSchemaDerivation", err)
			}
		})
	}
}

// TestLengthJSONNumberSyntax covers G4: length keywords require canonical JSON
// non-negative integers.
func TestLengthJSONNumberSyntax(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		tag     string
		wantErr error
		want    json.Number
	}{
		{name: "leading zero rejected", tag: "minLength=01", wantErr: analyzer.ErrSchemaDerivation},
		{name: "explicit plus rejected", tag: "minLength=+1", wantErr: analyzer.ErrSchemaDerivation},
		{name: "fractional rejected", tag: "minLength=1.5", wantErr: analyzer.ErrSchemaDerivation},
		{name: "negative rejected", tag: "minLength=-1", wantErr: analyzer.ErrSchemaDerivation},
		{name: "plain integer stored", tag: "minLength=3", want: json.Number("3")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaOfField(t, reflect.TypeOf(""), "field", tc.tag)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Errorf("SchemaOf() error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("SchemaOf() error: %v", err)
			}
			if diff := cmp.Diff(tc.want, prop(got, "field")[analyzer.PropMinLength]); diff != "" {
				t.Errorf("minLength mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestSchemaOfRecursiveTypes covers H1: cyclic named collection/struct types are
// rejected, while non-cyclic composites still derive.
func TestSchemaOfRecursiveTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		typ     reflect.Type
		wantErr bool
	}{
		{name: "recursive slice", typ: reflect.TypeOf(chain(nil)), wantErr: true},
		{name: "recursive map", typ: reflect.TypeOf(tree(nil)), wantErr: true},
		{name: "nested unnamed slice", typ: reflect.TypeOf([][]int(nil)), wantErr: false},
		{name: "slice of pointer", typ: reflect.TypeOf([]*struct{ X int }(nil)), wantErr: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := schemaOfField(t, tc.typ, "field", "")
			if tc.wantErr {
				if !errors.Is(err, analyzer.ErrSchemaDerivation) {
					t.Errorf("schemaOfField(%s) error = %v, want ErrSchemaDerivation", tc.name, err)
				}
				return
			}
			if err != nil {
				t.Errorf("schemaOfField(%s) unexpected error: %v", tc.name, err)
			}
		})
	}
}

// TestSchemaOfCustomUnmarshaler covers H2: json.Unmarshaler precedence. A type
// with only a custom UnmarshalJSON is rejected; one that also unmarshals from
// text (time.Time) derives to a string.
func TestSchemaOfCustomUnmarshaler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		typ      reflect.Type
		wantType string
		wantErr  bool
	}{
		{name: "json-only scalar", typ: reflect.TypeOf(stringyInt(0)), wantErr: true},
		{name: "json-only slice", typ: reflect.TypeOf(tags(nil)), wantErr: true},
		{name: "text and json", typ: reflect.TypeOf(time.Time{}), wantType: analyzer.TypeString},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaOfField(t, tc.typ, "field", "")
			if tc.wantErr {
				if !errors.Is(err, analyzer.ErrSchemaDerivation) {
					t.Errorf("schemaOfField(%s) error = %v, want ErrSchemaDerivation", tc.name, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("schemaOfField(%s) unexpected error: %v", tc.name, err)
			}
			if gotType := prop(got, "field")[analyzer.PropType]; gotType != tc.wantType {
				t.Errorf("schemaOfField(%s) type = %v, want %v", tc.name, gotType, tc.wantType)
			}
		})
	}
}

// TestSchemaOfMagnitudeBound covers H4 at derivation: an oversized bound literal
// in a jsonschema tag is rejected with ErrSchemaDerivation.
func TestSchemaOfMagnitudeBound(t *testing.T) {
	t.Parallel()

	_, err := schemaOfField(t, reflect.TypeOf(0), "field", "min=1e999999999")
	if !errors.Is(err, analyzer.ErrSchemaDerivation) {
		t.Errorf("schemaOfField(min=1e999999999) error = %v, want ErrSchemaDerivation", err)
	}
}

// TestSchemaOfStringOption covers S1: json:",string" on a string field is
// rejected, while numeric fields keep deriving to type:string.
func TestSchemaOfStringOption(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		typ      reflect.Type
		wantType string
		wantErr  bool
	}{
		{name: "string field rejected", typ: reflect.TypeOf(""), wantErr: true},
		{name: "int field allowed", typ: reflect.TypeOf(0), wantType: analyzer.TypeString},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaOfField(t, tc.typ, "field,string", "")
			if tc.wantErr {
				if !errors.Is(err, analyzer.ErrSchemaDerivation) {
					t.Errorf("schemaOfField(%s) error = %v, want ErrSchemaDerivation", tc.name, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("schemaOfField(%s) unexpected error: %v", tc.name, err)
			}
			if gotType := prop(got, "field")[analyzer.PropType]; gotType != tc.wantType {
				t.Errorf("schemaOfField(%s) type = %v, want %v", tc.name, gotType, tc.wantType)
			}
		})
	}
}

// TestSchemaOfNumericWidthBounds covers S5: derived integer/float schemas carry
// the width's representable bounds so out-of-range values are schema violations
// (FieldError) rather than opaque decode failures.
func TestSchemaOfNumericWidthBounds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		typ     reflect.Type
		accept  string
		reject  string
		wantErr bool // reject expected to be a schema violation
	}{
		{name: "uint8 upper", typ: reflect.TypeOf(uint8(0)), accept: `255`, reject: `256`, wantErr: true},
		{name: "uint8 lower", typ: reflect.TypeOf(uint8(0)), accept: `0`, reject: `-1`, wantErr: true},
		{name: "int8 upper", typ: reflect.TypeOf(int8(0)), accept: `127`, reject: `128`, wantErr: true},
		{name: "float32 range", typ: reflect.TypeOf(float32(0)), accept: `1e30`, reject: `1e40`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			schema, err := schemaOfField(t, tc.typ, "field", "")
			if err != nil {
				t.Fatalf("schemaOfField(%s) error: %v", tc.name, err)
			}
			field := prop(schema, "field")
			if err := analyzer.Validate(field, []byte(tc.accept)); err != nil {
				t.Errorf("Validate(%s, %s) = %v, want nil", tc.name, tc.accept, err)
			}
			err = analyzer.Validate(field, []byte(tc.reject))
			if tc.wantErr && !errors.Is(err, analyzer.ErrSchemaValidation) {
				t.Errorf("Validate(%s, %s) = %v, want ErrSchemaValidation", tc.name, tc.reject, err)
			}
		})
	}
}

// TestSchemaOfNestedNumericBounds covers the S5 sweep: nested slice-element
// numerics inherit the derived width bounds.
func TestSchemaOfNestedNumericBounds(t *testing.T) {
	t.Parallel()

	schema, err := schemaOfField(t, reflect.TypeOf([]uint8(nil)), "field", "")
	if err != nil {
		t.Fatalf("schemaOfField([]uint8) error: %v", err)
	}
	items, ok := prop(schema, "field")[analyzer.PropItems].(map[string]any)
	if !ok {
		t.Fatalf("items not a schema map: %#v", prop(schema, "field"))
	}
	if got := items[analyzer.PropMaximum]; got != json.Number("255") {
		t.Errorf("[]uint8 element maximum = %v, want 255", got)
	}
}

// TestSchemaOfTagOverridesWidthBound covers S5 precedence: a jsonschema max tag
// still overrides the derived width bound.
func TestSchemaOfTagOverridesWidthBound(t *testing.T) {
	t.Parallel()

	schema, err := schemaOfField(t, reflect.TypeOf(int8(0)), "field", "max=10")
	if err != nil {
		t.Fatalf("schemaOfField(int8, max=10) error: %v", err)
	}
	field := prop(schema, "field")
	if got := field[analyzer.PropMaximum]; got != json.Number("10") {
		t.Errorf("int8 max override = %v, want 10", got)
	}
	if err := analyzer.Validate(field, []byte(`11`)); !errors.Is(err, analyzer.ErrSchemaValidation) {
		t.Errorf("Validate(int8 max=10, 11) = %v, want ErrSchemaValidation", err)
	}
}

// TestBoundIntersection covers T1: a min/max tag may only narrow the native
// width range, never widen it, so a schema-conforming value always decodes into T.
func TestBoundIntersection(t *testing.T) {
	t.Parallel()

	int8Min, int8Max := signedBounds(8)

	tests := []struct {
		name string
		typ  reflect.Type
		tag  string
		key  string
		want string
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

// TestDominanceBeforeDerivation covers T3: schemas are derived only for the
// fields encoding/json keeps, so a shadowed or ambiguous unsupported-type field
// never fails derivation.
func TestDominanceBeforeDerivation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		value      any
		wantName   string
		wantType   string
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

// TestStringOptionPattern covers U3/W2: a json:",string" numeric field records
// the Go kind so a non-decodable string is a schema violation with field-level
// feedback rather than an opaque decode failure.
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
	if field[analyzer.PropStringEncodedKind] != "int" {
		t.Errorf("x-goStringKind = %v, want int", field[analyzer.PropStringEncodedKind])
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

// TestStringOptionUintptr covers W1: encoding/json honors json:",string" for
// uintptr, so its schema must be a decode-guarded string, not an integer.
func TestStringOptionUintptr(t *testing.T) {
	t.Parallel()

	got, err := schemaOfField(t, reflect.TypeOf(uintptr(0)), "field,string", "")
	if err != nil {
		t.Fatalf("SchemaOf(uintptr,string) error: %v", err)
	}
	field := prop(got, "field")
	if field[analyzer.PropType] != analyzer.TypeString {
		t.Errorf("type = %v, want string", field[analyzer.PropType])
	}
	if field[analyzer.PropStringEncodedKind] != "uintptr" {
		t.Errorf("x-goStringKind = %v, want uintptr", field[analyzer.PropStringEncodedKind])
	}
	if err := analyzer.Validate(field, []byte(`"123"`)); err != nil {
		t.Errorf(`Validate("123") error = %v, want nil`, err)
	}
	if err := analyzer.Validate(field, []byte(`123`)); !errors.Is(err, analyzer.ErrSchemaValidation) {
		t.Errorf("Validate(123) error = %v, want ErrSchemaValidation", err)
	}

	type quoted struct {
		N uintptr `json:"n,string"`
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

// TestStringOptionDecodeEquivalence covers W2/W3: for every quoted kind the
// derived schema accepts a string if and only if encoding/json decodes it,
// including the exact number grammar (".5" vs "1.") and width limits, so no
// schema-valid string can trigger an opaque decode failure.
func TestStringOptionDecodeEquivalence(t *testing.T) {
	t.Parallel()

	kinds := []struct {
		name string
		typ  reflect.Type
	}{
		{name: "bool", typ: reflect.TypeOf(false)},
		{name: "int", typ: reflect.TypeOf(int(0))},
		{name: "int8", typ: reflect.TypeOf(int8(0))},
		{name: "int64", typ: reflect.TypeOf(int64(0))},
		{name: "uint8", typ: reflect.TypeOf(uint8(0))},
		{name: "uintptr", typ: reflect.TypeOf(uintptr(0))},
		{name: "float32", typ: reflect.TypeOf(float32(0))},
		{name: "float64", typ: reflect.TypeOf(float64(0))},
	}
	samples := []string{
		"true", "false", "yes", "",
		"0", "1", "-1", "01", "+1", "127", "128", "-128", "-129", "255", "256",
		"abc", "1.", ".5", "1.5", "-1.5", "1e3", "1e10", "1e100", "1e400", "1.5e2", "-3",
	}
	for _, k := range kinds {
		t.Run(k.name, func(t *testing.T) {
			t.Parallel()
			schema, err := schemaOfField(t, k.typ, "field,string", "")
			if err != nil {
				t.Fatalf("SchemaOf(%s,string) error: %v", k.name, err)
			}
			field := prop(schema, "field")
			for _, s := range samples {
				quoted, err := json.Marshal(s)
				if err != nil {
					t.Fatalf("json.Marshal(%q): %v", s, err)
				}
				gotValid := analyzer.Validate(field, quoted) == nil
				wantValid := stringOptionDecodes(t, k.typ, s)
				if gotValid != wantValid {
					t.Errorf("Validate(%s, %q) accepted=%v, encoding/json decode=%v", k.name, s, gotValid, wantValid)
				}
			}
		})
	}
}

// TestStringOptionWidth covers W3 explicitly: an over-width quoted value is a
// schema violation, and an in-range value validates.
func TestStringOptionWidth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		typ    reflect.Type
		accept string
		reject string
	}{
		{name: "int8", typ: reflect.TypeOf(int8(0)), accept: "127", reject: "128"},
		{name: "float32", typ: reflect.TypeOf(float32(0)), accept: "3.5", reject: "1e100"},
		{name: "float64", typ: reflect.TypeOf(float64(0)), accept: "1.5", reject: "1e400"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			schema, err := schemaOfField(t, tc.typ, "field,string", "")
			if err != nil {
				t.Fatalf("SchemaOf(%s,string) error: %v", tc.name, err)
			}
			field := prop(schema, "field")
			accept, err := json.Marshal(tc.accept)
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			if err := analyzer.Validate(field, accept); err != nil {
				t.Errorf("Validate(%q) error = %v, want nil", tc.accept, err)
			}
			reject, err := json.Marshal(tc.reject)
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			if err := analyzer.Validate(field, reject); !errors.Is(err, analyzer.ErrSchemaValidation) {
				t.Errorf("Validate(%q) error = %v, want ErrSchemaValidation", tc.reject, err)
			}
		})
	}
}

// TestRequiredFlagDirectives covers W7: a valued or empty-valued required/optional
// flag, and a tag carrying both flags, are rejected; the bare flags still work.
func TestRequiredFlagDirectives(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		tag     string
		wantErr bool
	}{
		{name: "required_empty_value", tag: "required=", wantErr: true},
		{name: "optional_empty_value", tag: "optional=", wantErr: true},
		{name: "required_true", tag: "required=true", wantErr: true},
		{name: "required_then_optional", tag: "required;optional", wantErr: true},
		{name: "optional_then_required", tag: "optional;required", wantErr: true},
		{name: "bare_required", tag: "required"},
		{name: "bare_optional", tag: "optional"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := schemaOfField(t, reflect.TypeOf(0), "field", tc.tag)
			if tc.wantErr {
				if !errors.Is(err, analyzer.ErrSchemaDerivation) {
					t.Errorf("SchemaOf(%q) error = %v, want ErrSchemaDerivation", tc.tag, err)
				}
				return
			}
			if err != nil {
				t.Errorf("SchemaOf(%q) error = %v, want nil", tc.tag, err)
			}
		})
	}
}

// TestRequiredFlagEffect covers W7: the bare required and optional flags still
// change requiredness as before, so hardening the '=' form did not regress them.
func TestRequiredFlagEffect(t *testing.T) {
	t.Parallel()

	required, err := schemaOfField(t, reflect.TypeOf(""), "field", "required")
	if err != nil {
		t.Fatalf("SchemaOf(required) error: %v", err)
	}
	if got := requiredList(required); !reflect.DeepEqual(got, []string{"field"}) {
		t.Errorf("required = %v, want [field]", got)
	}
	optional, err := schemaOfField(t, reflect.TypeOf(""), "field", "optional")
	if err != nil {
		t.Fatalf("SchemaOf(optional) error: %v", err)
	}
	if got := requiredList(optional); len(got) != 0 {
		t.Errorf("required = %v, want empty", got)
	}
}

func TestSchemaOfHardening(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   any
		wantErr error
		check   func(t *testing.T, schema map[string]any)
	}{
		{
			name:    "recursive type rejected",
			value:   recursiveNode{},
			wantErr: analyzer.ErrSchemaDerivation,
		},
		{
			name:  "text marshaler and unmarshaler is a string",
			value: textValued{},
			check: func(t *testing.T, schema map[string]any) {
				got := prop(schema, "when")
				if got[analyzer.PropType] != analyzer.TypeString {
					t.Errorf("when type = %v, want string", got[analyzer.PropType])
				}
			},
		},
		{
			name:    "marshal-only text type rejected",
			value:   textMarshalOnly{},
			wantErr: analyzer.ErrSchemaDerivation,
		},
		{
			name:  "json string option is a string",
			value: quotedNumber{},
			check: func(t *testing.T, schema map[string]any) {
				got := prop(schema, "count")
				if got[analyzer.PropType] != analyzer.TypeString {
					t.Errorf("count type = %v, want string", got[analyzer.PropType])
				}
			},
		},
		{
			name:  "boolean enum coerced",
			value: flagged{},
			check: func(t *testing.T, schema map[string]any) {
				got := prop(schema, "on")
				want := []any{true, false}
				if diff := cmp.Diff(want, got[analyzer.PropEnum]); diff != "" {
					t.Errorf("on enum mismatch (-want +got):\n%s", diff)
				}
			},
		},
		{
			name:  "embedded fields promoted",
			value: embeddedOuter{},
			check: func(t *testing.T, schema map[string]any) {
				props, ok := schema[analyzer.PropProperties].(map[string]any)
				if !ok {
					t.Fatalf("schema has no properties: %v", schema)
				}
				if _, ok := props["city"]; !ok {
					t.Errorf("promoted field city missing: %v", props)
				}
				if _, ok := props["name"]; !ok {
					t.Errorf("field name missing: %v", props)
				}
				if _, ok := props["embeddedInner"]; ok {
					t.Errorf("embedded type leaked as a property")
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := analyzer.SchemaOf(tc.value)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("SchemaOf() error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("SchemaOf() unexpected error: %v", err)
			}
			tc.check(t, got)
		})
	}
}

func TestSchemaOfInvalidTag(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value any
	}{
		{name: "invalid min", value: struct {
			N int `json:"n" jsonschema:"min=zero"`
		}{}},
		{name: "invalid pattern", value: struct {
			S string `json:"s" jsonschema:"pattern=("`
		}{}},
		{name: "unknown directive", value: struct {
			S string `json:"s" jsonschema:"bogus=1"`
		}{}},
		{name: "non-numeric enum on int", value: struct {
			N int `json:"n" jsonschema:"enum=a|b"`
		}{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := analyzer.SchemaOf(tc.value); !errors.Is(err, analyzer.ErrSchemaDerivation) {
				t.Errorf("SchemaOf() error = %v, want ErrSchemaDerivation", err)
			}
		})
	}
}

func TestSchemaOf(t *testing.T) {
	t.Parallel()

	type Address struct {
		City string `json:"city"`
		Zip  string `json:"zip,omitempty"`
	}
	type Person struct {
		Name    string   `json:"name" jsonschema:"minLength=1"`
		Age     int      `json:"age" jsonschema:"min=0;max=130"`
		Rating  float64  `json:"rating"`
		Active  bool     `json:"active"`
		Tags    []string `json:"tags,omitempty"`
		Home    Address  `json:"home"`
		Note    *string  `json:"note"`
		Status  string   `json:"status" jsonschema:"enum=new|active"`
		secret  string
		Ignored string `json:"-"`
	}

	tests := []struct {
		name    string
		value   any
		want    map[string]any
		wantErr error
	}{
		{
			name:    "non struct",
			value:   42,
			wantErr: analyzer.ErrSchemaDerivation,
		},
		{
			name:  "nested struct with constraints",
			value: Person{},
			want: map[string]any{
				analyzer.PropType:       analyzer.TypeObject,
				analyzer.PropAdditional: false,
				analyzer.PropRequired:   []string{"name", "age", "rating", "active", "home", "status"},
				analyzer.PropProperties: map[string]any{
					"name":   map[string]any{analyzer.PropType: analyzer.TypeString, analyzer.PropMinLength: json.Number("1")},
					"age":    map[string]any{analyzer.PropType: analyzer.TypeInteger, analyzer.PropMinimum: json.Number("0"), analyzer.PropMaximum: json.Number("130")},
					"rating": map[string]any{analyzer.PropType: analyzer.TypeNumber, analyzer.PropMinimum: json.Number("-1.7976931348623157e+308"), analyzer.PropMaximum: json.Number("1.7976931348623157e+308")},
					"active": map[string]any{analyzer.PropType: analyzer.TypeBoolean},
					"tags":   map[string]any{analyzer.PropType: analyzer.TypeArray, analyzer.PropItems: map[string]any{analyzer.PropType: analyzer.TypeString}},
					"home": map[string]any{
						analyzer.PropType:       analyzer.TypeObject,
						analyzer.PropAdditional: false,
						analyzer.PropRequired:   []string{"city"},
						analyzer.PropProperties: map[string]any{
							"city": map[string]any{analyzer.PropType: analyzer.TypeString},
							"zip":  map[string]any{analyzer.PropType: analyzer.TypeString},
						},
					},
					"note":   map[string]any{analyzer.PropType: analyzer.TypeString},
					"status": map[string]any{analyzer.PropType: analyzer.TypeString, analyzer.PropEnum: []any{"new", "active"}},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := analyzer.SchemaOf(tc.value)
			if tc.wantErr != nil {
				if err == nil || !errors.Is(err, tc.wantErr) {
					t.Fatalf("SchemaOf(%v) error = %v, want %v", tc.value, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("SchemaOf(%v) unexpected error: %v", tc.value, err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("SchemaOf(%v) mismatch (-want +got):\n%s", tc.value, diff)
			}
		})
	}
}

func TestSchemaOfNumericEnum(t *testing.T) {
	t.Parallel()
	type roll struct {
		Face int `json:"face" jsonschema:"enum=1|2|3"`
	}
	got, err := analyzer.SchemaOf(roll{})
	if err != nil {
		t.Fatalf("SchemaOf(roll{}) unexpected error: %v", err)
	}
	intMin, intMax := signedBounds(strconv.IntSize)
	want := map[string]any{
		analyzer.PropType:       analyzer.TypeObject,
		analyzer.PropAdditional: false,
		analyzer.PropRequired:   []string{"face"},
		analyzer.PropProperties: map[string]any{
			"face": map[string]any{
				analyzer.PropType:    analyzer.TypeInteger,
				analyzer.PropMinimum: json.Number(intMin),
				analyzer.PropMaximum: json.Number(intMax),
				analyzer.PropEnum:    []any{json.Number("1"), json.Number("2"), json.Number("3")},
			},
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("SchemaOf(roll{}) mismatch (-want +got):\n%s", diff)
	}

	// A numeric value permitted by the enum validates; one outside it fails.
	if err := analyzer.Validate(got, []byte(`{"face":2}`)); err != nil {
		t.Errorf("Validate(face=2) = %v, want nil", err)
	}
	if err := analyzer.Validate(got, []byte(`{"face":9}`)); !errors.Is(err, analyzer.ErrSchemaValidation) {
		t.Errorf("Validate(face=9) = %v, want ErrSchemaValidation", err)
	}
}
