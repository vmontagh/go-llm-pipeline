package analyzer_test

import (
	"encoding/json"
	"errors"
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/pkg/analyzer"
)

// TestValidateUnknownKeyword covers the fail-closed invariant of the keyword-rule
// model: a schema keyword the validator does not recognize is rejected with a
// FieldError rather than silently ignored, at the root and at any nested level, so
// a misspelled or unsupported keyword cannot mask a missing constraint.
func TestValidateUnknownKeyword(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		schema   map[string]any
		instance string
		want     []analyzer.FieldError
	}{
		{
			name:     "root_unknown_keyword",
			schema:   map[string]any{analyzer.PropType: analyzer.TypeObject, "title": "person"},
			instance: `{}`,
			want:     []analyzer.FieldError{{Path: "", Rule: "title", Detail: "unrecognized schema keyword"}},
		},
		{
			name: "multiple_unknown_keywords_sorted",
			schema: map[string]any{
				analyzer.PropType: analyzer.TypeObject,
				"$schema":         "https://json-schema.org/draft/2020-12/schema",
				"title":           "person",
			},
			instance: `{}`,
			want: []analyzer.FieldError{
				{Path: "", Rule: "$schema", Detail: "unrecognized schema keyword"},
				{Path: "", Rule: "title", Detail: "unrecognized schema keyword"},
			},
		},
		{
			name: "nested_property_unknown_keyword_present",
			schema: map[string]any{
				analyzer.PropType: analyzer.TypeObject,
				analyzer.PropProperties: map[string]any{
					"age": map[string]any{analyzer.PropType: analyzer.TypeInteger, "format": "int32"},
				},
			},
			instance: `{"age":3}`,
			want:     []analyzer.FieldError{{Path: "age", Rule: "format", Detail: "unrecognized schema keyword"}},
		},
		{
			name: "nested_property_unknown_keyword_absent_instance",
			schema: map[string]any{
				analyzer.PropType: analyzer.TypeObject,
				analyzer.PropProperties: map[string]any{
					"age": map[string]any{analyzer.PropType: analyzer.TypeInteger, "format": "int32"},
				},
			},
			instance: `{}`,
			want:     []analyzer.FieldError{{Path: "age", Rule: "format", Detail: "unrecognized schema keyword"}},
		},
		{
			name: "items_schema_unknown_keyword_present",
			schema: map[string]any{
				analyzer.PropType:  analyzer.TypeArray,
				analyzer.PropItems: map[string]any{analyzer.PropType: analyzer.TypeString, "contentEncoding": "utf-8"},
			},
			instance: `["a"]`,
			want:     []analyzer.FieldError{{Path: "[]", Rule: "contentEncoding", Detail: "unrecognized schema keyword"}},
		},
		{
			name: "items_schema_unknown_keyword_empty_array",
			schema: map[string]any{
				analyzer.PropType:  analyzer.TypeArray,
				analyzer.PropItems: map[string]any{analyzer.PropType: analyzer.TypeString, "contentEncoding": "utf-8"},
			},
			instance: `[]`,
			want:     []analyzer.FieldError{{Path: "[]", Rule: "contentEncoding", Detail: "unrecognized schema keyword"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := analyzer.Validate(tc.schema, []byte(tc.instance))
			if !errors.Is(err, analyzer.ErrSchemaValidation) {
				t.Fatalf("Validate(%s) error = %v, want ErrSchemaValidation", tc.name, err)
			}
			got := collectFieldErrors(err)
			less := func(a, b analyzer.FieldError) bool { return a.Rule < b.Rule }
			sort.Slice(got, func(i, j int) bool { return got[i].Rule < got[j].Rule })
			if diff := cmp.Diff(tc.want, got, cmpopts.SortSlices(less)); diff != "" {
				t.Errorf("Validate(%s) FieldErrors mismatch (-want +got):\n%s", tc.name, diff)
			}
		})
	}
}

// TestValidateRecognizedKeywordsAccepted proves the fail-closed check does not
// reject the metadata keywords the validator understands but does not enforce: a
// description annotation on a conforming instance yields no error.
func TestValidateRecognizedKeywordsAccepted(t *testing.T) {
	t.Parallel()

	schema := map[string]any{
		analyzer.PropType:        analyzer.TypeObject,
		analyzer.PropDescription: "a person",
		analyzer.PropProperties: map[string]any{
			"name": map[string]any{analyzer.PropType: analyzer.TypeString, analyzer.PropDescription: "full name"},
		},
	}
	if err := analyzer.Validate(schema, []byte(`{"name":"ana"}`)); err != nil {
		t.Errorf("Validate(description schema) = %v, want nil", err)
	}
}

// TestSchemaOfEmitsOnlyRecognizedKeywords guards the coupling between derivation
// and validation: every keyword SchemaOf emits must be one Validate recognizes.
// It derives a schema exercising the object, array, string-constraint, numeric,
// enum, description, and base64 byte-slice keyword paths, then validates a
// conforming document and asserts no "unrecognized schema keyword" error appears,
// which would only happen if SchemaOf emitted a keyword outside the registry.
func TestSchemaOfEmitsOnlyRecognizedKeywords(t *testing.T) {
	t.Parallel()

	type address struct {
		City string `json:"city" jsonschema:"minLength=1"`
	}
	type person struct {
		Name    string   `json:"name" jsonschema:"minLength=1;description=full name"`
		Age     int      `json:"age" jsonschema:"min=0;max=130"`
		Rating  float64  `json:"rating"`
		Active  bool     `json:"active"`
		Status  string   `json:"status" jsonschema:"enum=new|active"`
		Code    string   `json:"code" jsonschema:"pattern=^[A-Z]+$"`
		Tags    []string `json:"tags"`
		Home    address  `json:"home"`
		Payload []byte   `json:"payload"`
	}

	schema, err := analyzer.SchemaOf(person{})
	if err != nil {
		t.Fatalf("SchemaOf(person) error: %v", err)
	}
	value := person{
		Name:    "ana",
		Age:     30,
		Status:  "new",
		Code:    "AB",
		Tags:    []string{"x"},
		Home:    address{City: "here"},
		Payload: []byte("hi"),
	}
	doc, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal(person) error: %v", err)
	}
	err = analyzer.Validate(schema, doc)
	for _, fe := range collectFieldErrors(err) {
		if fe.Detail == "unrecognized schema keyword" {
			t.Errorf("SchemaOf(person) emitted unrecognized keyword %q at %q", fe.Rule, fe.Path)
		}
	}
	if err != nil {
		t.Errorf("Validate(SchemaOf(person), conforming doc) = %v, want nil", err)
	}
}

// TestValidateUnknownKeywordCoexistsWithConstraint proves the unknown-keyword
// rejection does not suppress the other keyword rules: a schema carrying both an
// unknown keyword and a violated known constraint reports both.
func TestValidateUnknownKeywordCoexistsWithConstraint(t *testing.T) {
	t.Parallel()

	schema := map[string]any{
		analyzer.PropType:      analyzer.TypeString,
		analyzer.PropMaxLength: float64(2),
		"format":               "email",
	}
	err := analyzer.Validate(schema, []byte(`"toolong"`))
	if !errors.Is(err, analyzer.ErrSchemaValidation) {
		t.Fatalf("Validate = %v, want ErrSchemaValidation", err)
	}
	got := collectFieldErrors(err)
	want := []analyzer.FieldError{
		{Path: "", Rule: "format", Detail: "unrecognized schema keyword"},
		{Path: "", Rule: analyzer.PropMaxLength, Detail: "length 7 exceeds maximum 2"},
	}
	less := func(a, b analyzer.FieldError) bool { return a.Rule < b.Rule }
	if diff := cmp.Diff(want, got, cmpopts.SortSlices(less)); diff != "" {
		t.Errorf("Validate FieldErrors mismatch (-want +got):\n%s", diff)
	}
}
