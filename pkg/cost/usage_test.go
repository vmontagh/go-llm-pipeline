package cost_test

import (
	"errors"
	"math"
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/pkg/cost"
)

// maxInt is the largest token count, which depends on the int size.
var maxInt = strconv.Itoa(math.MaxInt)

func TestParseUsage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		body      string
		wantModel string
		want      cost.Usage
		wantErr   error
	}{
		{
			name:      "openai chat",
			body:      `{"model":"gpt-4o-mini","usage":{"prompt_tokens":12,"completion_tokens":5}}`,
			wantModel: "gpt-4o-mini", want: cost.Usage{Input: 12, Output: 5},
		},
		{
			name:      "openai responses",
			body:      `{"model":"gpt-4o","usage":{"input_tokens":7,"output_tokens":3}}`,
			wantModel: "gpt-4o", want: cost.Usage{Input: 7, Output: 3},
		},
		{
			name:      "gemini with thoughts",
			body:      "{\n \"modelVersion\": \"gemini-2.5-flash\",\n \"usageMetadata\": {\"promptTokenCount\": 9, \"candidatesTokenCount\": 4, \"thoughtsTokenCount\": 6}\n}",
			wantModel: "gemini-2.5-flash", want: cost.Usage{Input: 9, Output: 10},
		},
		{
			name:      "ollama",
			body:      `{"model":"llama3.2","prompt_eval_count":20,"eval_count":8}`,
			wantModel: "llama3.2", want: cost.Usage{Input: 20, Output: 8},
		},
		{
			name:      "ollama input only",
			body:      `{"model":"nomic-embed-text","prompt_eval_count":4}`,
			wantModel: "nomic-embed-text", want: cost.Usage{Input: 4},
		},
		{
			name:      "ollama stream",
			body:      "{\"model\":\"llama3.2\",\"response\":\"a\"}\n{\"model\":\"llama3.2\",\"done\":true,\"prompt_eval_count\":20,\"eval_count\":8}\n",
			wantModel: "llama3.2", want: cost.Usage{Input: 20, Output: 8},
		},
		{
			name:      "stream summed",
			body:      "{\"model\":\"m\",\"prompt_eval_count\":1,\"eval_count\":2}\n{\"prompt_eval_count\":3,\"eval_count\":4}",
			wantModel: "m", want: cost.Usage{Input: 4, Output: 6},
		},
		{name: "no usage", body: `{"model":"m","response":"hi"}`, wantErr: cost.ErrNoUsage},
		{name: "stream without usage", body: "{\"response\":\"a\"}\n{\"response\":\"b\"}", wantErr: cost.ErrNoUsage},
		{name: "server-sent events", body: "data: {\"usage\":{\"prompt_tokens\":1}}\n\n", wantErr: cost.ErrNoUsage},
		{name: "not json", body: "not json", wantErr: cost.ErrNoUsage},
		{name: "empty", wantErr: cost.ErrNoUsage},
		{name: "negative", body: `{"model":"m","usage":{"prompt_tokens":-1}}`, wantErr: cost.ErrInvalidUsage},
		{name: "negative output", body: `{"model":"m","eval_count":-1}`, wantErr: cost.ErrInvalidUsage},
		{name: "negative in stream", body: "{\"prompt_eval_count\":1}\n{\"prompt_eval_count\":-1}", wantErr: cost.ErrInvalidUsage},
		{name: "fields overflow", body: `{"usage":{"prompt_tokens":` + maxInt + `,"input_tokens":1}}`, wantErr: cost.ErrOverflow},
		{name: "output fields overflow", body: `{"usageMetadata":{"candidatesTokenCount":` + maxInt + `,"thoughtsTokenCount":1}}`, wantErr: cost.ErrOverflow},
		{name: "oversized count", body: `{"model":"m","usage":{"prompt_tokens":9223372036854775808}}`, wantErr: cost.ErrInvalidUsage},
		{name: "fractional count", body: `{"model":"m","eval_count":1.5}`, wantErr: cost.ErrInvalidUsage},
		{name: "string count", body: `{"usageMetadata":{"promptTokenCount":"7"}}`, wantErr: cost.ErrInvalidUsage},
		{name: "usage not an object", body: `{"usage":3}`, wantErr: cost.ErrInvalidUsage},
		{name: "oversized count in stream", body: "{\"prompt_eval_count\":1}\n{\"prompt_eval_count\":1e30}", wantErr: cost.ErrInvalidUsage},
		{name: "null openai count", body: `{"model":"m","usage":{"prompt_tokens":null,"completion_tokens":3}}`, wantErr: cost.ErrInvalidUsage},
		{name: "null gemini count", body: `{"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":null}}`, wantErr: cost.ErrInvalidUsage},
		{name: "null ollama count", body: `{"prompt_eval_count":4,"eval_count":null}`, wantErr: cost.ErrInvalidUsage},
		{name: "null non-count key inside usage", body: `{"model":"m","usage":{"prompt_tokens":2,"completion_tokens":1,"eval_count":null}}`, wantModel: "m", want: cost.Usage{Input: 2, Output: 1}},
		{name: "truncated stream line", body: "{\"prompt_eval_count\":3}\n{\"eval_count\":5", wantErr: cost.ErrInvalidUsage},
		{name: "non-json body", body: "<html>502 Bad Gateway</html>\n", wantErr: cost.ErrNoUsage},
		{name: "stream with blank lines", body: "{\"prompt_eval_count\":3}\n\n  \n{\"eval_count\":5}\n", want: cost.Usage{Input: 3, Output: 5}},
		{name: "top-level array", body: `[1,2]`, wantErr: cost.ErrNoUsage},
		{name: "null usage object", body: `{"model":"m","usage":null}`, wantErr: cost.ErrNoUsage},
		{name: "non-usage type mismatch", body: `{"model":5}`, wantErr: cost.ErrNoUsage},
		{name: "stream overflow", body: `{"prompt_eval_count":` + maxInt + "}\n" + `{"prompt_eval_count":1}`, wantErr: cost.ErrOverflow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			model, got, err := cost.ParseUsage([]byte(tt.body))
			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("ParseUsage(%q) error (-want +got):\n%s", tt.body, diff)
			}
			if model != tt.wantModel {
				t.Errorf("ParseUsage(%q) model = %q, want %q", tt.body, model, tt.wantModel)
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
		{text: "abcdefgh", want: 2},
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
	tests := []struct {
		name    string
		u, v    cost.Usage
		want    cost.Usage
		wantErr error
	}{
		{name: "sums", u: cost.Usage{Input: 1, Output: 2}, v: cost.Usage{Input: 3, Output: 4, Estimated: true}, want: cost.Usage{Input: 4, Output: 6, Estimated: true}},
		{name: "input overflow", u: cost.Usage{Input: math.MaxInt}, v: cost.Usage{Input: 1}, wantErr: cost.ErrOverflow},
		{name: "output overflow", u: cost.Usage{Output: math.MaxInt}, v: cost.Usage{Output: 1}, wantErr: cost.ErrOverflow},
		{name: "negative overflow", u: cost.Usage{Input: math.MinInt}, v: cost.Usage{Input: -1}, wantErr: cost.ErrOverflow},
	}
	for _, tt := range tests {
		got, err := tt.u.Add(tt.v)
		if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
			t.Errorf("%s: Add() error (-want +got):\n%s", tt.name, diff)
		}
		if diff := cmp.Diff(tt.want, got); diff != "" {
			t.Errorf("%s: Add() (-want +got):\n%s", tt.name, diff)
		}
	}
}
