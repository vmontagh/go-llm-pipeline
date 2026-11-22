package cost_test

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/pkg/cost"
)

func TestParseUsage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		body      string
		wantModel string
		want      cost.Usage
		wantOK    bool
	}{
		{
			name:      "openai chat",
			body:      `{"model":"gpt-4o-mini","usage":{"prompt_tokens":12,"completion_tokens":5}}`,
			wantModel: "gpt-4o-mini", want: cost.Usage{Input: 12, Output: 5}, wantOK: true,
		},
		{
			name:      "openai responses",
			body:      `{"model":"gpt-4o","usage":{"input_tokens":7,"output_tokens":3}}`,
			wantModel: "gpt-4o", want: cost.Usage{Input: 7, Output: 3}, wantOK: true,
		},
		{
			name:      "gemini with thoughts",
			body:      "{\n \"modelVersion\": \"gemini-2.5-flash\",\n \"usageMetadata\": {\"promptTokenCount\": 9, \"candidatesTokenCount\": 4, \"thoughtsTokenCount\": 6}\n}",
			wantModel: "gemini-2.5-flash", want: cost.Usage{Input: 9, Output: 10}, wantOK: true,
		},
		{
			name:      "ollama",
			body:      `{"model":"llama3.2","prompt_eval_count":20,"eval_count":8}`,
			wantModel: "llama3.2", want: cost.Usage{Input: 20, Output: 8}, wantOK: true,
		},
		{
			name:      "ollama input only",
			body:      `{"model":"nomic-embed-text","prompt_eval_count":4}`,
			wantModel: "nomic-embed-text", want: cost.Usage{Input: 4}, wantOK: true,
		},
		{
			name:      "ollama stream",
			body:      "{\"model\":\"llama3.2\",\"response\":\"a\"}\n{\"model\":\"llama3.2\",\"done\":true,\"prompt_eval_count\":20,\"eval_count\":8}\n",
			wantModel: "llama3.2", want: cost.Usage{Input: 20, Output: 8}, wantOK: true,
		},
		{
			name:      "stream summed",
			body:      "{\"model\":\"m\",\"prompt_eval_count\":1,\"eval_count\":2}\n{\"prompt_eval_count\":3,\"eval_count\":4}",
			wantModel: "m", want: cost.Usage{Input: 4, Output: 6}, wantOK: true,
		},
		{name: "no usage", body: `{"model":"m","response":"hi"}`},
		{name: "stream without usage", body: "{\"response\":\"a\"}\n{\"response\":\"b\"}"},
		{name: "server-sent events", body: "data: {\"usage\":{\"prompt_tokens\":1}}\n\n"},
		{name: "not json", body: "not json"},
		{name: "empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			model, got, ok := cost.ParseUsage([]byte(tt.body))
			if ok != tt.wantOK || model != tt.wantModel {
				t.Errorf("ParseUsage(%q) model, ok = %q, %t, want %q, %t", tt.body, model, ok, tt.wantModel, tt.wantOK)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("ParseUsage(%q) usage (-want +got):\n%s", tt.body, diff)
			}
		})
	}
}

func TestHeuristicCount(t *testing.T) {
	t.Parallel()
	tests := []struct {
		text string
		want int
	}{
		{text: "", want: 0},
		{text: "a", want: 1},
		{text: "abcd", want: 1},
		{text: "abcde", want: 2},
		{text: "héllo", want: 2},
	}
	for _, tt := range tests {
		got, err := cost.Heuristic{}.Count("any", tt.text)
		if err != nil || got != tt.want {
			t.Errorf("Heuristic.Count(%q) = %d, %v, want %d, nil", tt.text, got, err, tt.want)
		}
	}
}

// errCount is the error failingCounter returns.
var errCount = errors.New("count failed")

// failingCounter fails for one text and counts bytes otherwise.
type failingCounter struct{ fail string }

func (c failingCounter) Count(_, text string) (int, error) {
	if text == c.fail {
		return 0, errCount
	}
	return len(text), nil
}

func TestEstimate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		fail    string
		want    cost.Usage
		wantErr error
	}{
		{name: "counts both", fail: "-", want: cost.Usage{Input: 2, Output: 3, Estimated: true}},
		{name: "input fails", fail: "in", wantErr: errCount},
		{name: "output fails", fail: "out", wantErr: errCount},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := cost.Estimate(failingCounter{fail: tt.fail}, "m", "in", "out")
			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("Estimate() error (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Estimate() (-want +got):\n%s", diff)
			}
		})
	}
}

func TestUsageAdd(t *testing.T) {
	t.Parallel()
	got := cost.Usage{Input: 1, Output: 2}.Add(cost.Usage{Input: 3, Output: 4, Estimated: true})
	want := cost.Usage{Input: 4, Output: 6, Estimated: true}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Usage.Add() (-want +got):\n%s", diff)
	}
}
