package analyzer_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/vmontagh/go-llm-pipeline/pkg/analyzer"
)

// schemaOfField derives the schema for a single field of the given type carrying
// arbitrary json and jsonschema tags, built dynamically so tag cases stay
// table-driven and govet-clean.
func schemaOfField(t *testing.T, typ reflect.Type, jsonTag, jsonschemaTag string) (map[string]any, error) {
	t.Helper()
	tag := "json:" + strconv.Quote(jsonTag)
	if jsonschemaTag != "" {
		tag += " jsonschema:" + strconv.Quote(jsonschemaTag)
	}
	st := reflect.StructOf([]reflect.StructField{{
		Name: "Field",
		Type: typ,
		Tag:  reflect.StructTag(tag),
	}})
	return analyzer.SchemaOf(reflect.New(st).Elem().Interface())
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

// TestExtractNilSchema covers G5: WithSchema(nil) fails fast before any provider
// call.
func TestExtractNilSchema(t *testing.T) {
	t.Parallel()

	a := &scriptedAnalyzer{responses: []string{`{"number":"A-1","total":1}`}}
	_, err := analyzer.Extract[invoice](t.Context(), a, analyzer.ExtractRequest{Prompt: "x"}, analyzer.WithSchema(nil))
	if !errors.Is(err, analyzer.ErrSchemaDerivation) {
		t.Errorf("Extract(WithSchema(nil)) error = %v, want ErrSchemaDerivation", err)
	}
	if len(a.prompts) != 0 {
		t.Errorf("Analyze called %d times, want 0", len(a.prompts))
	}
}
