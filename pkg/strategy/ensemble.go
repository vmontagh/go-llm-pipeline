package strategy

import (
	"context"
	"errors"
	"strings"
	"sync"
)

type ensembleAnalyzer[R any] struct {
	generators []Generator[R]
	merger     func([]R, []error) (R, error)
	tracer     Tracer
}

// ErrEnsembleNoGenerators indicates NewEnsembleAnalyzer was called with no generators.
var ErrEnsembleNoGenerators = errors.New("at least one generator is required")

// ErrEnsembleNoMerger indicates NewEnsembleAnalyzer was called with a nil merger.
var ErrEnsembleNoMerger = errors.New("merger is required")

// EnsembleOption configures the ensembleAnalyzer.
type EnsembleOption[R any] func(*ensembleAnalyzer[R])

// WithEnsembleTracer sets the tracer for the ensembleAnalyzer.
func WithEnsembleTracer[R any](tracer Tracer) EnsembleOption[R] {
	return func(e *ensembleAnalyzer[R]) {
		if tracer != nil {
			e.tracer = tracer
		}
	}
}

// NewEnsembleAnalyzer creates a new ensembleAnalyzer with the given backend generators and merger.
func NewEnsembleAnalyzer[R any](
	generators []Generator[R],
	merger func([]R, []error) (R, error),
	options ...EnsembleOption[R],
) (Generator[R], error) {
	if len(generators) == 0 {
		return nil, ErrEnsembleNoGenerators
	}
	for _, g := range generators {
		if g == nil {
			return nil, ErrGeneratorRequired
		}
	}
	if merger == nil {
		return nil, ErrEnsembleNoMerger
	}
	e := &ensembleAnalyzer[R]{
		generators: make([]Generator[R], len(generators)),
		merger:     merger,
		tracer:     noOpTracer{},
	}
	copy(e.generators, generators)
	for _, opt := range options {
		opt(e)
	}
	return e, nil
}

func (e *ensembleAnalyzer[R]) Name() string {
	names := make([]string, len(e.generators))
	for i, g := range e.generators {
		names[i] = g.Name()
	}
	return "ensemble(" + strings.Join(names, ",") + ")"
}

func (e *ensembleAnalyzer[R]) Analyze(
	ctx context.Context,
	prompt string,
	imageData []byte,
	jobID string,
	publish func(eventType, itemID, message string, isError bool),
) (R, error) {
	var wg sync.WaitGroup
	results := make([]R, len(e.generators))
	errs := make([]error, len(e.generators))

	for i, g := range e.generators {
		wg.Add(1)
		go func(idx int, gen Generator[R]) {
			defer wg.Done()

			regionName := "Generator-" + gen.Name()
			defer e.tracer.StartRegion(ctx, regionName).End()

			res, err := gen.Analyze(ctx, prompt, imageData, jobID, publish)
			if err != nil {
				errs[idx] = err
				return
			}
			results[idx] = res
		}(i, g)
	}

	wg.Wait()
	return e.merger(results, errs)
}
