package eval_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/pkg/eval"
)

var (
	errScorer = errors.New("scorer failed")
	errPrompt = errors.New("prompt failed")
)

func TestFieldMatch(t *testing.T) {
	tests := []struct {
		name    string
		want    any
		got     any
		wantRes eval.Score
		wantErr error
	}{
		{name: "equal", want: map[string]any{"a": 1}, got: map[string]any{"a": 1}, wantRes: eval.Score{Value: 1, Pass: true, Fields: map[string]bool{"a": true}}},
		{name: "bothEmpty", want: map[string]any{}, got: map[string]any{}, wantRes: eval.Score{Value: 1, Pass: true, Fields: map[string]bool{}}},
		{name: "missingInGot", want: map[string]any{"a": 1, "b": 2}, got: map[string]any{"a": 1}, wantRes: eval.Score{Value: 0.5, Fields: map[string]bool{"a": true, "b": false}}},
		{name: "extraInGot", want: map[string]any{"a": 1}, got: map[string]any{"a": 1, "b": 2}, wantRes: eval.Score{Value: 0.5, Fields: map[string]bool{"a": true, "b": false}}},
		{name: "nullVsMissing", want: map[string]any{"a": nil}, got: map[string]any{}, wantRes: eval.Score{Fields: map[string]bool{"a": false}}},
		{name: "largeIntsDiffer", want: map[string]any{"id": int64(9007199254740993)}, got: map[string]any{"id": int64(9007199254740992)}, wantRes: eval.Score{Fields: map[string]bool{"id": false}}},
		{
			name:    "equivalentNumbers",
			want:    map[string]any{"a": json.Number("1.0"), "n": map[string]any{"x": []any{json.Number("1e0")}}, "z": math.Copysign(0, -1)},
			got:     map[string]any{"a": 1, "n": map[string]any{"x": []any{1}}, "z": 0.0},
			wantRes: eval.Score{Value: 1, Pass: true, Fields: map[string]bool{"a": true, "n": true, "z": true}},
		},
		{name: "nestedValueDiffers", want: map[string]any{"n": map[string]any{"x": []any{1, 2}}}, got: map[string]any{"n": map[string]any{"x": []any{1, 3}}}, wantRes: eval.Score{Fields: map[string]bool{"n": false}}},
		{name: "nestedLengthDiffers", want: map[string]any{"x": []any{1}}, got: map[string]any{"x": []any{1, 1}}, wantRes: eval.Score{Fields: map[string]bool{"x": false}}},
		{name: "nestedKeyMissing", want: map[string]any{"n": map[string]any{"a": nil, "b": 1}}, got: map[string]any{"n": map[string]any{"b": 1, "c": nil}}, wantRes: eval.Score{Fields: map[string]bool{"n": false}}},
		{name: "nestedKeyCountDiffers", want: map[string]any{"n": map[string]any{"b": 1}}, got: map[string]any{"n": map[string]any{"b": 1, "c": 2}}, wantRes: eval.Score{Fields: map[string]bool{"n": false}}},
		{name: "typeDiffers", want: map[string]any{"a": 1, "b": []any{}, "c": map[string]any{}}, got: map[string]any{"a": "1", "b": map[string]any{}, "c": []any{}}, wantRes: eval.Score{Fields: map[string]bool{"a": false, "b": false, "c": false}}},
		{name: "stringsAndBools", want: map[string]any{"s": "x", "b": true}, got: map[string]any{"s": "x", "b": false}, wantRes: eval.Score{Value: 0.5, Fields: map[string]bool{"s": true, "b": false}}},
		{name: "wantNotObject", want: 1, got: map[string]any{}, wantErr: eval.ErrNotObject},
		{name: "gotNotObject", want: map[string]any{}, got: []int{1}, wantErr: eval.ErrNotObject},
		{name: "gotNull", want: map[string]any{}, got: nil, wantErr: eval.ErrNotObject},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := eval.FieldMatch[any]{}.Evaluate(t.Context(), tt.want, tt.got)

			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("Evaluate(%v, %v) error mismatch (-want +got):\n%s", tt.want, tt.got, diff)
			}
			if diff := cmp.Diff(tt.wantRes, got); diff != "" {
				t.Errorf("Evaluate(%v, %v) mismatch (-want +got):\n%s", tt.want, tt.got, diff)
			}
		})
	}
}

// fakeScorer returns a fixed response and records the prompt it received.
type fakeScorer struct {
	resp   *eval.JudgeScore
	err    error
	prompt string
}

func (f *fakeScorer) Analyze(_ context.Context, prompt string, _ []byte, _ string, _ func(string, string, string, bool)) (*eval.JudgeScore, error) {
	f.prompt = prompt
	return f.resp, f.err
}

func echoPrompt(want, got string) (string, error) { return want + "|" + got, nil }

func TestNewJudge(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		scorer   eval.Scorer
		prompt   eval.JudgePrompt[string]
		minScore float64
	}{
		{name: "noID", scorer: &fakeScorer{}, prompt: echoPrompt},
		{name: "nilScorer", id: "j", prompt: echoPrompt},
		{name: "nilPrompt", id: "j", scorer: &fakeScorer{}},
		{name: "minBelowZero", id: "j", scorer: &fakeScorer{}, prompt: echoPrompt, minScore: -0.1},
		{name: "minNaN", id: "j", scorer: &fakeScorer{}, prompt: echoPrompt, minScore: math.NaN()},
		{name: "minAboveOne", id: "j", scorer: &fakeScorer{}, prompt: echoPrompt, minScore: 1.1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := eval.NewJudge(tt.id, tt.scorer, tt.prompt, tt.minScore)

			if diff := cmp.Diff(eval.ErrInvalidEvaluators, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("NewJudge(%q) error mismatch (-want +got):\n%s", tt.id, diff)
			}
		})
	}
}

func TestJudgeEvaluate(t *testing.T) {
	const minScore = 0.7
	tests := []struct {
		name       string
		scorer     *fakeScorer
		prompt     eval.JudgePrompt[string]
		want       eval.Score
		wantPrompt string
		wantErr    error
	}{
		{name: "pass", scorer: &fakeScorer{resp: &eval.JudgeScore{Score: 0.8, Feedback: "good"}}, want: eval.Score{Value: 0.8, Pass: true, Feedback: "good"}, wantPrompt: "w|g"},
		{name: "atThreshold", scorer: &fakeScorer{resp: &eval.JudgeScore{Score: minScore}}, want: eval.Score{Value: minScore, Pass: true}, wantPrompt: "w|g"},
		{name: "fail", scorer: &fakeScorer{resp: &eval.JudgeScore{Score: 0.2}}, want: eval.Score{Value: 0.2}, wantPrompt: "w|g"},
		{name: "scoreAboveOne", scorer: &fakeScorer{resp: &eval.JudgeScore{Score: 1.5}}, wantPrompt: "w|g", wantErr: eval.ErrScoreRange},
		{name: "scoreBelowZero", scorer: &fakeScorer{resp: &eval.JudgeScore{Score: -0.5}}, wantPrompt: "w|g", wantErr: eval.ErrScoreRange},
		{name: "scoreNaN", scorer: &fakeScorer{resp: &eval.JudgeScore{Score: math.NaN()}}, wantPrompt: "w|g", wantErr: eval.ErrScoreRange},
		{name: "nilResponse", scorer: &fakeScorer{}, wantPrompt: "w|g", wantErr: eval.ErrScoreRange},
		{name: "scorerError", scorer: &fakeScorer{err: errScorer}, wantPrompt: "w|g", wantErr: errScorer},
		{name: "promptError", scorer: &fakeScorer{}, prompt: func(string, string) (string, error) { return "", errPrompt }, wantErr: errPrompt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			prompt := tt.prompt
			if prompt == nil {
				prompt = echoPrompt
			}
			j, err := eval.NewJudge("judge", tt.scorer, prompt, minScore)
			if err != nil {
				t.Fatalf("NewJudge() error = %v", err)
			}

			got, err := j.Evaluate(t.Context(), "w", "g")

			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("Evaluate() error mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Evaluate() mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantPrompt, tt.scorer.prompt); diff != "" {
				t.Errorf("Evaluate() prompt mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
