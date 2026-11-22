package cost

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
)

// Usage is the token usage of one or more calls.
type Usage struct {
	Input  int `json:"input"`
	Output int `json:"output"`
	// Estimated is true when any part was counted locally by a Counter
	// rather than reported by the provider.
	Estimated bool `json:"estimated,omitempty"`
}

// Add returns the sum of u and v.
func (u Usage) Add(v Usage) Usage {
	return Usage{Input: u.Input + v.Input, Output: u.Output + v.Output, Estimated: u.Estimated || v.Estimated}
}

// Counter counts the tokens text uses on model. Implementations include
// Heuristic and, in a separate module to keep this one dependency-free,
// github.com/vmontagh/go-llm-pipeline/contrib/tiktoken.
type Counter interface {
	Count(model, text string) (int, error)
}

// heuristicBytesPerToken is the usual English average for BPE tokenizers.
const heuristicBytesPerToken = 4

// Heuristic is a dependency-free Counter that estimates one token per four
// bytes of text, rounded up. It is a rough fallback for budgeting, not a
// billing-grade count.
type Heuristic struct{}

// Count implements Counter.
func (Heuristic) Count(_, text string) (int, error) {
	return (len(text) + heuristicBytesPerToken - 1) / heuristicBytesPerToken, nil
}

// Estimate counts input and output with c and marks the result Estimated.
func Estimate(c Counter, model, input, output string) (Usage, error) {
	in, err := c.Count(model, input)
	if err != nil {
		return Usage{}, fmt.Errorf("counting input: %w", err)
	}
	out, err := c.Count(model, output)
	if err != nil {
		return Usage{}, fmt.Errorf("counting output: %w", err)
	}
	return Usage{Input: in, Output: out, Estimated: true}, nil
}

// usageBody is the union of the usage fields of supported providers.
type usageBody struct {
	Model        string `json:"model"`
	ModelVersion string `json:"modelVersion"`
	// OpenAI Chat Completions and embeddings use prompt/completion tokens;
	// the Responses API uses input/output tokens.
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		InputTokens      int `json:"input_tokens"`
		OutputTokens     int `json:"output_tokens"`
	} `json:"usage"`
	// Gemini bills thinking tokens as output.
	UsageMetadata *struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		ThoughtsTokenCount   int `json:"thoughtsTokenCount"`
	} `json:"usageMetadata"`
	// Ollama generate, chat, and embed.
	PromptEvalCount *int `json:"prompt_eval_count"`
	EvalCount       *int `json:"eval_count"`
}

// ParseUsage extracts the model and provider-reported usage from an OpenAI,
// Gemini, or Ollama response body. Newline-delimited JSON, as Ollama streams,
// is summed line by line. It returns false when the body reports no usage,
// for example a non-LLM response or an unsupported server-sent-event stream.
func ParseUsage(body []byte) (string, Usage, bool) {
	if json.Valid(body) {
		return parseUsageJSON(body)
	}
	var model string
	var total Usage
	found := false
	for line := range bytes.SplitSeq(body, []byte("\n")) {
		m, u, ok := parseUsageJSON(line)
		if !ok {
			continue
		}
		found = true
		model = cmp.Or(model, m)
		total = total.Add(u)
	}
	return model, total, found
}

func parseUsageJSON(doc []byte) (string, Usage, bool) {
	var b usageBody
	if err := json.Unmarshal(doc, &b); err != nil {
		return "", Usage{}, false
	}
	model := cmp.Or(b.ModelVersion, b.Model)
	switch {
	case b.Usage != nil:
		return model, Usage{
			Input:  b.Usage.PromptTokens + b.Usage.InputTokens,
			Output: b.Usage.CompletionTokens + b.Usage.OutputTokens,
		}, true
	case b.UsageMetadata != nil:
		m := b.UsageMetadata
		return model, Usage{Input: m.PromptTokenCount, Output: m.CandidatesTokenCount + m.ThoughtsTokenCount}, true
	case b.PromptEvalCount != nil || b.EvalCount != nil:
		var u Usage
		if b.PromptEvalCount != nil {
			u.Input = *b.PromptEvalCount
		}
		if b.EvalCount != nil {
			u.Output = *b.EvalCount
		}
		return model, u, true
	}
	return "", Usage{}, false
}
