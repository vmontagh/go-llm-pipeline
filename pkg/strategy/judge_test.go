package strategy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/vmontagh/go-llm-pipeline/pkg/policy"
	"google.golang.org/grpc/codes"
)

var (
	errJudgeTestFailed  = errors.New("failed")
	errJudgeTestFailed1 = errors.New("failed 1")
	errJudgeTestFailed2 = errors.New("failed 2")
	errJudgeTestJudge   = errors.New("judge failed")
	errJudgeTestPrompt  = errors.New("prompt failed")
)

type fakeJudgeAnalyzer struct {
	name   string
	result *JudgeResponse
	err    error
}

func (m *fakeJudgeAnalyzer) Name() string { return m.name }

func (m *fakeJudgeAnalyzer) Analyze(_ context.Context, _ string, _ []byte, _ string, _ func(string, string, string, bool)) (*JudgeResponse, error) {
	return m.result, m.err
}

type fakePointerAnalyzer struct {
	name   string
	result *dummyAnalysisResult
	err    error
	block  bool
}

func (m *fakePointerAnalyzer) Name() string {
	return m.name
}

func (m *fakePointerAnalyzer) Analyze(ctx context.Context, prompt string, imageData []byte, jobID string, publish func(eventType, itemID, message string, isError bool)) (*dummyAnalysisResult, error) {
	if m.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return m.result, m.err
}

func dummyPromptBuilder(_ context.Context, genPrompt string, drafts []*dummyAnalysisResult, feedback []string, attempt int) (string, error) {
	return "judge-prompt", nil
}

func TestJudgeAnalyzer_Init(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		judge         Judge
		generators    []Generator[*dummyAnalysisResult]
		promptBuilder JudgePromptBuilder[*dummyAnalysisResult]
		options       []JudgeOption[*dummyAnalysisResult]
		wantErr       error
		wantName      string
	}{
		{
			name:          "empty judge",
			judge:         nil,
			generators:    []Generator[*dummyAnalysisResult]{&fakePointerAnalyzer{name: "a"}},
			promptBuilder: dummyPromptBuilder,
			wantErr:       ErrJudgeRequired,
		},
		{
			name:          "empty generators",
			judge:         &fakeJudgeAnalyzer{name: "j"},
			generators:    nil,
			promptBuilder: dummyPromptBuilder,
			wantErr:       ErrJudgeNoGenerators,
		},
		{
			name:          "empty prompt builder",
			judge:         &fakeJudgeAnalyzer{name: "j"},
			generators:    []Generator[*dummyAnalysisResult]{&fakePointerAnalyzer{name: "a"}},
			promptBuilder: nil,
			wantErr:       ErrPromptBuilderRequired,
		},
		{
			name:          "nil generator in slice",
			judge:         &fakeJudgeAnalyzer{name: "j"},
			generators:    []Generator[*dummyAnalysisResult]{&fakePointerAnalyzer{name: "a"}, nil},
			promptBuilder: dummyPromptBuilder,
			wantErr:       ErrGeneratorRequired,
		},
		{
			name:          "nil logger and tracer options ignored",
			judge:         &fakeJudgeAnalyzer{name: "j"},
			generators:    []Generator[*dummyAnalysisResult]{&fakePointerAnalyzer{name: "a"}},
			promptBuilder: dummyPromptBuilder,
			options: []JudgeOption[*dummyAnalysisResult]{
				WithJudgeLogger[*dummyAnalysisResult](nil),
				WithJudgeTracer[*dummyAnalysisResult](nil),
			},
			wantErr: nil,
		},
		{
			name:          "success",
			judge:         &fakeJudgeAnalyzer{name: "j"},
			generators:    []Generator[*dummyAnalysisResult]{&fakePointerAnalyzer{name: "g1"}, &fakePointerAnalyzer{name: "g2"}},
			promptBuilder: dummyPromptBuilder,
			wantErr:       nil,
			wantName:      "judge(j) <- generators(g1,g2)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, err := NewJudgeAnalyzer[*dummyAnalysisResult](tt.judge, tt.generators, tt.promptBuilder, tt.options...)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want error %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			if tt.wantName != "" && a.Name() != tt.wantName {
				t.Errorf("a.Name() = %q, want %q", a.Name(), tt.wantName)
			}
		})
	}
}

func TestJudgeAnalyzer_Analyze(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		judge      Judge
		generators []Generator[*dummyAnalysisResult]
		want       *dummyAnalysisResult
		wantErr    error
		wantErrMsg string
	}{
		{
			name:  "all generators succeed",
			judge: &fakeJudgeAnalyzer{result: &JudgeResponse{Approved: true, SelectedDraftIndex: 0}},
			generators: []Generator[*dummyAnalysisResult]{
				&fakePointerAnalyzer{result: &dummyAnalysisResult{Heading: "Gen 1", Tags: []string{"a"}}},
				&fakePointerAnalyzer{result: &dummyAnalysisResult{Heading: "Gen 2", Tags: []string{"b"}}},
			},
			want:    &dummyAnalysisResult{Heading: "Gen 1", Tags: []string{"a"}},
			wantErr: nil,
		},
		{
			name:  "one generator fails, judge still runs",
			judge: &fakeJudgeAnalyzer{result: &JudgeResponse{Approved: true, SelectedDraftIndex: 0}},
			generators: []Generator[*dummyAnalysisResult]{
				&fakePointerAnalyzer{result: &dummyAnalysisResult{Heading: "Gen 1", Tags: []string{"a"}}},
				&fakePointerAnalyzer{err: errJudgeTestFailed},
			},
			want:    &dummyAnalysisResult{Heading: "Gen 1", Tags: []string{"a"}},
			wantErr: nil,
		},
		{
			name:  "all generators fail",
			judge: &fakeJudgeAnalyzer{result: &JudgeResponse{Approved: true, SelectedDraftIndex: 0}},
			generators: []Generator[*dummyAnalysisResult]{
				&fakePointerAnalyzer{err: errJudgeTestFailed1},
				&fakePointerAnalyzer{err: errJudgeTestFailed2},
			},
			want:    nil,
			wantErr: cmpopts.AnyError,
		},
		{
			name:  "judge fails",
			judge: &fakeJudgeAnalyzer{err: errJudgeTestJudge},
			generators: []Generator[*dummyAnalysisResult]{
				&fakePointerAnalyzer{result: &dummyAnalysisResult{Heading: "Gen 1", Tags: []string{"a"}}},
			},
			want:    nil,
			wantErr: cmpopts.AnyError,
		},
		{
			name:  "all generators return nil results without errors",
			judge: &fakeJudgeAnalyzer{result: &JudgeResponse{Approved: true, SelectedDraftIndex: 0}},
			generators: []Generator[*dummyAnalysisResult]{
				&fakePointerAnalyzer{},
				&fakePointerAnalyzer{},
			},
			want:       nil,
			wantErr:    cmpopts.AnyError,
			wantErrMsg: "returned nil drafts without an error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ja, err := NewJudgeAnalyzer(tt.judge, tt.generators, dummyPromptBuilder)
			if err != nil {
				t.Fatalf("setup failed: %v", err)
			}

			got, err := ja.Analyze(context.Background(), "prompt", nil, "job123", nil)

			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Analyze() error mismatch (-want +got):\n%s", diff)
			}
			if tt.wantErrMsg != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErrMsg)) {
				t.Errorf("Analyze() error = %v, want substring %q", err, tt.wantErrMsg)
			}
			if tt.wantErr != nil {
				return
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Analyze() result mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

type capturingJudge struct {
	name      string
	result    *JudgeResponse
	gotPrompt string
	gotBytes  []byte
}

func (c *capturingJudge) Name() string { return c.name }
func (c *capturingJudge) Analyze(_ context.Context, prompt string, imageData []byte, _ string, _ func(string, string, string, bool)) (*JudgeResponse, error) {
	c.gotPrompt = prompt
	c.gotBytes = imageData
	return c.result, nil
}

type capturingAnalyzer struct {
	name      string
	result    *dummyAnalysisResult
	gotPrompt string
}

func (c *capturingAnalyzer) Name() string { return c.name }
func (c *capturingAnalyzer) Analyze(_ context.Context, prompt string, _ []byte, _ string, _ func(string, string, string, bool)) (*dummyAnalysisResult, error) {
	c.gotPrompt = prompt
	return c.result, nil
}

func TestJudgeAnalyzer_ImageForwarding(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		multimodal      bool
		wantJudgeImgLen int
	}{
		{name: "default strips image from judge", multimodal: false, wantJudgeImgLen: 0},
		{name: "multimodal forwards image to judge", multimodal: true, wantJudgeImgLen: len("img")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			judge := &capturingJudge{name: "j", result: &JudgeResponse{Approved: true, SelectedDraftIndex: 0}}
			gen := &capturingAnalyzer{name: "g", result: &dummyAnalysisResult{Tags: []string{"a"}}}
			imgBytes := []byte("img")

			ja, err := NewJudgeAnalyzer(judge, []Generator[*dummyAnalysisResult]{gen}, dummyPromptBuilder, WithJudgeMultimodal[*dummyAnalysisResult](tt.multimodal))
			if err != nil {
				t.Fatalf("setup failed: %v", err)
			}
			if _, err := ja.Analyze(context.Background(), "prompt", imgBytes, "job123", nil); err != nil {
				t.Fatalf("Analyze() unexpected error: %v", err)
			}

			if got := len(judge.gotBytes); got != tt.wantJudgeImgLen {
				t.Errorf("judge ImageData len = %d, want %d", got, tt.wantJudgeImgLen)
			}
			if got := len(imgBytes); got != len("img") {
				t.Errorf("caller's job mutated: ImageData len = %d, want %d", got, len("img"))
			}
		})
	}
}

func TestJudgeAnalyzer_ContextCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())

	j := &fakeJudgeAnalyzer{name: "j"}
	g1 := &fakePointerAnalyzer{name: "g1", block: true}
	g2 := &fakePointerAnalyzer{name: "g2", block: true}

	ja, err := NewJudgeAnalyzer[*dummyAnalysisResult](j, []Generator[*dummyAnalysisResult]{g1, g2}, dummyPromptBuilder)
	if err != nil {
		t.Fatalf("NewJudgeAnalyzer() = %v", err)
	}

	cancel() // cancel immediately to avoid flaky tests

	_, err = ja.Analyze(ctx, "prompt", nil, "job123", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want error when context is canceled, got %v", err)
	}
}

type capturingGeneratorWithPrompts struct {
	name    string
	result  *dummyAnalysisResult
	prompts []string
}

func (c *capturingGeneratorWithPrompts) Name() string { return c.name }
func (c *capturingGeneratorWithPrompts) Analyze(_ context.Context, prompt string, _ []byte, _ string, _ func(string, string, string, bool)) (*dummyAnalysisResult, error) {
	c.prompts = append(c.prompts, prompt)
	return c.result, nil
}

type dynamicjudgeAnalyzer struct {
	name      string
	responses []*JudgeResponse
	call      int
}

func (d *dynamicjudgeAnalyzer) Name() string { return d.name }
func (d *dynamicjudgeAnalyzer) Analyze(_ context.Context, _ string, _ []byte, _ string, _ func(string, string, string, bool)) (*JudgeResponse, error) {
	if d.call >= len(d.responses) {
		return d.responses[len(d.responses)-1], nil
	}
	resp := d.responses[d.call]
	d.call++
	return resp, nil
}

func TestJudgeAnalyzer_ReflexionLoop(t *testing.T) {
	t.Parallel()
	const initialPrompt = "initial-caller-prompt"

	draft := &dummyAnalysisResult{Heading: "Draft", Tags: []string{"draft"}}

	tests := []struct {
		name                string
		judgeResponses      []*JudgeResponse
		want                *dummyAnalysisResult
		wantErr             error
		wantAttempt1Prompt  string // if set, attempt 1 must equal this
		wantAttempt2Rebuilt bool   // if true, attempt 2 must differ from attempt 1
	}{
		{
			name: "judge approves on first attempt — prompt forwarded as-is",
			judgeResponses: []*JudgeResponse{
				{Approved: true, SelectedDraftIndex: 0},
			},
			want:               draft,
			wantAttempt1Prompt: initialPrompt,
		},
		{
			name: "judge rejects then approves — feedback accumulated, prompt rebuilt",
			judgeResponses: []*JudgeResponse{
				{Approved: false, Feedback: "add more tags", SelectedDraftIndex: 0},
				{Approved: true, SelectedDraftIndex: 0},
			},
			want:                draft,
			wantAttempt1Prompt:  initialPrompt,
			wantAttempt2Rebuilt: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gen := &capturingGeneratorWithPrompts{
				name:   "gen",
				result: draft,
			}
			judge := &dynamicjudgeAnalyzer{name: "j", responses: tt.judgeResponses}

			ja, err := NewJudgeAnalyzer(judge, []Generator[*dummyAnalysisResult]{gen}, dummyPromptBuilder, WithJudgeMaxAttempts[*dummyAnalysisResult](3))
			if err != nil {
				t.Fatalf("setup: %v", err)
			}

			got, err := ja.Analyze(context.Background(), initialPrompt, nil, "job123", nil)

			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("error mismatch (-want +got):\n%s", diff)
			}
			if tt.wantErr != nil {
				return
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("result mismatch (-want +got):\n%s", diff)
			}
			if tt.wantAttempt1Prompt != "" {
				if len(gen.prompts) < 1 || gen.prompts[0] != tt.wantAttempt1Prompt {
					t.Errorf("attempt 1 prompt: want %q, got %v", tt.wantAttempt1Prompt, gen.prompts)
				}
			}
			if tt.wantAttempt2Rebuilt {
				if len(gen.prompts) < 2 || gen.prompts[1] == initialPrompt {
					t.Errorf("attempt 2: want rebuilt prompt, still got initial prompt %q", initialPrompt)
				}
			}
		})
	}
}

func TestJudgeAnalyzer_PromptBuilderError(t *testing.T) {
	t.Parallel()
	draft := &dummyAnalysisResult{Heading: "Draft"}
	reject := &JudgeResponse{Approved: false, Feedback: "more tags", SelectedDraftIndex: 0}

	tests := []struct {
		name           string
		failOnAttempt  int
		wantJudgeCalls int
	}{
		{name: "first attempt", failOnAttempt: 1},
		{name: "retry attempt after a rejection", failOnAttempt: 2, wantJudgeCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			judge := &dynamicjudgeAnalyzer{name: "j", responses: []*JudgeResponse{reject}}
			builder := func(_ context.Context, _ string, _ []*dummyAnalysisResult, _ []string, attempt int) (string, error) {
				if attempt == tt.failOnAttempt {
					return "", errJudgeTestPrompt
				}
				return "judge-prompt", nil
			}
			gen := &fakePointerAnalyzer{name: "gen", result: draft}
			ja, err := NewJudgeAnalyzer(judge, []Generator[*dummyAnalysisResult]{gen}, builder, WithJudgeMaxAttempts[*dummyAnalysisResult](3))
			if err != nil {
				t.Fatalf("NewJudgeAnalyzer() = %v", err)
			}

			got, err := ja.Analyze(t.Context(), "prompt", nil, "job123", nil)

			if diff := cmp.Diff(errJudgeTestPrompt, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Analyze() error mismatch (-want +got):\n%s", diff)
			}
			if got != nil {
				t.Errorf("Analyze() = %+v, want nil", got)
			}
			if judge.call != tt.wantJudgeCalls {
				t.Errorf("judge called %d times, want %d", judge.call, tt.wantJudgeCalls)
			}
		})
	}
}

func TestJudgeAnalyzer_ExhaustedRetries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		responses []*JudgeResponse
		want      *dummyAnalysisResult
		wantErr   error
	}{
		{
			name: "all retries rejected and final selected draft is returned",
			responses: []*JudgeResponse{
				{Approved: false, Feedback: "bad 1", SelectedDraftIndex: 0},
				{Approved: false, Feedback: "bad 2", SelectedDraftIndex: 0},
				{Approved: false, Feedback: "bad 3", SelectedDraftIndex: 0},
			},
			want:    &dummyAnalysisResult{Tags: []string{"a"}}, // draft from fake analyzer
			wantErr: nil,
		},
		{
			name: "all retries rejected but final selected draft is still returned",
			responses: []*JudgeResponse{
				{Approved: false, Feedback: "bad 1", SelectedDraftIndex: 0},
				{Approved: false, Feedback: "bad 2", SelectedDraftIndex: 0},
				{Approved: false, Feedback: "fine whatever", SelectedDraftIndex: 0},
			},
			want:    &dummyAnalysisResult{Tags: []string{"a"}}, // draft from fake analyzer
			wantErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			judge := &dynamicjudgeAnalyzer{name: "j", responses: tt.responses}
			gen := &fakePointerAnalyzer{name: "g", result: &dummyAnalysisResult{Tags: []string{"a"}}}

			ja, err := NewJudgeAnalyzer(judge, []Generator[*dummyAnalysisResult]{gen}, dummyPromptBuilder, WithJudgeMaxAttempts[*dummyAnalysisResult](3))
			if err != nil {
				t.Fatalf("setup: %v", err)
			}

			got, err := ja.Analyze(context.Background(), "prompt", nil, "job123", nil)

			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Analyze() error mismatch (-want +got):\n%s", diff)
			}
			if tt.wantErr != nil {
				return
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Analyze() result mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestJudgeAnalyzer_OutOfBoundsBehavior(t *testing.T) {
	t.Parallel()
	draft0 := &dummyAnalysisResult{Heading: "Gen 1", Tags: []string{"a"}}
	draft1 := &dummyAnalysisResult{Heading: "Gen 2", Tags: []string{"b"}}

	tests := []struct {
		name          string
		behavior      OutOfBoundsBehavior
		index         int
		maxAttempts   int
		generators    []Generator[*dummyAnalysisResult]
		want          *dummyAnalysisResult
		wantCode      codes.Code
		wantRetryable bool
		wantErrSubstr string
	}{
		{
			name:        "fallback uses first draft for index past end",
			behavior:    OutOfBoundsBehaviorFallback,
			index:       2,
			maxAttempts: 3,
			generators: []Generator[*dummyAnalysisResult]{
				&fakePointerAnalyzer{name: "g1", result: draft0},
				&fakePointerAnalyzer{name: "g2", result: draft1},
			},
			want: draft0,
		},
		{
			name:          "retry returns retry-classified error for negative index",
			behavior:      OutOfBoundsBehaviorRetry,
			index:         -1,
			maxAttempts:   3,
			generators:    []Generator[*dummyAnalysisResult]{&fakePointerAnalyzer{name: "g1", result: draft0}},
			wantCode:      codes.Unavailable,
			wantRetryable: true,
			wantErrSubstr: "attempt 3/3",
		},
		{
			name:          "fail returns terminal error for index past end",
			behavior:      OutOfBoundsBehaviorFail,
			index:         1,
			maxAttempts:   2,
			generators:    []Generator[*dummyAnalysisResult]{&fakePointerAnalyzer{name: "g1", result: draft0}},
			wantCode:      codes.Internal,
			wantRetryable: false,
			wantErrSubstr: "attempt 1/2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			judge := &fakeJudgeAnalyzer{
				name:   "j",
				result: &JudgeResponse{Approved: true, SelectedDraftIndex: tt.index},
			}
			ja, err := NewJudgeAnalyzer(judge, tt.generators, dummyPromptBuilder,
				WithJudgeMaxAttempts[*dummyAnalysisResult](tt.maxAttempts),
				WithJudgeOutOfBoundsBehavior[*dummyAnalysisResult](tt.behavior),
			)
			if err != nil {
				t.Fatalf("setup failed: %v", err)
			}

			got, err := ja.Analyze(context.Background(), "prompt", nil, "job123", nil)

			if tt.wantErrSubstr == "" {
				if err != nil {
					t.Fatalf("Analyze() unexpected error: %v", err)
				}
				if diff := cmp.Diff(tt.want, got); diff != "" {
					t.Errorf("Analyze() result mismatch (-want +got):\n%s", diff)
				}
				return
			}
			if err == nil {
				t.Fatal("Analyze() error = nil, want non-nil")
			}
			if !strings.Contains(err.Error(), tt.wantErrSubstr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantErrSubstr)
			}
			if gotCode := policy.Code(err); gotCode != tt.wantCode {
				t.Errorf("policy.Code(err) = %v, want %v", gotCode, tt.wantCode)
			}
			if gotRetryable := policy.IsRetryable(err); gotRetryable != tt.wantRetryable {
				t.Errorf("policy.IsRetryable(err) = %v, want %v", gotRetryable, tt.wantRetryable)
			}
		})
	}
}

func TestIsNil(t *testing.T) {
	t.Parallel()
	var nilPtr *dummyAnalysisResult
	var nilMap map[string]int
	var nilSlice []int
	var nilChan chan int
	var nilFunc func()

	tests := []struct {
		name string
		val  any
		want bool
	}{
		{"untyped nil", nil, true},
		{"nil pointer", nilPtr, true},
		{"nil map", nilMap, true},
		{"nil slice", nilSlice, true},
		{"nil chan", nilChan, true},
		{"nil func", nilFunc, true},
		{"non-nil pointer", &dummyAnalysisResult{}, false},
		{"non-nil value", dummyAnalysisResult{}, false},
		{"int value", 0, false},
		{"string value", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isNil(tt.val); got != tt.want {
				t.Errorf("isNil(%#v) = %t, want %t", tt.val, got, tt.want)
			}
		})
	}
}
