package analyzer_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/vmontagh/go-llm-pipeline/pkg/analyzer"
)

type invoice struct {
	Number string  `json:"number" jsonschema:"minLength=1"`
	Total  float64 `json:"total" jsonschema:"min=0"`
}

// scriptedAnalyzer returns the next scripted response on each Analyze call,
// recording the prompts it received.
type scriptedAnalyzer struct {
	responses []string
	err       error
	prompts   []string
}

func (s *scriptedAnalyzer) Analyze(_ context.Context, prompt string, _ []byte, _ string, _ analyzer.PublishFn) (string, error) {
	s.prompts = append(s.prompts, prompt)
	if s.err != nil {
		return "", s.err
	}
	i := len(s.prompts) - 1
	if i >= len(s.responses) {
		return s.responses[len(s.responses)-1], nil
	}
	return s.responses[i], nil
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
