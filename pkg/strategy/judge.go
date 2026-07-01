package strategy

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/vmontagh/go-llm-pipeline/pkg/policy"
	"google.golang.org/grpc/codes"
)

// OutOfBoundsBehavior defines how out-of-bounds draft indices are handled.
type OutOfBoundsBehavior string

const (
	OutOfBoundsBehaviorRetry    OutOfBoundsBehavior = "retry"
	OutOfBoundsBehaviorFail     OutOfBoundsBehavior = "fail"
	OutOfBoundsBehaviorFallback OutOfBoundsBehavior = "fallback"
)

// Sentinel errors for the judge strategy.
var (
	ErrJudgeRequired          = errors.New("judge analyzer is required")
	ErrJudgeNoGenerators      = errors.New("at least one generator is required for judge pattern")
	ErrJudgeOutOfBounds       = errors.New("judge returned out-of-bounds selected_draft_index")
	ErrJudgeAttemptsExhausted = errors.New("judge exhausted all attempts")
	ErrGeneratorsAllNil       = errors.New("all generator(s) returned nil drafts without an error, cannot judge")
	ErrPromptBuilderRequired  = errors.New("promptBuilder is required")
	ErrGeneratorsAllFailed    = errors.New("all generator(s) failed, cannot judge")
)

type judgeAnalyzer[R any] struct {
	judge               Judge
	generators          []Generator[R]
	promptBuilder       func(ctx context.Context, generatorPrompt string, drafts []R, feedback []string, attempt int) string
	logger              Logger
	tracer              Tracer
	multimodalJudge     bool
	maxAttempts         int
	outOfBoundsBehavior OutOfBoundsBehavior
}

// JudgeOption configures the judgeAnalyzer.
type JudgeOption[R any] func(*judgeAnalyzer[R])

// WithJudgeLogger sets the logger for the judgeAnalyzer.
func WithJudgeLogger[R any](l Logger) JudgeOption[R] {
	return func(j *judgeAnalyzer[R]) {
		j.logger = l
	}
}

// WithJudgeTracer sets the tracer for the judgeAnalyzer.
func WithJudgeTracer[R any](tracer Tracer) JudgeOption[R] {
	return func(j *judgeAnalyzer[R]) {
		j.tracer = tracer
	}
}

// WithJudgeOutOfBoundsBehavior sets the behavior for out-of-bounds draft indices.
func WithJudgeOutOfBoundsBehavior[R any](behavior OutOfBoundsBehavior) JudgeOption[R] {
	return func(j *judgeAnalyzer[R]) {
		j.outOfBoundsBehavior = behavior
	}
}

// WithJudgeMultimodal controls whether the judge receives the original
// image bytes.
func WithJudgeMultimodal[R any](multimodal bool) JudgeOption[R] {
	return func(j *judgeAnalyzer[R]) {
		j.multimodalJudge = multimodal
	}
}

// WithJudgeMaxAttempts sets the max attempts for the Reflexion loop.
func WithJudgeMaxAttempts[R any](maxAttempts int) JudgeOption[R] {
	return func(j *judgeAnalyzer[R]) {
		if maxAttempts > 0 {
			j.maxAttempts = maxAttempts
		}
	}
}

// NewJudgeAnalyzer creates a new judgeAnalyzer.
func NewJudgeAnalyzer[R any](
	judge Judge,
	generators []Generator[R],
	promptBuilder func(ctx context.Context, generatorPrompt string, drafts []R, feedback []string, attempt int) string,
	options ...JudgeOption[R],
) (Generator[R], error) {
	if judge == nil {
		return nil, ErrJudgeRequired
	}
	if len(generators) == 0 {
		return nil, ErrJudgeNoGenerators
	}
	if promptBuilder == nil {
		return nil, ErrPromptBuilderRequired
	}

	j := &judgeAnalyzer[R]{
		judge:               judge,
		generators:          make([]Generator[R], len(generators)),
		promptBuilder:       promptBuilder,
		logger:              noOpLogger{},
		tracer:              noOpTracer{},
		maxAttempts:         3,
		outOfBoundsBehavior: OutOfBoundsBehaviorFallback,
	}

	copy(j.generators, generators)

	for _, opt := range options {
		opt(j)
	}

	return j, nil
}

func (j *judgeAnalyzer[R]) Name() string {
	genNames := make([]string, len(j.generators))
	for i, g := range j.generators {
		genNames[i] = g.Name()
	}
	return fmt.Sprintf("judge(%s) <- generators(%s)", j.judge.Name(), strings.Join(genNames, ","))
}

func (j *judgeAnalyzer[R]) Analyze(
	ctx context.Context,
	prompt string,
	imageData []byte,
	jobID string,
	publish func(eventType, itemID, message string, isError bool),
) (R, error) {
	var feedbackHistory []string
	var zero R

	for attempt := 1; attempt <= j.maxAttempts; attempt++ {
		genPrompt := prompt
		if attempt > 1 && len(feedbackHistory) > 0 {
			var b strings.Builder
			b.WriteString(prompt)
			b.WriteString("\n\nADDITIONAL FEEDBACK FROM PREVIOUS ATTEMPTS:\n")
			for _, fb := range feedbackHistory {
				b.WriteString("- ")
				b.WriteString(fb)
				b.WriteString("\n")
			}
			genPrompt = b.String()
		}

		drafts, err := j.generateDrafts(ctx, genPrompt, imageData, jobID, publish)
		if err != nil {
			return zero, err
		}

		judgePrompt := j.promptBuilder(ctx, prompt, drafts, feedbackHistory, attempt)

		var judgeImageData []byte
		if j.multimodalJudge {
			judgeImageData = imageData
		}

		resp, err := j.judge.Analyze(ctx, judgePrompt, judgeImageData, jobID, publish)
		if err != nil {
			return zero, fmt.Errorf("judge failed to select a draft: %w", err)
		}

		idx := resp.SelectedDraftIndex
		if idx < 0 || idx >= len(drafts) {
			msg := fmt.Errorf("%w: %d (draft count: %d) on attempt %d/%d", ErrJudgeOutOfBounds, idx, len(drafts), attempt, j.maxAttempts)
			switch j.outOfBoundsBehavior {
			case OutOfBoundsBehaviorRetry:
				if attempt < j.maxAttempts {
					j.logger.Warnf("Judge returned out-of-bounds selected_draft_index %d (draft count: %d) on attempt %d/%d, retrying...", idx, len(drafts), attempt, j.maxAttempts)
					feedbackHistory = append(feedbackHistory, fmt.Sprintf("Judge selected out-of-bounds draft index %d.", idx))
					continue
				}
				return zero, policy.WithCode(codes.Unavailable, msg)
			case OutOfBoundsBehaviorFail:
				return zero, msg
			}

			// Fallback behavior
			j.logger.Warnf("Judge returned out-of-bounds selected_draft_index %d (draft count: %d). Falling back to index 0.", idx, len(drafts))
			if publish != nil {
				publish("warn", jobID, fmt.Sprintf("Judge returned out-of-bounds selected_draft_index %d. draft count: %d.", idx, len(drafts)), false)
			}
			idx = 0
		}

		if resp.Approved || attempt == j.maxAttempts {
			return drafts[idx], nil
		}

		if feedback := resp.Feedback; feedback != "" {
			feedbackHistory = append(feedbackHistory, feedback)
		}
		j.logger.Debugf("Judge rejected (attempt %d/%d): %s", attempt, j.maxAttempts, resp.Feedback)
	}

	return zero, fmt.Errorf("%w: %d", ErrJudgeAttemptsExhausted, j.maxAttempts)
}

func (j *judgeAnalyzer[R]) generateDrafts(
	ctx context.Context,
	prompt string,
	imageData []byte,
	jobID string,
	publish func(eventType, itemID, message string, isError bool),
) ([]R, error) {
	var wg sync.WaitGroup
	results := make([]R, len(j.generators))
	errs := make([]error, len(j.generators))
	hasResult := make([]bool, len(j.generators))

	j.logger.Debugf("Running %d generators for Judge pattern", len(j.generators))
	for i, g := range j.generators {
		wg.Add(1)
		go func(idx int, a Generator[R]) {
			defer wg.Done()

			regionName := "JudgeGen-" + a.Name()
			defer j.tracer.StartRegion(ctx, regionName).End()

			res, err := a.Analyze(ctx, prompt, imageData, jobID, publish)
			if err != nil {
				errs[idx] = err
				return
			}
			results[idx] = res
			hasResult[idx] = true
		}(i, g)
	}

	wg.Wait()

	var drafts []R
	for i, hasRes := range hasResult {
		if errs[i] != nil {
			j.logger.Warnf("Generator %s failed: %v", j.generators[i].Name(), errs[i])
			continue
		}
		if hasRes {
			if !isNil(results[i]) {
				drafts = append(drafts, results[i])
			}
		}
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	if len(drafts) == 0 {
		var failures []string
		for i, err := range errs {
			if err != nil {
				failures = append(failures, fmt.Sprintf("[%s: %v]", j.generators[i].Name(), err))
			}
		}
		// If there is only one generator, wrap its error directly so the tier propagates
		if len(j.generators) == 1 && errs[0] != nil {
			return nil, fmt.Errorf("all generator(s) failed, cannot judge: %w", errs[0])
		}
		if len(failures) == 0 {
			return nil, fmt.Errorf("%w: count=%d", ErrGeneratorsAllNil, len(j.generators))
		}
		return nil, fmt.Errorf("%w: count=%d details=%s", ErrGeneratorsAllFailed, len(j.generators), strings.Join(failures, " "))
	}

	return drafts, nil
}

func isNil(val any) bool {
	if val == nil {
		return true
	}
	v := reflect.ValueOf(val)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Map, reflect.Pointer, reflect.UnsafePointer, reflect.Interface, reflect.Slice:
		return v.IsNil()
	}
	return false
}
