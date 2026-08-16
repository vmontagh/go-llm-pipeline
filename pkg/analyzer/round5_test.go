package analyzer_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/vmontagh/go-llm-pipeline/pkg/analyzer"
)

// chain is a cyclic named slice type (H1): it refers to itself and cannot be
// reflected into a finite schema.
type chain []chain

// tree is a cyclic named map type (H1).
type tree map[string]tree

// stringyInt decodes from a JSON string via a custom UnmarshalJSON, so its wire
// shape cannot be reflected from its underlying integer kind (H2).
type stringyInt int

func (s *stringyInt) UnmarshalJSON([]byte) error { return nil }

// tags is a named slice with custom JSON unmarshaling (H2).
type tags []string

func (t *tags) UnmarshalJSON([]byte) error { return nil }

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

// TestValidateCompositeEnum covers H3: enum is enforced for array and object
// instances, with numeric-aware member matching.
func TestValidateCompositeEnum(t *testing.T) {
	t.Parallel()

	arraySchema := map[string]any{
		analyzer.PropType:  analyzer.TypeArray,
		analyzer.PropItems: map[string]any{analyzer.PropType: analyzer.TypeInteger},
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
		{name: "array enum numeric-aware", schema: arraySchema, data: `[1.0,2]`, wantErr: false},
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

// TestSchemaOfMagnitudeBound covers H4 at derivation: an oversized bound literal
// in a jsonschema tag is rejected with ErrSchemaDerivation.
func TestSchemaOfMagnitudeBound(t *testing.T) {
	t.Parallel()

	_, err := schemaOfField(t, reflect.TypeOf(0), "field", "min=1e999999999")
	if !errors.Is(err, analyzer.ErrSchemaDerivation) {
		t.Errorf("schemaOfField(min=1e999999999) error = %v, want ErrSchemaDerivation", err)
	}
}
