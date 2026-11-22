// Package eval scores pipeline output against labeled examples so prompt and
// model changes can be gated in CI. Replay provider calls from cassettes to
// keep runs offline, deterministic, and free.
package eval

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	// ErrInvalidDataset reports an empty dataset or a missing or duplicate
	// example name.
	ErrInvalidDataset = errors.New("invalid dataset")
	// ErrInvalidEvaluators reports a missing evaluator or a duplicate name.
	ErrInvalidEvaluators = errors.New("invalid evaluators")
	// ErrBelowThreshold reports a pass rate under the required minimum.
	ErrBelowThreshold = errors.New("pass rate below threshold")
)

// Example is one labeled input with its expected output.
type Example[In, Out any] struct {
	Name  string `json:"name" yaml:"name"`
	Input In     `json:"input" yaml:"input"`
	Want  Out    `json:"want" yaml:"want"`
}

// Score is one evaluator's verdict on one example.
type Score struct {
	Evaluator string `json:"evaluator"`
	// Value is in [0, 1].
	Value float64 `json:"value"`
	Pass  bool    `json:"pass"`
	// Fields maps a field name to whether it matched; nil when the
	// evaluator does not score fields.
	Fields   map[string]bool `json:"fields,omitempty"`
	Feedback string          `json:"feedback,omitempty"`
}

// Evaluator scores got against want.
type Evaluator[Out any] interface {
	Name() string
	Evaluate(ctx context.Context, want, got Out) (Score, error)
}

// Target produces the output under test for one input.
type Target[In, Out any] func(ctx context.Context, in In) (Out, error)

// LoadDataset reads a YAML list of examples from name in fsys, for example
// LoadDataset[In, Out](os.DirFS("testdata"), "dataset.yaml"). Unknown fields
// are rejected so typos in the dataset surface early.
func LoadDataset[In, Out any](fsys fs.FS, name string) ([]Example[In, Out], error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, fmt.Errorf("opening dataset: %w", err)
	}
	defer f.Close() // Error ignored: the file is only read.

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	var examples []Example[In, Out]
	if err := dec.Decode(&examples); err != nil {
		return nil, fmt.Errorf("decoding dataset %q: %w", name, err)
	}
	var extra any
	switch err := dec.Decode(&extra); {
	case err == nil:
		return nil, fmt.Errorf("dataset %q: %w: more than one YAML document", name, ErrInvalidDataset)
	case !errors.Is(err, io.EOF):
		return nil, fmt.Errorf("decoding dataset %q: %w", name, err)
	}
	if err := validateExamples(examples); err != nil {
		return nil, fmt.Errorf("dataset %q: %w", name, err)
	}
	return examples, nil
}

func validateExamples[In, Out any](examples []Example[In, Out]) error {
	if len(examples) == 0 {
		return fmt.Errorf("%w: no examples", ErrInvalidDataset)
	}
	return checkNames(ErrInvalidDataset, len(examples), func(i int) string { return examples[i].Name })
}

func validateEvaluators[Out any](evaluators []Evaluator[Out]) error {
	if len(evaluators) == 0 {
		return fmt.Errorf("%w: none given", ErrInvalidEvaluators)
	}
	if slices.Contains(evaluators, nil) {
		return fmt.Errorf("%w: nil evaluator", ErrInvalidEvaluators)
	}
	return checkNames(ErrInvalidEvaluators, len(evaluators), func(i int) string { return evaluators[i].Name() })
}

func checkNames(sentinel error, n int, name func(int) string) error {
	seen := make(map[string]bool, n)
	for i := range n {
		switch nm := name(i); {
		case strings.TrimSpace(nm) == "":
			return fmt.Errorf("%w: entry %d has no name", sentinel, i)
		case seen[nm]:
			return fmt.Errorf("%w: duplicate name %q", sentinel, nm)
		default:
			seen[nm] = true
		}
	}
	return nil
}

// ExampleResult is the outcome of one example. Err holds the target's error,
// in which case Scores is empty and Pass is false.
type ExampleResult struct {
	Name   string  `json:"name"`
	Pass   bool    `json:"pass"`
	Err    error   `json:"-"`
	Scores []Score `json:"scores,omitempty"`
}

// Summary aggregates one evaluator's scores across all examples. Examples
// whose target failed count as 0 and as not passed.
type Summary struct {
	Mean     float64 `json:"mean"`
	PassRate float64 `json:"pass_rate"`
	// FieldAccuracy maps a field name to the fraction of scored examples
	// in which it matched; nil when the evaluator does not score fields.
	FieldAccuracy map[string]float64 `json:"field_accuracy,omitempty"`
}

// Report is the result of Run. Examples are sorted by name, so a report
// compares cleanly across runs (for example with llmtest.Golden).
type Report struct {
	Examples   []ExampleResult    `json:"examples"`
	PassRate   float64            `json:"pass_rate"`
	Evaluators map[string]Summary `json:"evaluators"`
}

// Check returns an error wrapping ErrBelowThreshold when the pass rate is
// under minPassRate or minPassRate is NaN.
func (r *Report) Check(minPassRate float64) error {
	if math.IsNaN(minPassRate) || r.PassRate < minPassRate {
		return fmt.Errorf("%w: %.3f < %.3f", ErrBelowThreshold, r.PassRate, minPassRate)
	}
	return nil
}

// Run calls target for each example in turn and scores the output with every
// evaluator. A target error marks the example failed and the run continues;
// an evaluator error or a canceled ctx stops the run.
func Run[In, Out any](ctx context.Context, examples []Example[In, Out], target Target[In, Out], evaluators ...Evaluator[Out]) (*Report, error) {
	if err := validateExamples(examples); err != nil {
		return nil, fmt.Errorf("validating examples: %w", err)
	}
	if err := validateEvaluators(evaluators); err != nil {
		return nil, fmt.Errorf("validating evaluators: %w", err)
	}

	results := make([]ExampleResult, 0, len(examples))
	for _, ex := range examples {
		res, err := runExample(ctx, ex, target, evaluators)
		if err != nil {
			return nil, fmt.Errorf("example %q: %w", ex.Name, err)
		}
		results = append(results, res)
	}
	slices.SortFunc(results, func(a, b ExampleResult) int { return strings.Compare(a.Name, b.Name) })
	return newReport(results, evaluators), nil
}

func runExample[In, Out any](ctx context.Context, ex Example[In, Out], target Target[In, Out], evaluators []Evaluator[Out]) (ExampleResult, error) {
	res := ExampleResult{Name: ex.Name}
	if err := ctx.Err(); err != nil {
		return res, err
	}
	got, err := target(ctx, ex.Input)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return res, ctxErr
	}
	if err != nil {
		res.Err = err
		return res, nil //nolint:nilerr // A target error fails the example, not the run.
	}
	res.Pass = true
	for _, ev := range evaluators {
		s, err := ev.Evaluate(ctx, ex.Want, got)
		if err != nil {
			return res, fmt.Errorf("evaluator %q: %w", ev.Name(), err)
		}
		if err := ctx.Err(); err != nil {
			return res, err
		}
		s.Evaluator = ev.Name()
		res.Pass = res.Pass && s.Pass
		res.Scores = append(res.Scores, s)
	}
	return res, nil
}

func newReport[Out any](results []ExampleResult, evaluators []Evaluator[Out]) *Report {
	n := float64(len(results))
	r := &Report{Examples: results, Evaluators: make(map[string]Summary, len(evaluators))}
	for _, res := range results {
		if res.Pass {
			r.PassRate++
		}
	}
	r.PassRate /= n

	for i, ev := range evaluators {
		var sum Summary
		fieldHits := map[string]float64{}
		fieldSeen := map[string]float64{}
		for _, res := range results {
			if res.Err != nil {
				continue
			}
			s := res.Scores[i]
			sum.Mean += s.Value
			if s.Pass {
				sum.PassRate++
			}
			for field, ok := range s.Fields {
				fieldSeen[field]++
				if ok {
					fieldHits[field]++
				}
			}
		}
		sum.Mean /= n
		sum.PassRate /= n
		if len(fieldSeen) > 0 {
			sum.FieldAccuracy = make(map[string]float64, len(fieldSeen))
			for field, seen := range fieldSeen {
				sum.FieldAccuracy[field] = fieldHits[field] / seen
			}
		}
		r.Evaluators[ev.Name()] = sum
	}
	return r
}
