package analyzer_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/vmontagh/go-llm-pipeline/pkg/analyzer"
)

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
				analyzer.PropType:         analyzer.TypeObject,
				analyzer.PropAdditional:   false,
				analyzer.PropRequired:     []string{"name", "age", "rating", "active", "home", "status"},
				analyzer.PropProperties: map[string]any{
					"name":   map[string]any{analyzer.PropType: analyzer.TypeString, analyzer.PropMinLength: json.Number("1")},
					"age":    map[string]any{analyzer.PropType: analyzer.TypeInteger, analyzer.PropMinimum: json.Number("0"), analyzer.PropMaximum: json.Number("130")},
					"rating": map[string]any{analyzer.PropType: analyzer.TypeNumber},
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
