package gemini

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestConvertJSONSchema(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   map[string]any
		want *schema
	}{
		{name: "nil input", in: nil, want: nil},
		{name: "empty input", in: map[string]any{}, want: nil},
		{name: "missing type collapses to nil", in: map[string]any{"description": "x"}, want: nil},
		{
			name: "primitive string with description",
			in:   map[string]any{"type": "string", "description": "d"},
			want: &schema{Type: geminiTypeString, Description: "d"},
		},
		{
			name: "nullable object",
			in:   map[string]any{"type": "object", "nullable": true},
			want: &schema{Type: geminiTypeObject, Nullable: true},
		},
		{
			name: "unknown type maps to unspecified",
			in:   map[string]any{"type": "null"},
			want: &schema{Type: geminiTypeUnspecified},
		},
		{
			name: "array of strings",
			in: map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
			want: &schema{
				Type:  geminiTypeArray,
				Items: &schema{Type: geminiTypeString},
			},
		},
		{
			name: "AnalysisResult-shaped object",
			in: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"heading":     map[string]any{"type": "string"},
					"description": map[string]any{"type": "string"},
					"tags": map[string]any{
						"type":  "array",
						"items": map[string]any{"type": "string"},
					},
				},
				"required": []any{"heading", "tags", "description"},
			},
			want: &schema{
				Type: geminiTypeObject,
				Properties: map[string]*schema{
					"heading":     {Type: geminiTypeString},
					"description": {Type: geminiTypeString},
					"tags": {
						Type:  geminiTypeArray,
						Items: &schema{Type: geminiTypeString},
					},
				},
				Required: []string{"heading", "tags", "description"},
			},
		},
		{
			name: "primitive type coverage",
			in: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"n": map[string]any{"type": "number"},
					"i": map[string]any{"type": "integer"},
					"b": map[string]any{"type": "boolean"},
				},
			},
			want: &schema{
				Type: geminiTypeObject,
				Properties: map[string]*schema{
					"n": {Type: geminiTypeNumber},
					"i": {Type: geminiTypeInteger},
					"b": {Type: geminiTypeBoolean},
				},
			},
		},
		{
			name: "required as []string is preserved",
			in: map[string]any{
				"type":     "object",
				"required": []string{"a", "b"},
				"properties": map[string]any{
					"a": map[string]any{"type": "string"},
					"b": map[string]any{"type": "string"},
				},
			},
			want: &schema{
				Type:     geminiTypeObject,
				Required: []string{"a", "b"},
				Properties: map[string]*schema{
					"a": {Type: geminiTypeString},
					"b": {Type: geminiTypeString},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := convertJSONSchema(tt.in)
			if diff := cmp.Diff(tt.want, got, cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
				t.Errorf("convertJSONSchema mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
