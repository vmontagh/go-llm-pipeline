package analyzer_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"testing"

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
		want := map[string]any{
			analyzer.PropType: analyzer.TypeObject,
			analyzer.PropAdditional: map[string]any{
				analyzer.PropType: analyzer.TypeInteger,
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

// TestValidateIntegerByValue covers F4: integer validation is by mathematical
// value, so 1e0 and 1.0 are schema-valid integers while 1.5 is not.
func TestValidateIntegerByValue(t *testing.T) {
	t.Parallel()

	schema := map[string]any{analyzer.PropType: analyzer.TypeInteger}
	tests := []struct {
		name    string
		doc     string
		wantErr error
	}{
		{name: "plain integer", doc: `1`, wantErr: nil},
		{name: "exponent form", doc: `1e0`, wantErr: nil},
		{name: "trailing zero fraction", doc: `1.0`, wantErr: nil},
		{name: "fractional rejected", doc: `1.5`, wantErr: analyzer.ErrSchemaValidation},
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

// TestExtractSchemaMarshalFailure covers F6: an unmarshalable WithSchema makes
// Extract fail fast, before any provider call.
func TestExtractSchemaMarshalFailure(t *testing.T) {
	t.Parallel()

	badSchema := map[string]any{
		analyzer.PropType:    analyzer.TypeNumber,
		analyzer.PropMinimum: math.NaN(),
	}
	a := &scriptedAnalyzer{responses: []string{`{"number":"A-1","total":1}`}}
	_, err := analyzer.Extract[invoice](t.Context(), a, analyzer.ExtractRequest{Prompt: "x"}, analyzer.WithSchema(badSchema))
	if err == nil || !strings.Contains(err.Error(), "marshaling schema") {
		t.Errorf("Extract() error = %v, want a marshaling schema error", err)
	}
	if len(a.prompts) != 0 {
		t.Errorf("Analyze called %d times, want 0", len(a.prompts))
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

// TestExtractDecodeFeedback covers F10: a persistent ErrDecode surfaces
// structure-mismatch feedback, not malformed-JSON wording.
func TestExtractDecodeFeedback(t *testing.T) {
	t.Parallel()

	permissive := map[string]any{
		analyzer.PropType: analyzer.TypeObject,
		analyzer.PropProperties: map[string]any{
			"number": map[string]any{analyzer.PropType: analyzer.TypeString},
			"total":  map[string]any{analyzer.PropType: analyzer.TypeNumber},
		},
	}
	a := &scriptedAnalyzer{responses: []string{`{"number":"A-1","total":1,"extra":true}`}}
	_, err := analyzer.Extract[invoice](t.Context(), a, analyzer.ExtractRequest{Prompt: "x"},
		analyzer.WithSchema(permissive), analyzer.WithStrictDecoding(true), analyzer.WithMaxAttempts(2))
	if !errors.Is(err, analyzer.ErrDecode) {
		t.Fatalf("Extract() error = %v, want ErrDecode", err)
	}
	last := a.prompts[len(a.prompts)-1]
	if !strings.Contains(last, "did not match the required structure") {
		t.Errorf("final prompt missing structure-mismatch feedback:\n%s", last)
	}
	if strings.Contains(last, "did not contain a valid JSON object") {
		t.Errorf("final prompt should not use malformed-JSON wording:\n%s", last)
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

// TestExtractCustomTextType covers F1: a type implementing both TextMarshaler and
// TextUnmarshaler is a JSON string and round-trips through Extract.
func TestExtractCustomTextType(t *testing.T) {
	t.Parallel()

	schema, err := analyzer.SchemaOf(textValued{})
	if err != nil {
		t.Fatalf("SchemaOf(textValued{}) error: %v", err)
	}
	if got := prop(schema, "when")[analyzer.PropType]; got != analyzer.TypeString {
		t.Fatalf("when type = %v, want string", got)
	}
	a := &scriptedAnalyzer{responses: []string{`{"when":"2020-01-01"}`}}
	got, err := analyzer.Extract[textValued](t.Context(), a, analyzer.ExtractRequest{Prompt: "x"})
	if err != nil {
		t.Fatalf("Extract[textValued] error: %v", err)
	}
	if diff := cmp.Diff(textValued{}, got); diff != "" {
		t.Errorf("Extract[textValued] mismatch (-want +got):\n%s", diff)
	}
}

// dupTagStruct builds, via reflection, a struct with two direct fields sharing
// the json name "x" (a compile-time source literal would trip govet's structtag
// check for the intentional duplicate).
func dupTagStruct() reflect.Type {
	return reflect.StructOf([]reflect.StructField{
		{Name: "A", Type: reflect.TypeOf(""), Tag: `json:"x"`},
		{Name: "B", Type: reflect.TypeOf(""), Tag: `json:"x"`},
	})
}

// schemaOfTagged derives the schema for a single field of the given kind
// carrying the supplied jsonschema tag, built dynamically so tag cases stay
// table-driven.
func schemaOfTagged(t *testing.T, kind reflect.Type, jsonschemaTag string) (map[string]any, error) {
	t.Helper()
	field := reflect.StructField{
		Name: "Field",
		Type: kind,
		Tag:  reflect.StructTag(fmt.Sprintf(`json:"field" jsonschema:%q`, jsonschemaTag)),
	}
	st := reflect.StructOf([]reflect.StructField{field})
	return analyzer.SchemaOf(reflect.New(st).Elem().Interface())
}

// propNames returns the sorted property names declared by a schema.
func propNames(schema map[string]any) []string {
	props, ok := schema[analyzer.PropProperties].(map[string]any)
	if !ok || len(props) == 0 {
		return nil
	}
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// requiredList returns the sorted required property names of a schema.
func requiredList(schema map[string]any) []string {
	req, ok := schema[analyzer.PropRequired].([]string)
	if !ok || len(req) == 0 {
		return nil
	}
	out := append([]string(nil), req...)
	sort.Strings(out)
	return out
}
