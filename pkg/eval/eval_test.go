package eval_test

import (
	"context"
	"errors"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"gopkg.in/yaml.v3"

	"github.com/vmontagh/go-llm-pipeline/pkg/eval"
)

type photo struct {
	Heading string   `json:"heading" yaml:"heading"`
	Tags    []string `json:"tags" yaml:"tags"`
}

type example = eval.Example[string, photo]

var (
	errTarget = errors.New("target failed")
	errBroken = errors.New("evaluator failed")
	catPhoto  = photo{Heading: "Cat", Tags: []string{"cat"}}
	dogPhoto  = photo{Heading: "Dog", Tags: []string{"dog"}}
)

// stubEvaluator returns a fixed score or error.
type stubEvaluator struct {
	name  string
	score eval.Score
	err   error
}

func (s *stubEvaluator) Name() string { return s.name }
func (s *stubEvaluator) Evaluate(context.Context, photo, photo) (eval.Score, error) {
	return s.score, s.err
}

func TestLoadDataset(t *testing.T) {
	tests := []struct {
		name        string
		file        string // dataset.yaml content; empty means no file
		want        []example
		wantErr     error
		wantYAMLErr bool
	}{
		{
			name: "valid",
			file: "- name: cat\n  input: cat.jpg\n  want: {heading: Cat, tags: [cat]}\n",
			want: []example{{Name: "cat", Input: "cat.jpg", Want: catPhoto}},
		},
		{name: "missingFile", wantErr: fs.ErrNotExist},
		{name: "empty", file: "[]\n", wantErr: eval.ErrInvalidDataset},
		{name: "missingName", file: "- input: a.jpg\n", wantErr: eval.ErrInvalidDataset},
		{name: "duplicateName", file: "- name: a\n- name: a\n", wantErr: eval.ErrInvalidDataset},
		{name: "unknownField", file: "- name: a\n  inptu: a.jpg\n", wantYAMLErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fsys := fstest.MapFS{}
			if tt.file != "" {
				fsys["dataset.yaml"] = &fstest.MapFile{Data: []byte(tt.file)}
			}

			got, err := eval.LoadDataset[string, photo](fsys, "dataset.yaml")

			if tt.wantYAMLErr {
				var typeErr *yaml.TypeError
				if !errors.As(err, &typeErr) {
					t.Fatalf("LoadDataset() error = %v, want *yaml.TypeError", err)
				}
				return
			}
			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("LoadDataset() error mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("LoadDataset() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRun(t *testing.T) {
	fieldMatch := eval.FieldMatch[photo]{}
	outputs := map[string]photo{"cat.jpg": catPhoto, "dog.jpg": dogPhoto}
	target := func(_ context.Context, in string) (photo, error) {
		out, ok := outputs[in]
		if !ok {
			return photo{}, errTarget
		}
		return out, nil
	}
	tests := []struct {
		name       string
		examples   []example
		evaluators []eval.Evaluator[photo]
		cancel     bool
		want       *eval.Report
		wantErr    error
	}{
		{
			name: "allPassSortedByName",
			examples: []example{
				{Name: "b", Input: "dog.jpg", Want: dogPhoto},
				{Name: "a", Input: "cat.jpg", Want: catPhoto},
			},
			evaluators: []eval.Evaluator[photo]{fieldMatch},
			want: &eval.Report{
				Examples: []eval.ExampleResult{
					{Name: "a", Pass: true, Scores: []eval.Score{matchScore(true, true)}},
					{Name: "b", Pass: true, Scores: []eval.Score{matchScore(true, true)}},
				},
				PassRate: 1,
				Evaluators: map[string]eval.Summary{
					"field_match": {Mean: 1, PassRate: 1, FieldAccuracy: map[string]float64{"heading": 1, "tags": 1}},
				},
			},
		},
		{
			name: "partialMatchAndTargetError",
			examples: []example{
				{Name: "a", Input: "cat.jpg", Want: photo{Heading: "Cat", Tags: []string{"pet"}}},
				{Name: "b", Input: "missing.jpg", Want: dogPhoto},
			},
			evaluators: []eval.Evaluator[photo]{fieldMatch},
			want: &eval.Report{
				Examples: []eval.ExampleResult{
					{Name: "a", Scores: []eval.Score{{Evaluator: "field_match", Value: 0.5, Fields: map[string]bool{"heading": true, "tags": false}}}},
					{Name: "b", Err: errTarget},
				},
				Evaluators: map[string]eval.Summary{
					"field_match": {Mean: 0.25, FieldAccuracy: map[string]float64{"heading": 1, "tags": 0}},
				},
			},
		},
		{
			name:       "anyEvaluatorFailureFailsExample",
			examples:   []example{{Name: "a", Input: "cat.jpg", Want: dogPhoto}},
			evaluators: []eval.Evaluator[photo]{fieldMatch, &stubEvaluator{name: "ok", score: eval.Score{Value: 1, Pass: true}}},
			want: &eval.Report{
				Examples: []eval.ExampleResult{{Name: "a", Scores: []eval.Score{
					{Evaluator: "field_match", Fields: map[string]bool{"heading": false, "tags": false}},
					{Evaluator: "ok", Value: 1, Pass: true},
				}}},
				Evaluators: map[string]eval.Summary{
					"field_match": {FieldAccuracy: map[string]float64{"heading": 0, "tags": 0}},
					"ok":          {Mean: 1, PassRate: 1},
				},
			},
		},
		{name: "noExamples", evaluators: []eval.Evaluator[photo]{fieldMatch}, wantErr: eval.ErrInvalidDataset},
		{name: "duplicateExample", examples: []example{{Name: "a"}, {Name: "a"}}, evaluators: []eval.Evaluator[photo]{fieldMatch}, wantErr: eval.ErrInvalidDataset},
		{name: "noEvaluators", examples: []example{{Name: "a"}}, wantErr: eval.ErrInvalidEvaluators},
		{name: "duplicateEvaluator", examples: []example{{Name: "a"}}, evaluators: []eval.Evaluator[photo]{fieldMatch, fieldMatch}, wantErr: eval.ErrInvalidEvaluators},
		{name: "evaluatorError", examples: []example{{Name: "a", Input: "cat.jpg"}}, evaluators: []eval.Evaluator[photo]{&stubEvaluator{name: "broken", err: errBroken}}, wantErr: errBroken},
		{name: "canceled", examples: []example{{Name: "a", Input: "cat.jpg"}}, evaluators: []eval.Evaluator[photo]{fieldMatch}, cancel: true, wantErr: context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tt.cancel {
				cancel()
			}

			got, err := eval.Run(ctx, tt.examples, target, tt.evaluators...)

			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("Run() error mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.want, got, cmpopts.EquateErrors(), cmpopts.EquateApprox(0, 1e-9)); diff != "" {
				t.Errorf("Run() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func matchScore(heading, tags bool) eval.Score {
	return eval.Score{Evaluator: "field_match", Value: 1, Pass: true, Fields: map[string]bool{"heading": heading, "tags": tags}}
}

func TestReportCheck(t *testing.T) {
	tests := []struct {
		name     string
		passRate float64
		min      float64
		wantErr  error
	}{
		{name: "above", passRate: 0.9, min: 0.8},
		{name: "equal", passRate: 0.8, min: 0.8},
		{name: "below", passRate: 0.7, min: 0.8, wantErr: eval.ErrBelowThreshold},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := &eval.Report{PassRate: tt.passRate}

			err := r.Check(tt.min)

			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Check(%v) with pass rate %v mismatch (-want +got):\n%s", tt.min, tt.passRate, diff)
			}
		})
	}
}
