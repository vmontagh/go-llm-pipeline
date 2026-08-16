package analyzer_test

import (
	"errors"
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/pkg/analyzer"
)

func TestSchemaOfNumericEnum(t *testing.T) {
	t.Parallel()
	type roll struct {
		Face int `json:"face" jsonschema:"enum=1|2|3"`
	}
	got, err := analyzer.SchemaOf(roll{})
	if err != nil {
		t.Fatalf("SchemaOf(roll{}) unexpected error: %v", err)
	}
	want := map[string]any{
		analyzer.PropType:       analyzer.TypeObject,
		analyzer.PropAdditional: false,
		analyzer.PropRequired:   []string{"face"},
		analyzer.PropProperties: map[string]any{
			"face": map[string]any{
				analyzer.PropType: analyzer.TypeInteger,
				analyzer.PropEnum: []any{float64(1), float64(2), float64(3)},
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
			less := func(a, b analyzer.FieldError) bool { return a.Path < b.Path }
			sort.Slice(got, func(i, j int) bool { return got[i].Path < got[j].Path })
			sort.Slice(tc.want, func(i, j int) bool { return tc.want[i].Path < tc.want[j].Path })
			if diff := cmp.Diff(tc.want, got, cmpopts.SortSlices(less)); diff != "" {
				t.Errorf("Validate(%s) mismatch (-want +got):\n%s", tc.data, diff)
			}
		})
	}
}

func collectFieldErrors(err error) []analyzer.FieldError {
	var out []analyzer.FieldError
	var walk func(error)
	walk = func(e error) {
		if e == nil {
			return
		}
		if joined, ok := e.(interface{ Unwrap() []error }); ok {
			for _, sub := range joined.Unwrap() {
				walk(sub)
			}
			return
		}
		var fe *analyzer.FieldError
		if errors.As(e, &fe) {
			out = append(out, *fe)
		}
	}
	walk(err)
	return out
}
