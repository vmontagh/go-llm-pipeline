package cost

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
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

// Add returns the sum of u and v, or ErrOverflow if a count does not fit in
// an int.
func (u Usage) Add(v Usage) (Usage, error) {
	in, inOK := checkedAdd(u.Input, v.Input)
	out, outOK := checkedAdd(u.Output, v.Output)
	if !inOK || !outOK {
		return Usage{}, fmt.Errorf("%w: token count", ErrOverflow)
	}
	return Usage{Input: in, Output: out, Estimated: u.Estimated || v.Estimated}, nil
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
	n := len(text)
	return n/heuristicBytesPerToken + min(n%heuristicBytesPerToken, 1), nil
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
// is summed line by line. It returns ErrNoUsage when the body reports no
// usage, for example a non-LLM response or an unsupported server-sent-event
// stream, ErrInvalidUsage for negative counts, and ErrOverflow when a sum
// does not fit in an int.
func ParseUsage(body []byte) (string, Usage, error) {
	if json.Valid(body) {
		return parseUsageJSON(body)
	}
	var model string
	var total Usage
	found := false
	for line := range bytes.SplitSeq(body, []byte("\n")) {
		m, u, err := parseUsageJSON(line)
		if errors.Is(err, ErrNoUsage) {
			continue
		}
		if err != nil {
			return "", Usage{}, err
		}
		if total, err = total.Add(u); err != nil {
			return "", Usage{}, fmt.Errorf("summing stream: %w", err)
		}
		found = true
		model = cmp.Or(model, m)
	}
	if !found {
		return "", Usage{}, ErrNoUsage
	}
	return model, total, nil
}

func parseUsageJSON(doc []byte) (string, Usage, error) {
	var b usageBody
	if err := json.Unmarshal(doc, &b); err != nil {
		return "", Usage{}, fmt.Errorf("%w: %w", ErrNoUsage, err)
	}
	var in, out []int
	switch {
	case b.Usage != nil:
		in = []int{b.Usage.PromptTokens, b.Usage.InputTokens}
		out = []int{b.Usage.CompletionTokens, b.Usage.OutputTokens}
	case b.UsageMetadata != nil:
		m := b.UsageMetadata
		in = []int{m.PromptTokenCount}
		out = []int{m.CandidatesTokenCount, m.ThoughtsTokenCount}
	case b.PromptEvalCount != nil || b.EvalCount != nil:
		in = []int{derefOrZero(b.PromptEvalCount)}
		out = []int{derefOrZero(b.EvalCount)}
	default:
		return "", Usage{}, ErrNoUsage
	}
	inTotal, inErr := sumTokens(in)
	outTotal, outErr := sumTokens(out)
	if err := errors.Join(inErr, outErr); err != nil {
		return "", Usage{}, err
	}
	return cmp.Or(b.ModelVersion, b.Model), Usage{Input: inTotal, Output: outTotal}, nil
}

func derefOrZero(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// sumTokens adds reported token counts, rejecting negative counts and
// overflow.
func sumTokens(counts []int) (int, error) {
	total := 0
	for _, c := range counts {
		if c < 0 {
			return 0, fmt.Errorf("%w: negative token count %d", ErrInvalidUsage, c)
		}
		var ok bool
		if total, ok = checkedAdd(total, c); !ok {
			return 0, fmt.Errorf("%w: token count", ErrOverflow)
		}
	}
	return total, nil
}
