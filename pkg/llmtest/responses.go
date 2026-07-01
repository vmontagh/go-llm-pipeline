package llmtest

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/vmontagh/go-llm-pipeline/pkg/vcrpatch"
)

const (
	roleModel   = "model"
	modelKey    = "model"
	methodPost  = "POST"
	protoHTTP11 = "HTTP/1.1"
	contentKey  = "content"
	roleKey     = "role"
)

// ValidResultObj is a mock JSON string containing a valid tags response object.
const ValidResultObj = `{"heading": "Cute Indoor Cat", "tags": ["animal", "cat", "indoor", "cute", "feline", "pet", "whiskers", "paws", "meow", "purr", "furry", "kitty", "kitten", "domestic", "mammal", "portrait", "eyes", "ears", "tail", "snout", "beautiful", "adorable", "looking", "fur"]}`

// ValidJudgeResultObj is a mock JSON string containing a valid judge response object.
const ValidJudgeResultObj = `{"approved": true, "selected_draft_index": 0, "feedback": "Looks good"}`

// GeminiSuccess returns a mock JSON response for a successful Gemini generateContent call.
func GeminiSuccess() string {
	return GeminiResponse(ValidResultObj)
}

// GeminiJudgeSuccess returns a mock JSON response for a successful Gemini judge call.
func GeminiJudgeSuccess() string {
	return GeminiResponse(ValidJudgeResultObj)
}

// GeminiResponse wraps text in candidates[].content.parts[].text.
func GeminiResponse(text string) string {
	resp := map[string]interface{}{
		"candidates": []interface{}{
			map[string]interface{}{
				contentKey: map[string]interface{}{
					"parts": []interface{}{
						map[string]interface{}{"text": text},
					},
					roleKey: roleModel,
				},
			},
		},
	}
	b, err := json.Marshal(resp)
	if err != nil {
		return fmt.Sprintf(`{"error":"marshal: %v"}`, err)
	}
	return string(b)
}

// GeminiQuotaExceeded returns a mock JSON response simulating a Gemini 429 error.
func GeminiQuotaExceeded() string {
	return `{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`
}

// OpenAISuccess returns a mock JSON response for a successful OpenAI completions call.
func OpenAISuccess() string {
	return OpenAIResponse(ValidResultObj)
}

// OpenAIJudgeSuccess returns a mock JSON response for a successful OpenAI judge call.
func OpenAIJudgeSuccess() string {
	return OpenAIResponse(ValidJudgeResultObj)
}

// OpenAIResponse wraps text in choices[].message.content.
func OpenAIResponse(text string) string {
	resp := map[string]interface{}{
		"id":      "chatcmpl-123",
		"object":  "chat.completion",
		"created": 1677652288,
		modelKey:  "gpt-4o-mini",
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"message": map[string]interface{}{
					roleKey:    "assistant",
					contentKey: text,
				},
				"finish_reason": "stop",
			},
		},
	}
	b, err := json.Marshal(resp)
	if err != nil {
		return fmt.Sprintf(`{"error":"marshal: %v"}`, err)
	}
	return string(b)
}

// OpenAIQuotaExceeded returns a mock JSON response simulating an OpenAI 429 quota error.
func OpenAIQuotaExceeded() string {
	return `{"error": {"message": "You exceeded your current quota.", "type": "insufficient_quota", "param": null, "code": "insufficient_quota"}}`
}

// OllamaSuccess returns a mock JSON response for a successful Ollama generate call.
func OllamaSuccess() string {
	return OllamaResponse(ValidResultObj)
}

// OllamaJudgeSuccess returns a mock JSON response for a successful Ollama judge call.
func OllamaJudgeSuccess() string {
	return OllamaResponse(ValidJudgeResultObj)
}

// OllamaResponse wraps text in response field.
func OllamaResponse(text string) string {
	resp := map[string]interface{}{
		modelKey:     "gemma3",
		"created_at": "2026-06-06T00:00:00Z",
		"response":   text,
		"done":       true,
	}
	b, err := json.Marshal(resp)
	if err != nil {
		return fmt.Sprintf(`{"error":"marshal: %v"}`, err)
	}
	return string(b)
}

// OllamaQuotaExceeded returns a mock JSON response simulating an Ollama 429 error.
func OllamaQuotaExceeded() string {
	return `{"error": "rate limit exceeded"}`
}

// OllamaMalformedJSON returns a mock JSON response simulating a parse failure.
func OllamaMalformedJSON(text string) string {
	return OllamaResponse(text)
}

// GeminiInteraction constructs a cassette interaction for a Gemini call.
func GeminiInteraction(code int, body string) vcrpatch.Interaction {
	status := fmt.Sprintf("%d %s", code, http.StatusText(code))
	return vcrpatch.Interaction{
		Request: vcrpatch.InteractionReq{
			Method:     methodPost,
			Host:       "generativelanguage.googleapis.com",
			URL:        "https://generativelanguage.googleapis.com/v1beta/models/gemini-3.5-flash:generateContent",
			Proto:      protoHTTP11,
			ProtoMajor: 1,
			ProtoMinor: 1,
			Headers:    map[string][]string{},
		},
		Response: vcrpatch.InteractionResp{
			Body:       body,
			Code:       code,
			Status:     status,
			Duration:   "0s",
			Proto:      protoHTTP11,
			ProtoMajor: 1,
			ProtoMinor: 1,
			Headers:    map[string][]string{},
		},
	}
}

// OpenAIInteraction constructs a cassette interaction for an OpenAI call.
func OpenAIInteraction(code int, body string) vcrpatch.Interaction {
	status := fmt.Sprintf("%d %s", code, http.StatusText(code))
	return vcrpatch.Interaction{
		Request: vcrpatch.InteractionReq{
			Method:     methodPost,
			Host:       "api.openai.com",
			URL:        "https://api.openai.com/v1/chat/completions",
			Proto:      protoHTTP11,
			ProtoMajor: 1,
			ProtoMinor: 1,
			Headers:    map[string][]string{},
		},
		Response: vcrpatch.InteractionResp{
			Body:       body,
			Code:       code,
			Status:     status,
			Duration:   "0s",
			Proto:      protoHTTP11,
			ProtoMajor: 1,
			ProtoMinor: 1,
			Headers:    map[string][]string{},
		},
	}
}

// OllamaInteraction constructs a cassette interaction for an Ollama call.
func OllamaInteraction(code int, body string) vcrpatch.Interaction {
	status := fmt.Sprintf("%d %s", code, http.StatusText(code))
	return vcrpatch.Interaction{
		Request: vcrpatch.InteractionReq{
			Method:     methodPost,
			Host:       "localhost:11434",
			URL:        "http://localhost:11434/api/generate",
			Proto:      protoHTTP11,
			ProtoMajor: 1,
			ProtoMinor: 1,
			Headers:    map[string][]string{},
		},
		Response: vcrpatch.InteractionResp{
			Body:       body,
			Code:       code,
			Status:     status,
			Duration:   "0s",
			Proto:      protoHTTP11,
			ProtoMajor: 1,
			ProtoMinor: 1,
			Headers:    map[string][]string{},
		},
	}
}
