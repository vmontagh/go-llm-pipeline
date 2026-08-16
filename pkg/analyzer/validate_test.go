package analyzer_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/vmontagh/go-llm-pipeline/pkg/analyzer"
)

// TestValidateIntegerLiteralForm covers F4: integer validation mirrors
// encoding/json, which decodes a number into a Go integer only when it is written
// in integer form. So 1e0 and 1.0 are not schema-valid integers even though they
// are mathematically integral, keeping schema-validity and decodability identical.
func TestValidateIntegerLiteralForm(t *testing.T) {
	t.Parallel()

	schema := map[string]any{analyzer.PropType: analyzer.TypeInteger}
	tests := []struct {
		name    string
		doc     string
		wantErr error
	}{
		{name: "plain integer", doc: `1`, wantErr: nil},
		{name: "negative integer", doc: `-42`, wantErr: nil},
		{name: "large integer", doc: `9007199254740993`, wantErr: nil},
		{name: "exponent form rejected", doc: `1e0`, wantErr: analyzer.ErrSchemaValidation},
		{name: "uppercase exponent rejected", doc: `1000E0`, wantErr: analyzer.ErrSchemaValidation},
		{name: "trailing zero fraction rejected", doc: `1.0`, wantErr: analyzer.ErrSchemaValidation},
		{name: "fractional rejected", doc: `1.5`, wantErr: analyzer.ErrSchemaValidation},
		{name: "negative zero rejected", doc: `-0`, wantErr: analyzer.ErrSchemaValidation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := analyzer.Validate(schema, []byte(tc.doc)); !errors.Is(err, tc.wantErr) {
				t.Errorf("Validate(%s) = %v, want errors.Is %v", tc.doc, err, tc.wantErr)
			}
		})
	}
}

// TestValidateNegativeZeroUnsigned covers the negative-zero gap for unsigned
// fields: "-0" satisfies the minimum:0 that SchemaOf emits for a uint, but
// encoding/json decodes unsigned fields with strconv.ParseUint, which rejects the
// leading minus. Validate must reject "-0" so it never accepts a value the decoder
// cannot decode.
func TestValidateNegativeZeroUnsigned(t *testing.T) {
	t.Parallel()

	type unsignedField struct {
		U uint `json:"u"`
	}
	schema, err := analyzer.SchemaOf(unsignedField{})
	if err != nil {
		t.Fatalf("SchemaOf(unsignedField{}) error = %v", err)
	}
	tests := []struct {
		name    string
		doc     string
		wantErr error
	}{
		{name: "plain zero accepted", doc: `{"u":0}`, wantErr: nil},
		{name: "positive integer accepted", doc: `{"u":7}`, wantErr: nil},
		{name: "negative zero rejected", doc: `{"u":-0}`, wantErr: analyzer.ErrSchemaValidation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotErr := analyzer.Validate(schema, []byte(tc.doc))
			if !errors.Is(gotErr, tc.wantErr) {
				t.Errorf("Validate(%s) = %v, want errors.Is %v", tc.doc, gotErr, tc.wantErr)
			}
			// Guard the invariant: when Validate accepts, a strict decode must too.
			if gotErr == nil {
				if derr := json.Unmarshal([]byte(tc.doc), &unsignedField{}); derr != nil {
					t.Errorf("Validate accepted %s but decode failed: %v", tc.doc, derr)
				}
			}
		})
	}
}

// TestValidateDuplicateKeys covers the duplicate-object-key guard: a repeated key
// is ambiguous because decoding keeps only the last value, so Validate rejects it
// up front to stay consistent with a strict decode into the target type.
func TestValidateDuplicateKeys(t *testing.T) {
	t.Parallel()

	objectSchema := map[string]any{analyzer.PropType: analyzer.TypeObject}
	nestedSchema := map[string]any{
		analyzer.PropType: analyzer.TypeObject,
		analyzer.PropProperties: map[string]any{
			"inner": map[string]any{analyzer.PropType: analyzer.TypeObject},
			"list":  map[string]any{analyzer.PropType: analyzer.TypeArray},
		},
	}
	tests := []struct {
		name    string
		schema  map[string]any
		doc     string
		wantErr error
	}{
		{name: "unique keys accepted", schema: objectSchema, doc: `{"a":1,"b":2}`, wantErr: nil},
		{name: "repeated key label reused as value accepted", schema: objectSchema, doc: `{"a":"a","b":"a"}`, wantErr: nil},
		{name: "top-level duplicate rejected", schema: objectSchema, doc: `{"a":1,"a":2}`, wantErr: analyzer.ErrSchemaValidation},
		{name: "nested object duplicate rejected", schema: nestedSchema, doc: `{"inner":{"x":1,"x":2}}`, wantErr: analyzer.ErrSchemaValidation},
		{name: "duplicate inside array element rejected", schema: nestedSchema, doc: `{"list":[{"x":1,"x":2}]}`, wantErr: analyzer.ErrSchemaValidation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := analyzer.Validate(tc.schema, []byte(tc.doc)); !errors.Is(err, tc.wantErr) {
				t.Errorf("Validate(%q) = %v, want errors.Is %v", tc.doc, err, tc.wantErr)
			}
		})
	}
}

// TestValidateTrailingContent covers F11: content after the first JSON value is
// rejected.
func TestValidateTrailingContent(t *testing.T) {
	t.Parallel()

	schema := map[string]any{analyzer.PropType: analyzer.TypeObject}
	tests := []struct {
		name    string
		doc     string
		wantErr error
	}{
		{name: "single object accepted", doc: `{}`, wantErr: nil},
		{name: "trailing whitespace accepted", doc: "{}\n\t ", wantErr: nil},
		{name: "trailing object rejected", doc: `{} {}`, wantErr: analyzer.ErrSchemaValidation},
		{name: "trailing garbage rejected", doc: `{} garbage`, wantErr: analyzer.ErrSchemaValidation},
		{name: "stray brace rejected", doc: `{} }`, wantErr: analyzer.ErrSchemaValidation},
		{name: "stray bracket rejected", doc: `{} ]`, wantErr: analyzer.ErrSchemaValidation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := analyzer.Validate(schema, []byte(tc.doc)); !errors.Is(err, tc.wantErr) {
				t.Errorf("Validate(%q) = %v, want errors.Is %v", tc.doc, err, tc.wantErr)
			}
		})
	}
}

// TestValidateCompositeEnum covers H3: enum is enforced for array and object
// instances, with numeric-aware member matching.
func TestValidateCompositeEnum(t *testing.T) {
	t.Parallel()

	arraySchema := map[string]any{
		analyzer.PropType:  analyzer.TypeArray,
		analyzer.PropItems: map[string]any{analyzer.PropType: analyzer.TypeInteger},
		analyzer.PropEnum:  []any{[]any{1, 2}},
	}
	// A number-typed variant exercises numeric-aware enum matching (the member 1
	// matching the value 1.0) without asking an integer field to accept 1.0, which
	// encoding/json would refuse to decode.
	arrayNumberSchema := map[string]any{
		analyzer.PropType:  analyzer.TypeArray,
		analyzer.PropItems: map[string]any{analyzer.PropType: analyzer.TypeNumber},
		analyzer.PropEnum:  []any{[]any{1, 2}},
	}
	objectSchema := map[string]any{
		analyzer.PropType: analyzer.TypeObject,
		analyzer.PropProperties: map[string]any{
			"a": map[string]any{analyzer.PropType: analyzer.TypeInteger},
		},
		analyzer.PropAdditional: false,
		analyzer.PropEnum:       []any{map[string]any{"a": 1}},
	}

	tests := []struct {
		name    string
		schema  map[string]any
		data    string
		wantErr bool
	}{
		{name: "array enum match", schema: arraySchema, data: `[1,2]`, wantErr: false},
		{name: "array enum numeric-aware", schema: arrayNumberSchema, data: `[1.0,2]`, wantErr: false},
		{name: "array enum mismatch", schema: arraySchema, data: `[1,3]`, wantErr: true},
		{name: "object enum match", schema: objectSchema, data: `{"a":1}`, wantErr: false},
		{name: "object enum mismatch", schema: objectSchema, data: `{"a":2}`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := analyzer.Validate(tc.schema, []byte(tc.data))
			if tc.wantErr {
				if !errors.Is(err, analyzer.ErrSchemaValidation) {
					t.Errorf("Validate(%s) = %v, want ErrSchemaValidation", tc.data, err)
				}
				return
			}
			if err != nil {
				t.Errorf("Validate(%s) = %v, want nil", tc.data, err)
			}
		})
	}
}

// TestValidateNumberMagnitude covers H4: an oversized numeric literal is rejected
// as a field error without exhausting memory, while ordinary large values pass.
func TestValidateNumberMagnitude(t *testing.T) {
	t.Parallel()

	schema := map[string]any{analyzer.PropType: analyzer.TypeNumber}

	tests := []struct {
		name    string
		data    string
		wantErr bool
	}{
		{name: "huge exponent", data: `1e100000000`, wantErr: true},
		{name: "moderate large", data: `1e100`, wantErr: false},
		{name: "big integer", data: `1267650600228229401496703205376`, wantErr: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := analyzer.Validate(schema, []byte(tc.data))
			if tc.wantErr {
				if !errors.Is(err, analyzer.ErrSchemaValidation) {
					t.Errorf("Validate(%s) = %v, want ErrSchemaValidation", tc.data, err)
				}
				return
			}
			if err != nil {
				t.Errorf("Validate(%s) = %v, want nil", tc.data, err)
			}
		})
	}
}

// TestValidateEnumCanonicalization covers I1: enum members supplied as arbitrary
// Go types (not just []string/[]any of canonical values) are enforced after the
// schema is canonicalized through a JSON round-trip.
func TestValidateEnumCanonicalization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		schema  map[string]any
		data    string
		wantErr bool
	}{
		{
			name:    "typed int enum rejects",
			schema:  map[string]any{analyzer.PropType: analyzer.TypeInteger, analyzer.PropEnum: []int{1, 2}},
			data:    `3`,
			wantErr: true,
		},
		{
			name:    "typed int enum accepts",
			schema:  map[string]any{analyzer.PropType: analyzer.TypeInteger, analyzer.PropEnum: []int{1, 2}},
			data:    `2`,
			wantErr: false,
		},
		{
			name: "typed composite array enum accepts",
			schema: map[string]any{
				analyzer.PropType:  analyzer.TypeArray,
				analyzer.PropItems: map[string]any{analyzer.PropType: analyzer.TypeInteger},
				analyzer.PropEnum:  []any{[]int{1, 2}},
			},
			data:    `[1,2]`,
			wantErr: false,
		},
		{
			name: "typed composite array enum rejects",
			schema: map[string]any{
				analyzer.PropType:  analyzer.TypeArray,
				analyzer.PropItems: map[string]any{analyzer.PropType: analyzer.TypeInteger},
				analyzer.PropEnum:  []any{[]int{1, 2}},
			},
			data:    `[1,3]`,
			wantErr: true,
		},
		{
			name: "typed object enum accepts",
			schema: map[string]any{
				analyzer.PropType:       analyzer.TypeObject,
				analyzer.PropAdditional: true,
				analyzer.PropEnum:       []any{map[string]int{"a": 1}},
			},
			data:    `{"a":1}`,
			wantErr: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := analyzer.Validate(tc.schema, []byte(tc.data))
			if tc.wantErr {
				if !errors.Is(err, analyzer.ErrSchemaValidation) {
					t.Errorf("Validate(%s) = %v, want ErrSchemaValidation", tc.data, err)
				}
				return
			}
			if err != nil {
				t.Errorf("Validate(%s) = %v, want nil", tc.data, err)
			}
		})
	}
}

// TestValidateTypeUnion covers S2: type as an array/union or "null" is enforced
// without failing open, and an unrecognized type token is a FieldError.
func TestValidateTypeUnion(t *testing.T) {
	t.Parallel()

	union := map[string]any{analyzer.PropType: []any{analyzer.TypeString, analyzer.TypeNull}}
	nullOnly := map[string]any{analyzer.PropType: analyzer.TypeNull}
	unknown := map[string]any{analyzer.PropType: "frobnicate"}

	tests := []struct {
		name    string
		schema  map[string]any
		data    string
		wantErr bool
	}{
		{name: "union accepts string", schema: union, data: `"x"`, wantErr: false},
		{name: "union accepts null", schema: union, data: `null`, wantErr: false},
		{name: "union rejects number", schema: union, data: `5`, wantErr: true},
		{name: "union rejects object", schema: union, data: `{}`, wantErr: true},
		{name: "null accepts null", schema: nullOnly, data: `null`, wantErr: false},
		{name: "null rejects non-null", schema: nullOnly, data: `1`, wantErr: true},
		{name: "unknown token is a field error", schema: unknown, data: `"x"`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := analyzer.Validate(tc.schema, []byte(tc.data))
			if tc.wantErr {
				if !errors.Is(err, analyzer.ErrSchemaValidation) {
					t.Errorf("Validate(%s) = %v, want ErrSchemaValidation", tc.data, err)
				}
				return
			}
			if err != nil {
				t.Errorf("Validate(%s) = %v, want nil", tc.data, err)
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
			name:    "large but valid minimum enforced exactly",
			schema:  map[string]any{analyzer.PropType: analyzer.TypeInteger, analyzer.PropMinimum: json.Number("9007199254740993")},
			data:    `9007199254740992`,
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

// TestTypelessSchemaKeywords covers V1: a schema with no type keyword still
// enforces the keywords relevant to the instance's own kind rather than failing
// open, and never reports a type mismatch (absence means any type is allowed).
func TestTypelessSchemaKeywords(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		schema  map[string]any
		data    string
		wantErr bool
	}{
		{
			name:    "required without type rejects empty object",
			schema:  map[string]any{analyzer.PropRequired: []any{"id"}},
			data:    `{}`,
			wantErr: true,
		},
		{
			name:   "required without type accepts present property",
			schema: map[string]any{analyzer.PropRequired: []any{"id"}},
			data:   `{"id":1}`,
		},
		{
			name:    "minLength without type rejects short string",
			schema:  map[string]any{analyzer.PropMinLength: json.Number("3")},
			data:    `"ab"`,
			wantErr: true,
		},
		{
			name:   "minLength without type accepts long string",
			schema: map[string]any{analyzer.PropMinLength: json.Number("3")},
			data:   `"abcd"`,
		},
		{
			name:   "minLength without type ignores non-string",
			schema: map[string]any{analyzer.PropMinLength: json.Number("3")},
			data:   `5`,
		},
		{
			name:    "minimum without type rejects below",
			schema:  map[string]any{analyzer.PropMinimum: json.Number("10")},
			data:    `5`,
			wantErr: true,
		},
		{
			name:   "minimum without type accepts above",
			schema: map[string]any{analyzer.PropMinimum: json.Number("10")},
			data:   `12`,
		},
		{
			name: "typeless object properties enforced",
			schema: map[string]any{
				analyzer.PropProperties: map[string]any{
					"a": map[string]any{analyzer.PropType: analyzer.TypeInteger},
				},
				analyzer.PropAdditional: false,
			},
			data:    `{"b":1}`,
			wantErr: true,
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

// TestMalformedStructuralKeywords covers W4: a present-but-wrong-type structural
// keyword in a caller schema fails closed with ErrSchemaValidation rather than
// silently allowing the instance.
func TestMalformedStructuralKeywords(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		schema   map[string]any
		instance string
	}{
		{
			name:     "additionalProperties_not_bool_or_object",
			schema:   map[string]any{analyzer.PropType: analyzer.TypeObject, analyzer.PropAdditional: "bad"},
			instance: `{"x":1}`,
		},
		{
			name:     "items_not_object",
			schema:   map[string]any{analyzer.PropType: analyzer.TypeArray, analyzer.PropItems: "bad"},
			instance: `[1]`,
		},
		{
			name:     "pattern_not_string",
			schema:   map[string]any{analyzer.PropType: analyzer.TypeString, analyzer.PropPattern: 123},
			instance: `"x"`,
		},
		{
			name:     "enum_not_array",
			schema:   map[string]any{analyzer.PropType: analyzer.TypeString, analyzer.PropEnum: "x"},
			instance: `"a"`,
		},
		{
			name:     "required_not_array",
			schema:   map[string]any{analyzer.PropType: analyzer.TypeObject, analyzer.PropRequired: "id"},
			instance: `{}`,
		},
		{
			name:     "required_non_string_member",
			schema:   map[string]any{analyzer.PropType: analyzer.TypeObject, analyzer.PropRequired: []any{1}},
			instance: `{}`,
		},
		{
			name:     "properties_not_object",
			schema:   map[string]any{analyzer.PropType: analyzer.TypeObject, analyzer.PropProperties: "bad"},
			instance: `{}`,
		},
		{
			name:     "property_child_not_object",
			schema:   map[string]any{analyzer.PropType: analyzer.TypeObject, analyzer.PropProperties: map[string]any{"a": "bad"}},
			instance: `{"a":1}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := analyzer.Validate(tc.schema, []byte(tc.instance))
			if !errors.Is(err, analyzer.ErrSchemaValidation) {
				t.Errorf("Validate(%s) error = %v, want ErrSchemaValidation", tc.name, err)
			}
		})
	}
}

// TestWellFormedSchemaUnaffected covers W4: a correctly shaped derived schema
// still validates a conforming instance without a spurious keyword error.
func TestWellFormedSchemaUnaffected(t *testing.T) {
	t.Parallel()

	schema, err := analyzer.SchemaOf(invoice{})
	if err != nil {
		t.Fatalf("SchemaOf(invoice) error: %v", err)
	}
	if err := analyzer.Validate(schema, []byte(`{"number":"A-1","total":2}`)); err != nil {
		t.Errorf("Validate(valid invoice) error = %v, want nil", err)
	}
}

// TestWellFormedStructuralKeywords covers W4: the VALID shape of each hardened
// keyword still validates a conforming instance, proving the fail-closed checks
// only reject genuinely malformed keyword values.
func TestWellFormedStructuralKeywords(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		schema   map[string]any
		instance string
	}{
		{
			name:     "additionalProperties_bool",
			schema:   map[string]any{analyzer.PropType: analyzer.TypeObject, analyzer.PropAdditional: false},
			instance: `{}`,
		},
		{
			name:     "additionalProperties_schema",
			schema:   map[string]any{analyzer.PropType: analyzer.TypeObject, analyzer.PropAdditional: map[string]any{analyzer.PropType: analyzer.TypeInteger}},
			instance: `{"x":1}`,
		},
		{
			name:     "items_schema",
			schema:   map[string]any{analyzer.PropType: analyzer.TypeArray, analyzer.PropItems: map[string]any{analyzer.PropType: analyzer.TypeInteger}},
			instance: `[1,2]`,
		},
		{
			name:     "pattern_string",
			schema:   map[string]any{analyzer.PropType: analyzer.TypeString, analyzer.PropPattern: "^a+$"},
			instance: `"aaa"`,
		},
		{
			name:     "enum_array",
			schema:   map[string]any{analyzer.PropType: analyzer.TypeString, analyzer.PropEnum: []any{"a", "b"}},
			instance: `"a"`,
		},
		{
			name:     "required_array",
			schema:   map[string]any{analyzer.PropType: analyzer.TypeObject, analyzer.PropRequired: []any{"id"}},
			instance: `{"id":1}`,
		},
		{
			name:     "properties_object",
			schema:   map[string]any{analyzer.PropType: analyzer.TypeObject, analyzer.PropProperties: map[string]any{"id": map[string]any{analyzer.PropType: analyzer.TypeInteger}}},
			instance: `{"id":1}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := analyzer.Validate(tc.schema, []byte(tc.instance)); err != nil {
				t.Errorf("Validate(%s) error = %v, want nil", tc.name, err)
			}
		})
	}
}

func TestValidateExactAndUnicode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		schema        map[string]any
		doc           string
		wantErr       error  // nil means the document must validate
		wantDetailHas string // substring required in the error message
	}{
		{
			name: "large integer maximum is exact",
			// 2^53, where float64 can no longer distinguish n and n+1.
			schema:  map[string]any{analyzer.PropType: analyzer.TypeInteger, analyzer.PropMaximum: json.Number("9007199254740992")},
			doc:     `9007199254740993`,
			wantErr: analyzer.ErrSchemaValidation,
		},
		{
			name:    "unicode length counts runes",
			schema:  map[string]any{analyzer.PropType: analyzer.TypeString, analyzer.PropMaxLength: 1},
			doc:     `"é"`,
			wantErr: nil,
		},
		{
			name:    "int keyword from caller schema enforced",
			schema:  map[string]any{analyzer.PropType: analyzer.TypeInteger, analyzer.PropMinimum: 5},
			doc:     `3`,
			wantErr: analyzer.ErrSchemaValidation,
		},
		{
			name:    "string slice enum rejects non-member",
			schema:  map[string]any{analyzer.PropType: analyzer.TypeString, analyzer.PropEnum: []string{"new", "active"}},
			doc:     `"paused"`,
			wantErr: analyzer.ErrSchemaValidation,
		},
		{
			name:    "string slice enum accepts member",
			schema:  map[string]any{analyzer.PropType: analyzer.TypeString, analyzer.PropEnum: []string{"new", "active"}},
			doc:     `"new"`,
			wantErr: nil,
		},
		{
			name:          "invalid pattern surfaced not skipped",
			schema:        map[string]any{analyzer.PropType: analyzer.TypeString, analyzer.PropPattern: "("},
			doc:           `"anything"`,
			wantErr:       analyzer.ErrSchemaValidation,
			wantDetailHas: "invalid",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := analyzer.Validate(tc.schema, []byte(tc.doc))
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate(%s) error = %v, want errors.Is %v", tc.doc, err, tc.wantErr)
			}
			if tc.wantDetailHas != "" && !strings.Contains(err.Error(), tc.wantDetailHas) {
				t.Errorf("Validate(%s) error %q should contain %q", tc.doc, err, tc.wantDetailHas)
			}
		})
	}
}

func TestExactNumericPrecision(t *testing.T) {
	t.Parallel()

	type tagMin struct {
		N int64 `json:"n" jsonschema:"min=9007199254740993"`
	}
	type tagEnum struct {
		N int64 `json:"n" jsonschema:"enum=9007199254740993"`
	}

	schemaOf := func(t *testing.T, v any) map[string]any {
		t.Helper()
		s, err := analyzer.SchemaOf(v)
		if err != nil {
			t.Fatalf("SchemaOf(%T) error: %v", v, err)
		}
		return s
	}

	tests := []struct {
		name string
		// schema builds the schema under test; t is available for SchemaOf setup.
		schema func(t *testing.T) map[string]any
		// wantLiteral, when set, must appear in the marshaled schema so the exact
		// literal is preserved for the model.
		wantLiteral string
		accept      string // JSON document expected to validate
		reject      string // JSON document expected to fail validation
	}{
		{
			name:        "tag bound stores and validates exact literal",
			schema:      func(t *testing.T) map[string]any { return schemaOf(t, tagMin{}) },
			wantLiteral: "9007199254740993",
			accept:      `{"n":9007199254740993}`,
			reject:      `{"n":9007199254740992}`,
		},
		{
			name:   "numeric enum matches exact member only",
			schema: func(t *testing.T) map[string]any { return schemaOf(t, tagEnum{}) },
			accept: `{"n":9007199254740993}`,
			reject: `{"n":9007199254740992}`,
		},
		{
			name: "caller schema uint64 minimum enforced exactly",
			schema: func(*testing.T) map[string]any {
				return map[string]any{
					analyzer.PropType:    analyzer.TypeInteger,
					analyzer.PropMinimum: uint64(9007199254740993),
				}
			},
			accept: `9007199254740993`,
			reject: `9007199254740992`,
		},
		{
			// 2^100 and 2^100+1 round to the same big.Float at 64-bit precision but
			// are distinct big.Rat values.
			name: "bound beyond big.Float precision is exact",
			schema: func(*testing.T) map[string]any {
				return map[string]any{
					analyzer.PropType:    analyzer.TypeInteger,
					analyzer.PropMaximum: json.Number("1267650600228229401496703205376"),
				}
			},
			accept: `1267650600228229401496703205376`,
			reject: `1267650600228229401496703205377`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			schema := tc.schema(t)
			if tc.wantLiteral != "" {
				marshaled, err := json.Marshal(schema)
				if err != nil {
					t.Fatalf("Marshal(schema) error: %v", err)
				}
				if !strings.Contains(string(marshaled), tc.wantLiteral) {
					t.Errorf("schema %s should keep the exact literal %s", marshaled, tc.wantLiteral)
				}
			}
			if err := analyzer.Validate(schema, []byte(tc.accept)); err != nil {
				t.Errorf("Validate(%s) = %v, want nil", tc.accept, err)
			}
			if err := analyzer.Validate(schema, []byte(tc.reject)); !errors.Is(err, analyzer.ErrSchemaValidation) {
				t.Errorf("Validate(%s) = %v, want ErrSchemaValidation", tc.reject, err)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	schema := map[string]any{
		analyzer.PropType:       analyzer.TypeObject,
		analyzer.PropAdditional: false,
		analyzer.PropRequired:   []string{"name", "age", "status"},
		analyzer.PropProperties: map[string]any{
			"name":   map[string]any{analyzer.PropType: analyzer.TypeString, analyzer.PropMinLength: float64(1), analyzer.PropMaxLength: float64(4)},
			"age":    map[string]any{analyzer.PropType: analyzer.TypeInteger, analyzer.PropMinimum: float64(0), analyzer.PropMaximum: float64(10)},
			"status": map[string]any{analyzer.PropType: analyzer.TypeString, analyzer.PropEnum: []any{"new", "active"}},
			"tags":   map[string]any{analyzer.PropType: analyzer.TypeArray, analyzer.PropItems: map[string]any{analyzer.PropType: analyzer.TypeString}},
		},
	}

	tests := []struct {
		name string
		data string
		want []analyzer.FieldError
	}{
		{
			name: "valid",
			data: `{"name":"ana","age":3,"status":"new"}`,
		},
		{
			name: "missing required",
			data: `{"name":"ana"}`,
			want: []analyzer.FieldError{
				{Path: "age", Rule: analyzer.PropRequired, Detail: "missing required property"},
				{Path: "status", Rule: analyzer.PropRequired, Detail: "missing required property"},
			},
		},
		{
			name: "wrong type",
			data: `{"name":"ana","age":"old","status":"new"}`,
			want: []analyzer.FieldError{
				{Path: "age", Rule: analyzer.PropType, Detail: "expected integer, got string"},
			},
		},
		{
			name: "enum violation",
			data: `{"name":"ana","age":3,"status":"paused"}`,
			want: []analyzer.FieldError{
				{Path: "status", Rule: analyzer.PropEnum, Detail: "value paused is not one of [new, active]"},
			},
		},
		{
			name: "number out of range",
			data: `{"name":"ana","age":99,"status":"new"}`,
			want: []analyzer.FieldError{
				{Path: "age", Rule: analyzer.PropMaximum, Detail: "value 99 exceeds maximum 10"},
			},
		},
		{
			name: "string too long",
			data: `{"name":"annabelle","age":3,"status":"new"}`,
			want: []analyzer.FieldError{
				{Path: "name", Rule: analyzer.PropMaxLength, Detail: "length 9 exceeds maximum 4"},
			},
		},
		{
			name: "unexpected property",
			data: `{"name":"ana","age":3,"status":"new","extra":true}`,
			want: []analyzer.FieldError{
				{Path: "extra", Rule: analyzer.PropAdditional, Detail: "unexpected property"},
			},
		},
		{
			name: "array item type",
			data: `{"name":"ana","age":3,"status":"new","tags":["ok",5]}`,
			want: []analyzer.FieldError{
				{Path: "tags[1]", Rule: analyzer.PropType, Detail: "expected string, got number"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := analyzer.Validate(schema, []byte(tc.data))
			if len(tc.want) == 0 {
				if err != nil {
					t.Fatalf("Validate(%s) = %v, want nil", tc.data, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate(%s) = nil, want errors", tc.data)
			}
			if !errors.Is(err, analyzer.ErrSchemaValidation) {
				t.Errorf("Validate(%s) not errors.Is ErrSchemaValidation", tc.data)
			}
			got := collectFieldErrors(err)
			if diff := cmp.Diff(tc.want, got, cmpopts.SortSlices(lessFieldError)); diff != "" {
				t.Errorf("Validate(%s) mismatch (-want +got):\n%s", tc.data, diff)
			}
		})
	}
}

// TestValidateByteSliceBase64 covers W11: a byte-slice schema accepts a valid
// base64 string, and rejects both an invalid-base64 string and a JSON array with
// ErrSchemaValidation, so schema-validity tracks encoding/json decodability.
func TestValidateByteSliceBase64(t *testing.T) {
	t.Parallel()

	schema, err := schemaOfField(t, reflect.TypeOf([]byte(nil)), "field", "")
	if err != nil {
		t.Fatalf("schemaOfField([]byte) error: %v", err)
	}
	field := prop(schema, "field")
	tests := []struct {
		name    string
		doc     string
		wantErr error
	}{
		{name: "valid_base64", doc: `"aGk="`, wantErr: nil},
		{name: "empty_string", doc: `""`, wantErr: nil},
		{name: "invalid_base64", doc: `"!!!"`, wantErr: analyzer.ErrSchemaValidation},
		{name: "json_array", doc: `[104,105]`, wantErr: analyzer.ErrSchemaValidation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := analyzer.Validate(field, []byte(tc.doc)); !errors.Is(err, tc.wantErr) {
				t.Errorf("Validate(%s) = %v, want errors.Is %v", tc.doc, err, tc.wantErr)
			}
		})
	}
}

// TestValidateByteSliceDecodeEquivalence covers W11: for a matrix of candidate
// strings, byte-slice schema acceptance matches whether encoding/json decodes the
// quoted string into a []byte, so validation can never diverge from decoding.
func TestValidateByteSliceDecodeEquivalence(t *testing.T) {
	t.Parallel()

	byteType := reflect.TypeOf([]byte(nil))
	schema, err := schemaOfField(t, byteType, "field", "")
	if err != nil {
		t.Fatalf("schemaOfField([]byte) error: %v", err)
	}
	field := prop(schema, "field")
	samples := []string{"aGk=", "", "AA==", "Zm9vYmFy", "!!!", "aGk", "====", "not base64!"}
	for _, s := range samples {
		t.Run(s, func(t *testing.T) {
			t.Parallel()
			quoted, err := json.Marshal(s)
			if err != nil {
				t.Fatalf("json.Marshal(%q): %v", s, err)
			}
			gotAccept := analyzer.Validate(field, quoted) == nil
			wantAccept := stringOptionDecodes(t, byteType, s)
			if gotAccept != wantAccept {
				t.Errorf("Validate(%q) accepted = %t, want %t (encoding/json decodability)", s, gotAccept, wantAccept)
			}
		})
	}
}

// TestValidateJSONNumber covers W12: a type:number schema accepts integer and
// fractional numbers and rejects a non-numeric string with ErrSchemaValidation.
func TestValidateJSONNumber(t *testing.T) {
	t.Parallel()

	schema, err := schemaOfField(t, reflect.TypeOf(json.Number("")), "field", "")
	if err != nil {
		t.Fatalf("schemaOfField(json.Number) error: %v", err)
	}
	field := prop(schema, "field")
	tests := []struct {
		name    string
		doc     string
		wantErr error
	}{
		{name: "integer", doc: `12`, wantErr: nil},
		{name: "fractional", doc: `1.5`, wantErr: nil},
		{name: "quoted_number_rejected", doc: `"12"`, wantErr: analyzer.ErrSchemaValidation},
		{name: "non_numeric_rejected", doc: `"x"`, wantErr: analyzer.ErrSchemaValidation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := analyzer.Validate(field, []byte(tc.doc)); !errors.Is(err, tc.wantErr) {
				t.Errorf("Validate(%s) = %v, want errors.Is %v", tc.doc, err, tc.wantErr)
			}
		})
	}
}
