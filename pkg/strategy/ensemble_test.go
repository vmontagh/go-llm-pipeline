package strategy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

var (
	errPrimaryFailed   = errors.New("primary failed")
	errSecondaryFailed = errors.New("secondary failed")
)

type dummyAnalysisResult struct {
	Heading     string
	Description string
	Tags        []string
}

type fakeAnalyzer struct {
	name   string
	result dummyAnalysisResult
	err    error
	delay  time.Duration
}

func (m *fakeAnalyzer) Name() string {
	return m.name
}

func (m *fakeAnalyzer) Analyze(ctx context.Context, prompt string, imageData []byte, jobID string, publish func(eventType, itemID, message string, isError bool)) (dummyAnalysisResult, error) {
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return dummyAnalysisResult{}, ctx.Err()
		}
	}
	return m.result, m.err
}

func testMerger(results []dummyAnalysisResult, errs []error) (dummyAnalysisResult, error) {
	if errs[0] != nil {
		return dummyAnalysisResult{}, fmt.Errorf("primary analyzer failed: %w", errs[0])
	}

	finalResult := dummyAnalysisResult{
		Heading:     results[0].Heading,
		Description: results[0].Description,
	}

	seenTags := make(map[string]bool)
	for i, res := range results {
		if errs[i] != nil {
			continue
		}

		for _, tag := range res.Tags {
			t := strings.ToLower(strings.TrimSpace(tag))
			if t == "" || seenTags[t] {
				continue
			}
			seenTags[t] = true
			finalResult.Tags = append(finalResult.Tags, t)
		}
	}

	return finalResult, nil
}

func TestEnsembleAnalyzer_Init(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		generators []Generator[dummyAnalysisResult]
		merger     func([]dummyAnalysisResult, []error) (dummyAnalysisResult, error)
		options    []EnsembleOption[dummyAnalysisResult]
		wantErr    error
		wantName   string
	}{
		{
			name:       "empty",
			generators: nil,
			merger:     testMerger,
			wantErr:    ErrEnsembleNoGenerators,
		},
		{
			name:       "nil_merger",
			generators: []Generator[dummyAnalysisResult]{&fakeAnalyzer{name: "a"}},
			merger:     nil,
			wantErr:    ErrEnsembleNoMerger,
		},
		{
			name:       "nil generator in slice",
			generators: []Generator[dummyAnalysisResult]{&fakeAnalyzer{name: "a"}, nil},
			merger:     testMerger,
			wantErr:    ErrGeneratorRequired,
		},
		{
			name:       "nil tracer option ignored",
			generators: []Generator[dummyAnalysisResult]{&fakeAnalyzer{name: "a"}},
			merger:     testMerger,
			options: []EnsembleOption[dummyAnalysisResult]{
				WithEnsembleTracer[dummyAnalysisResult](nil),
			},
			wantErr: nil,
		},
		{
			name:       "success",
			generators: []Generator[dummyAnalysisResult]{&fakeAnalyzer{name: "a"}},
			merger:     testMerger,
			wantErr:    nil,
			wantName:   "ensemble(a)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, err := NewEnsembleAnalyzer[dummyAnalysisResult](tt.generators, tt.merger, tt.options...)
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

func TestEnsembleAnalyzer_Analyze(t *testing.T) {
	t.Parallel()
	primaryErr := errPrimaryFailed

	tests := []struct {
		name      string
		analyzers []Generator[dummyAnalysisResult]
		want      dummyAnalysisResult
		wantErr   error
	}{
		{
			name: "primary error fails all",
			analyzers: []Generator[dummyAnalysisResult]{
				&fakeAnalyzer{err: primaryErr},
				&fakeAnalyzer{result: dummyAnalysisResult{Heading: "Ignore", Tags: []string{"a"}}},
			},
			want:    dummyAnalysisResult{},
			wantErr: cmpopts.AnyError,
		},
		{
			name: "secondary error ignored",
			analyzers: []Generator[dummyAnalysisResult]{
				&fakeAnalyzer{result: dummyAnalysisResult{Heading: "Primary", Tags: []string{"primary-tag"}}},
				&fakeAnalyzer{err: errSecondaryFailed},
			},
			want: dummyAnalysisResult{
				Heading: "Primary",
				Tags:    []string{"primary-tag"},
			},
			wantErr: nil,
		},
		{
			name: "merges and deduplicates tags",
			analyzers: []Generator[dummyAnalysisResult]{
				&fakeAnalyzer{result: dummyAnalysisResult{Heading: "H1", Description: "D1", Tags: []string{"A", "b", "C"}}},
				&fakeAnalyzer{result: dummyAnalysisResult{Heading: "H2", Description: "D2", Tags: []string{"b", "d", " E "}}},
				&fakeAnalyzer{result: dummyAnalysisResult{Heading: "H3", Description: "D3", Tags: []string{"a", "f", ""}}},
			},
			want: dummyAnalysisResult{
				Heading:     "H1",
				Description: "D1",
				Tags:        []string{"a", "b", "c", "d", "e", "f"},
			},
			wantErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ens, err := NewEnsembleAnalyzer(tt.analyzers, testMerger)
			if err != nil {
				t.Fatalf("setup failed: %v", err)
			}

			got, err := ens.Analyze(context.Background(), "prompt", nil, "job123", nil)

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

func TestEnsembleAnalyzer_ContextCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())

	a1 := &fakeAnalyzer{name: "a1", delay: 50 * time.Millisecond}
	a2 := &fakeAnalyzer{name: "a2", delay: 50 * time.Millisecond}

	ens, err := NewEnsembleAnalyzer([]Generator[dummyAnalysisResult]{a1, a2}, testMerger)
	if err != nil {
		t.Fatalf("NewEnsembleAnalyzer() = %v", err)
	}

	cancel() // cancel immediately to avoid flaky tests

	_, err = ens.Analyze(ctx, "prompt", nil, "job123", nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Analyze() err = %v, want %v", err, context.Canceled)
	}
}
