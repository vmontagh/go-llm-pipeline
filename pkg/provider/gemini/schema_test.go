package gemini

import (
	"testing"

	"github.com/google/generative-ai-go/genai"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestConvertJSONSchema(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   map[string]any
		want *genai.Schema
	}{
		{name: "nil input", in: nil, want: nil},
		{name: "empty input", in: map[string]any{}, want: nil},
		{name: "missing type collapses to nil", in: map[string]any{"description": "x"}, want: nil},
		{
			name: "primitive string with description",
			in:   map[string]any{"type": "string", "description": "d"},
			want: &genai.Schema{Type: genai.TypeString, Description: "d"},
		},
		{
			name: "nullable object",
			in:   map[string]any{"type": "object", "nullable": true},
			want: &genai.Schema{Type: genai.TypeObject, Nullable: true},
		},
		{
			name: "array of strings",
			in: map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
			want: &genai.Schema{
				Type:  genai.TypeArray,
				Items: &genai.Schema{Type: genai.TypeString},
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
			want: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"heading":     {Type: genai.TypeString},
					"description": {Type: genai.TypeString},
					"tags": {
						Type:  genai.TypeArray,
						Items: &genai.Schema{Type: genai.TypeString},
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
			want: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"n": {Type: genai.TypeNumber},
					"i": {Type: genai.TypeInteger},
					"b": {Type: genai.TypeBoolean},
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
			want: &genai.Schema{
				Type:     genai.TypeObject,
				Required: []string{"a", "b"},
				Properties: map[string]*genai.Schema{
					"a": {Type: genai.TypeString},
					"b": {Type: genai.TypeString},
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
