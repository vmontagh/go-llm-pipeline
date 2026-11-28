package llmfixtures_test

import (
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/vmontagh/go-llm-pipeline/internal/llmfixtures"
)

// TestFixturesWrapPayload checks that each canned provider reply is valid JSON
// in that provider's wire shape and carries the matching payload verbatim, and
// that every payload is itself valid JSON as the analyzers expect.
func TestFixturesWrapPayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		fixture func() string
		extract func(t *testing.T, body string) string
		want    string
	}{
		{name: "GeminiSuccess", fixture: llmfixtures.GeminiSuccess, extract: geminiText, want: llmfixtures.ValidResultObj},
		{name: "GeminiJudgeSuccess", fixture: llmfixtures.GeminiJudgeSuccess, extract: geminiText, want: llmfixtures.ValidJudgeResultObj},
		{name: "OpenAISuccess", fixture: llmfixtures.OpenAISuccess, extract: openAIText, want: llmfixtures.ValidResultObj},
		{name: "OpenAIJudgeSuccess", fixture: llmfixtures.OpenAIJudgeSuccess, extract: openAIText, want: llmfixtures.ValidJudgeResultObj},
		{name: "OllamaSuccess", fixture: llmfixtures.OllamaSuccess, extract: ollamaText, want: llmfixtures.ValidResultObj},
		{name: "OllamaJudgeSuccess", fixture: llmfixtures.OllamaJudgeSuccess, extract: ollamaText, want: llmfixtures.ValidJudgeResultObj},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.extract(t, tc.fixture())
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("%s() payload mismatch (-want +got):\n%s", tc.name, diff)
			}
			if !json.Valid([]byte(got)) {
				t.Errorf("%s() payload %q is not valid JSON", tc.name, got)
			}
		})
	}
}

func geminiText(t *testing.T, body string) string {
	t.Helper()
	var resp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	mustUnmarshal(t, body, &resp)
	if len(resp.Candidates) != 1 || len(resp.Candidates[0].Content.Parts) != 1 {
		t.Fatalf("Gemini body %q: want exactly one candidate with one part", body)
	}
	return resp.Candidates[0].Content.Parts[0].Text
}

func openAIText(t *testing.T, body string) string {
	t.Helper()
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	mustUnmarshal(t, body, &resp)
	if len(resp.Choices) != 1 {
		t.Fatalf("OpenAI body %q: want exactly one choice", body)
	}
	return resp.Choices[0].Message.Content
}

func ollamaText(t *testing.T, body string) string {
	t.Helper()
	var resp struct {
		Response string `json:"response"`
	}
	mustUnmarshal(t, body, &resp)
	return resp.Response
}

func mustUnmarshal(t *testing.T, body string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(body), v); err != nil {
		t.Fatalf("json.Unmarshal(%q) = %v, want nil", body, err)
	}
}
