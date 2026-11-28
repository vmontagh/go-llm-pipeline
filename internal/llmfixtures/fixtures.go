// Package llmfixtures holds canned photo-tagging and judge replies for tests.
package llmfixtures

import "github.com/vmontagh/go-llm-pipeline/pkg/llmvcr"

// ValidResultObj is a mock JSON string containing a valid tags response object.
const ValidResultObj = `{"heading": "Cute Indoor Cat", "tags": ["animal", "cat", "indoor", "cute", "feline", "pet", "whiskers", "paws", "meow", "purr", "furry", "kitty", "kitten", "domestic", "mammal", "portrait", "eyes", "ears", "tail", "snout", "beautiful", "adorable", "looking", "fur"]}`

// ValidJudgeResultObj is a mock JSON string containing a valid judge response object.
const ValidJudgeResultObj = `{"approved": true, "selected_draft_index": 0, "feedback": "Looks good"}`

// GeminiSuccess returns a mock JSON response for a successful Gemini generateContent call.
func GeminiSuccess() string {
	return llmvcr.GeminiResponse(ValidResultObj)
}

// GeminiJudgeSuccess returns a mock JSON response for a successful Gemini judge call.
func GeminiJudgeSuccess() string {
	return llmvcr.GeminiResponse(ValidJudgeResultObj)
}

// OpenAISuccess returns a mock JSON response for a successful OpenAI completions call.
func OpenAISuccess() string {
	return llmvcr.OpenAIResponse(ValidResultObj)
}

// OpenAIJudgeSuccess returns a mock JSON response for a successful OpenAI judge call.
func OpenAIJudgeSuccess() string {
	return llmvcr.OpenAIResponse(ValidJudgeResultObj)
}

// OllamaSuccess returns a mock JSON response for a successful Ollama generate call.
func OllamaSuccess() string {
	return llmvcr.OllamaResponse(ValidResultObj)
}

// OllamaJudgeSuccess returns a mock JSON response for a successful Ollama judge call.
func OllamaJudgeSuccess() string {
	return llmvcr.OllamaResponse(ValidJudgeResultObj)
}
