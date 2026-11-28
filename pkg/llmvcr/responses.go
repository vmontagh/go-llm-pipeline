package llmvcr

import (
	"encoding/json"
	"fmt"
	"net/http"
)

const (
	roleModel   = "model"
	modelKey    = "model"
	methodPost  = "POST"
	protoHTTP11 = "HTTP/1.1"
	contentKey  = "content"
	roleKey     = "role"
)

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

// GeminiInteraction constructs a cassette interaction for a Gemini call.
func GeminiInteraction(code int, body string) Interaction {
	status := fmt.Sprintf("%d %s", code, http.StatusText(code))
	return Interaction{
		Request: InteractionReq{
			Method:     methodPost,
			Host:       "generativelanguage.googleapis.com",
			URL:        "https://generativelanguage.googleapis.com/v1beta/models/gemini-3.5-flash:generateContent",
			Proto:      protoHTTP11,
			ProtoMajor: 1,
			ProtoMinor: 1,
			Headers:    map[string][]string{},
		},
		Response: InteractionResp{
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
func OpenAIInteraction(code int, body string) Interaction {
	status := fmt.Sprintf("%d %s", code, http.StatusText(code))
	return Interaction{
		Request: InteractionReq{
			Method:     methodPost,
			Host:       "api.openai.com",
			URL:        "https://api.openai.com/v1/chat/completions",
			Proto:      protoHTTP11,
			ProtoMajor: 1,
			ProtoMinor: 1,
			Headers:    map[string][]string{},
		},
		Response: InteractionResp{
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
func OllamaInteraction(code int, body string) Interaction {
	status := fmt.Sprintf("%d %s", code, http.StatusText(code))
	return Interaction{
		Request: InteractionReq{
			Method:     methodPost,
			Host:       "localhost:11434",
			URL:        "http://localhost:11434/api/generate",
			Proto:      protoHTTP11,
			ProtoMajor: 1,
			ProtoMinor: 1,
			Headers:    map[string][]string{},
		},
		Response: InteractionResp{
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
