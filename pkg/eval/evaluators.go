package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

var (
	// ErrNotObject reports a value that does not encode to a JSON object.
	ErrNotObject = errors.New("value is not a JSON object")
	// ErrScoreRange reports a judge score outside [0, 1].
	ErrScoreRange = errors.New("judge score out of range")
)

// FieldMatch compares the top-level JSON fields of want and got. Value is
// the fraction of fields that match; the example passes only if all match.
type FieldMatch[Out any] struct{}

// Name returns "field_match".
func (FieldMatch[Out]) Name() string { return "field_match" }

// Evaluate implements Evaluator.
func (FieldMatch[Out]) Evaluate(_ context.Context, want, got Out) (Score, error) {
	w, err := fields(want)
	if err != nil {
		return Score{}, fmt.Errorf("want: %w", err)
	}
	g, err := fields(got)
	if err != nil {
		return Score{}, fmt.Errorf("got: %w", err)
	}
	s := Score{Value: 1, Pass: true, Fields: make(map[string]bool, len(w))}
	for k, v := range w {
		gv, ok := g[k]
		s.Fields[k] = ok && reflect.DeepEqual(v, gv)
	}
	for k := range g {
		if _, ok := w[k]; !ok {
			s.Fields[k] = false
		}
	}
	if len(s.Fields) == 0 {
		return s, nil
	}
	matched := 0
	for _, ok := range s.Fields {
		if ok {
			matched++
		}
	}
	s.Value = float64(matched) / float64(len(s.Fields))
	s.Pass = matched == len(s.Fields)
	return s, nil
}

func fields(v any) (map[string]any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encoding: %w", err)
	}
	// UseNumber keeps large integers exact instead of rounding to float64.
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, ErrNotObject
	}
	return m, nil
}

// JudgeScore is the structured verdict a judge model returns.
type JudgeScore struct {
	// Score is in [0, 1].
	Score    float64 `json:"score" yaml:"score"`
	Feedback string  `json:"feedback" yaml:"feedback"`
}

// Scorer is the judge model. An analyzer built with result type *JudgeScore
// satisfies it.
type Scorer interface {
	Analyze(ctx context.Context, prompt string, imageData []byte, jobID string, publish func(eventType, itemID, message string, isError bool)) (*JudgeScore, error)
}

// JudgePrompt builds the judge prompt from the expected and actual output.
type JudgePrompt[Out any] func(want, got Out) (string, error)

// Judge is an LLM-as-judge evaluator. Create one with NewJudge.
type Judge[Out any] struct {
	id       string
	scorer   Scorer
	prompt   JudgePrompt[Out]
	minScore float64
}

// NewJudge returns a judge named id. An example passes when its score is at
// least minScore. It returns ErrInvalidEvaluators for an empty id, a nil
// scorer or prompt, or a minScore that is NaN or outside [0, 1].
func NewJudge[Out any](id string, scorer Scorer, prompt JudgePrompt[Out], minScore float64) (*Judge[Out], error) {
	if id == "" || scorer == nil || prompt == nil || !inUnitRange(minScore) {
		return nil, fmt.Errorf("%w: judge %q needs a name, scorer, prompt, and minScore in [0, 1]", ErrInvalidEvaluators, id)
	}
	return &Judge[Out]{id: id, scorer: scorer, prompt: prompt, minScore: minScore}, nil
}

// Name returns the judge's id.
func (j *Judge[Out]) Name() string { return j.id }

// Evaluate implements Evaluator.
func (j *Judge[Out]) Evaluate(ctx context.Context, want, got Out) (Score, error) {
	prompt, err := j.prompt(want, got)
	if err != nil {
		return Score{}, fmt.Errorf("building prompt: %w", err)
	}
	resp, err := j.scorer.Analyze(ctx, prompt, nil, j.id, nil)
	if err != nil {
		return Score{}, fmt.Errorf("scoring: %w", err)
	}
	if resp == nil {
		return Score{}, fmt.Errorf("%w: no score returned", ErrScoreRange)
	}
	if !inUnitRange(resp.Score) {
		return Score{}, fmt.Errorf("%w: %v", ErrScoreRange, resp.Score)
	}
	return Score{Value: resp.Score, Pass: resp.Score >= j.minScore, Feedback: resp.Feedback}, nil
}

// inUnitRange reports whether v is in [0, 1]. NaN fails both comparisons.
func inUnitRange(v float64) bool { return v >= 0 && v <= 1 }
