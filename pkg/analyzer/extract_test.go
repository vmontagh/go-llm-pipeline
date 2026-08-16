package analyzer_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/vmontagh/go-llm-pipeline/pkg/analyzer"
)

// TestExtractSchemaMarshalFailure covers F6: an unmarshalable WithSchema makes
// Extract fail fast, before any provider call.
func TestExtractSchemaMarshalFailure(t *testing.T) {
	t.Parallel()

	badSchema := map[string]any{
		analyzer.PropType: analyzer.TypeObject,
		analyzer.PropProperties: map[string]any{
			"total": map[string]any{
				analyzer.PropType:    analyzer.TypeNumber,
				analyzer.PropMinimum: math.NaN(),
			},
		},
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

// TestExtractNonObjectRoot covers V2: Extract rejects a WithSchema whose root
// type is present and not object, before any provider call, while object-root
// and typeless-root schemas still work.
func TestExtractNonObjectRoot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		schema  map[string]any
		wantErr bool
	}{
		{
			name:    "array root rejected",
			schema:  map[string]any{analyzer.PropType: analyzer.TypeArray, analyzer.PropItems: map[string]any{analyzer.PropType: analyzer.TypeInteger}},
			wantErr: true,
		},
		{
			name:    "string root rejected",
			schema:  map[string]any{analyzer.PropType: analyzer.TypeString},
			wantErr: true,
		},
		{
			name: "object root accepted",
			schema: map[string]any{
				analyzer.PropType: analyzer.TypeObject,
				analyzer.PropProperties: map[string]any{
					"number": map[string]any{analyzer.PropType: analyzer.TypeString},
					"total":  map[string]any{analyzer.PropType: analyzer.TypeNumber},
				},
			},
		},
		{
			name: "typeless root accepted",
			schema: map[string]any{
				analyzer.PropProperties: map[string]any{
					"number": map[string]any{analyzer.PropType: analyzer.TypeString},
				},
			},
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

func TestExtract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		responses     []string
		opts          []analyzer.ExtractOption
		want          invoice
		wantErrs      []error
		wantCalls     int
		wantPromptHas string // substring expected in the final prompt
	}{
		{
			name:      "success first attempt",
			responses: []string{`{"number":"A-1","total":42}`},
			want:      invoice{Number: "A-1", Total: 42},
			wantCalls: 1,
		},
		{
			name:          "malformed json recovers",
			responses:     []string{"sorry, no json here", `{"number":"A-2","total":10}`},
			want:          invoice{Number: "A-2", Total: 10},
			wantCalls:     2,
			wantPromptHas: "did not contain a valid JSON object",
		},
		{
			name:          "field validation recovers",
			responses:     []string{`{"number":"","total":-5}`, `{"number":"A-3","total":5}`},
			want:          invoice{Number: "A-3", Total: 5},
			wantCalls:     2,
			wantPromptHas: "minLength",
		},
		{
			name:      "persistent validation failure",
			responses: []string{`{"number":"","total":-1}`},
			opts:      []analyzer.ExtractOption{analyzer.WithMaxAttempts(2)},
			wantErrs:  []error{analyzer.ErrMaxAttemptsExceeded, analyzer.ErrSchemaValidation},
			wantCalls: 2,
		},
		{
			name:          "type mismatch recovers via field feedback",
			responses:     []string{`{"number":"A-4","total":"lots"}`, `{"number":"A-4","total":7}`},
			want:          invoice{Number: "A-4", Total: 7},
			wantCalls:     2,
			wantPromptHas: "expected number",
		},
		{
			name:      "persistent malformed json",
			responses: []string{"not json"},
			opts:      []analyzer.ExtractOption{analyzer.WithMaxAttempts(3)},
			wantErrs:  []error{analyzer.ErrMaxAttemptsExceeded, analyzer.ErrJSONMalformed},
			wantCalls: 3,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := &scriptedAnalyzer{responses: tc.responses}
			got, err := analyzer.Extract[invoice](t.Context(), a, analyzer.ExtractRequest{Prompt: "extract"}, tc.opts...)

			if len(tc.wantErrs) > 0 {
				for _, want := range tc.wantErrs {
					if !errors.Is(err, want) {
						t.Errorf("Extract() error = %v, want errors.Is %v", err, want)
					}
				}
			} else {
				if err != nil {
					t.Fatalf("Extract() unexpected error: %v", err)
				}
				if diff := cmp.Diff(tc.want, got); diff != "" {
					t.Errorf("Extract() mismatch (-want +got):\n%s", diff)
				}
			}

			if len(a.prompts) != tc.wantCalls {
				t.Errorf("Analyze called %d times, want %d", len(a.prompts), tc.wantCalls)
			}
			if !strings.Contains(a.prompts[0], "JSON Schema") {
				t.Errorf("first prompt missing schema instruction: %q", a.prompts[0])
			}
			if tc.wantPromptHas != "" {
				last := a.prompts[len(a.prompts)-1]
				if !strings.Contains(last, tc.wantPromptHas) {
					t.Errorf("final prompt missing feedback %q:\n%s", tc.wantPromptHas, last)
				}
			}
		})
	}
}

func TestExtractContextCanceled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	a := &scriptedAnalyzer{responses: []string{`{"number":"A-1","total":1}`}}
	_, err := analyzer.Extract[invoice](ctx, a, analyzer.ExtractRequest{Prompt: "extract"})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Extract() error = %v, want context.Canceled", err)
	}
	if len(a.prompts) != 0 {
		t.Errorf("Analyze called %d times, want 0", len(a.prompts))
	}
}

func TestExtractNilAnalyzer(t *testing.T) {
	t.Parallel()
	_, err := analyzer.Extract[invoice](t.Context(), nil, analyzer.ExtractRequest{Prompt: "x"})
	if !errors.Is(err, analyzer.ErrNilAnalyzer) {
		t.Errorf("Extract(nil) error = %v, want ErrNilAnalyzer", err)
	}
}

// TestExtractStrictDecoding proves the validate-before-decode ordering. Under
// the default schema (additionalProperties:false) an unknown field is a schema
// violation caught before decoding; with a permissive schema the same field
// reaches DisallowUnknownFields and surfaces ErrDecode.
func TestExtractStrictDecoding(t *testing.T) {
	t.Parallel()

	const response = `{"number":"A-1","total":1,"extra":true}`
	permissive := map[string]any{
		analyzer.PropType: analyzer.TypeObject,
		analyzer.PropProperties: map[string]any{
			"number": map[string]any{analyzer.PropType: analyzer.TypeString},
			"total":  map[string]any{analyzer.PropType: analyzer.TypeNumber},
		},
	}

	tests := []struct {
		name    string
		opts    []analyzer.ExtractOption
		wantErr error
	}{
		{
			name:    "default schema rejects unknown field as validation error",
			opts:    []analyzer.ExtractOption{analyzer.WithStrictDecoding(true), analyzer.WithMaxAttempts(1)},
			wantErr: analyzer.ErrSchemaValidation,
		},
		{
			name:    "permissive schema surfaces decode error",
			opts:    []analyzer.ExtractOption{analyzer.WithSchema(permissive), analyzer.WithStrictDecoding(true), analyzer.WithMaxAttempts(1)},
			wantErr: analyzer.ErrDecode,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := &scriptedAnalyzer{responses: []string{response}}
			_, err := analyzer.Extract[invoice](t.Context(), a, analyzer.ExtractRequest{Prompt: "x"}, tc.opts...)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("Extract() strict error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestExtractByteSliceRoundTrip covers W11: a []byte field round-trips through a
// base64 string response, so a schema-valid model reply decodes without a
// false-negative retry loop.
func TestExtractByteSliceRoundTrip(t *testing.T) {
	t.Parallel()

	type withBytes struct {
		B []byte `json:"b"`
	}
	a := &scriptedAnalyzer{responses: []string{`{"b":"aGk="}`}}
	got, err := analyzer.Extract[withBytes](t.Context(), a, analyzer.ExtractRequest{Prompt: "x"})
	if err != nil {
		t.Fatalf("Extract[withBytes] error: %v", err)
	}
	if diff := cmp.Diff([]byte("hi"), got.B); diff != "" {
		t.Errorf("decoded bytes mismatch (-want +got):\n%s", diff)
	}
	if len(a.prompts) != 1 {
		t.Errorf("Analyze called %d times, want 1", len(a.prompts))
	}
}

// TestExtractJSONNumberRoundTrip covers W12: a json.Number field round-trips
// through a bare numeric response instead of being rejected as a non-string.
func TestExtractJSONNumberRoundTrip(t *testing.T) {
	t.Parallel()

	type withNumber struct {
		N json.Number `json:"n"`
	}
	a := &scriptedAnalyzer{responses: []string{`{"n":12}`}}
	got, err := analyzer.Extract[withNumber](t.Context(), a, analyzer.ExtractRequest{Prompt: "x"})
	if err != nil {
		t.Fatalf("Extract[withNumber] error: %v", err)
	}
	if got.N != json.Number("12") {
		t.Errorf("Extract[withNumber].N = %q, want %q", got.N, "12")
	}
	if len(a.prompts) != 1 {
		t.Errorf("Analyze called %d times, want 1", len(a.prompts))
	}
}
