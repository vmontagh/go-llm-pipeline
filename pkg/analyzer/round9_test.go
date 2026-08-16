package analyzer_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/vmontagh/go-llm-pipeline/pkg/analyzer"
)

// stringOptionDecodes reports whether s decodes into a json:",string" field of
// the given kind exactly as encoding/json would. It is the oracle the derived
// schema must agree with, so schema-validity and decodability never diverge.
func stringOptionDecodes(t *testing.T, typ reflect.Type, s string) bool {
	t.Helper()
	holder := reflect.StructOf([]reflect.StructField{{
		Name: "Field",
		Type: typ,
		Tag:  reflect.StructTag(`json:"field,string"`),
	}})
	quoted, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("json.Marshal(%q): %v", s, err)
	}
	body := []byte(`{"field":`)
	body = append(body, quoted...)
	body = append(body, '}')
	return json.Unmarshal(body, reflect.New(holder).Interface()) == nil
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

// TestNilAnalyzerSentinel covers W9: Extract with a nil analyzer returns the
// ErrNilAnalyzer sentinel, matched via errors.Is rather than its message string.
func TestNilAnalyzerSentinel(t *testing.T) {
	t.Parallel()

	_, err := analyzer.Extract[invoice](t.Context(), nil, analyzer.ExtractRequest{Prompt: "x"})
	if !errors.Is(err, analyzer.ErrNilAnalyzer) {
		t.Errorf("Extract(nil) error = %v, want ErrNilAnalyzer", err)
	}
}

// TestExtractObjectRootUnion covers W5: an object-inclusive union root (as []any
// or native []string) is accepted and extraction proceeds, while an
// object-excluding or empty union is rejected before any provider call.
func TestExtractObjectRootUnion(t *testing.T) {
	t.Parallel()

	objectProps := map[string]any{
		"number": map[string]any{analyzer.PropType: analyzer.TypeString},
		"total":  map[string]any{analyzer.PropType: analyzer.TypeNumber},
	}
	tests := []struct {
		name    string
		schema  map[string]any
		wantErr bool
	}{
		{
			name:   "object_null_union_any",
			schema: map[string]any{analyzer.PropType: []any{analyzer.TypeObject, analyzer.TypeNull}, analyzer.PropProperties: objectProps},
		},
		{
			name:   "object_union_strings",
			schema: map[string]any{analyzer.PropType: []string{analyzer.TypeObject}, analyzer.PropProperties: objectProps},
		},
		{
			name:    "array_null_union_rejected",
			schema:  map[string]any{analyzer.PropType: []any{analyzer.TypeArray, analyzer.TypeNull}, analyzer.PropItems: map[string]any{analyzer.PropType: analyzer.TypeInteger}},
			wantErr: true,
		},
		{
			name:    "empty_union_rejected",
			schema:  map[string]any{analyzer.PropType: []any{}},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := &scriptedAnalyzer{responses: []string{`{"number":"A-1","total":1}`}}
			_, err := analyzer.Extract[invoice](t.Context(), a, analyzer.ExtractRequest{Prompt: "x"}, analyzer.WithSchema(tc.schema))
			if tc.wantErr {
				if !errors.Is(err, analyzer.ErrSchemaDerivation) {
					t.Errorf("Extract() error = %v, want ErrSchemaDerivation", err)
				}
				if len(a.prompts) != 0 {
					t.Errorf("Analyze called %d times, want 0", len(a.prompts))
				}
				return
			}
			if err != nil {
				t.Errorf("Extract() error = %v, want nil", err)
			}
			if len(a.prompts) != 1 {
				t.Errorf("Analyze called %d times, want 1", len(a.prompts))
			}
		})
	}
}

// TestExtractTypelessRoot covers W6: a typeless schema carrying non-object
// constraints is rejected up front, while an object-shaped or empty typeless
// schema still extracts.
func TestExtractTypelessRoot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		schema  map[string]any
		wantErr bool
	}{
		{
			name:    "typeless_items_rejected",
			schema:  map[string]any{analyzer.PropItems: map[string]any{analyzer.PropType: analyzer.TypeInteger}},
			wantErr: true,
		},
		{
			name:    "typeless_minlength_rejected",
			schema:  map[string]any{analyzer.PropMinLength: json.Number("1")},
			wantErr: true,
		},
		{
			name:   "typeless_object_shaped_accepted",
			schema: map[string]any{analyzer.PropProperties: map[string]any{"number": map[string]any{analyzer.PropType: analyzer.TypeString}}},
		},
		{
			name:   "empty_schema_accepted",
			schema: map[string]any{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := &scriptedAnalyzer{responses: []string{`{"number":"A-1","total":1}`}}
			_, err := analyzer.Extract[invoice](t.Context(), a, analyzer.ExtractRequest{Prompt: "x"}, analyzer.WithSchema(tc.schema))
			if tc.wantErr {
				if !errors.Is(err, analyzer.ErrSchemaDerivation) {
					t.Errorf("Extract() error = %v, want ErrSchemaDerivation", err)
				}
				if len(a.prompts) != 0 {
					t.Errorf("Analyze called %d times, want 0", len(a.prompts))
				}
				return
			}
			if err != nil {
				t.Errorf("Extract() error = %v, want nil", err)
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
