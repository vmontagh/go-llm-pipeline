package analyzer_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/vmontagh/go-llm-pipeline/pkg/analyzer"
)

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

// TestExtractStringOptionRoundTrip covers S1: an int field with json:",string"
// derives type:string and Extract still decodes a quoted "123" value.
func TestExtractStringOptionRoundTrip(t *testing.T) {
	t.Parallel()

	type quoted struct {
		N int `json:"n,string"`
	}
	a := &scriptedAnalyzer{responses: []string{`{"n":"123"}`}}
	got, err := analyzer.Extract[quoted](t.Context(), a, analyzer.ExtractRequest{Prompt: "x"})
	if err != nil {
		t.Fatalf("Extract[quoted] error: %v", err)
	}
	if got.N != 123 {
		t.Errorf("Extract[quoted].N = %d, want 123", got.N)
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
