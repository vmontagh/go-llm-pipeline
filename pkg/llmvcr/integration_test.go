package llmvcr_test

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"

	"github.com/vmontagh/go-llm-pipeline/pkg/llmvcr"
	provpkg "github.com/vmontagh/go-llm-pipeline/pkg/provider"
	"github.com/vmontagh/go-llm-pipeline/pkg/provider/gemini"
	"github.com/vmontagh/go-llm-pipeline/pkg/provider/openai"
)

const (
	openAISuccessBody = `{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello-openai"},"finish_reason":"stop"}]}`
	geminiSuccessBody = `{"candidates":[{"content":{"parts":[{"text":"hello-gemini"}],"role":"model"}}]}`
	secretAPIKey      = "sk-integrationSECRET1234567890abcd"
)

func TestIntegration_OpenAI_RecordReplayScrub(t *testing.T) {
	t.Parallel()
	cassettePath := filepath.Join(t.TempDir(), "openai")

	// Record: real HTTP through the recorder against a stub OpenAI server.
	srv := newJSONServer(t, openAISuccessBody)
	recRec, err := llmvcr.New(cassettePath, llmvcr.ModeRecordOnce)
	if err != nil {
		t.Fatalf("New(record) = %v", err)
	}
	p := openai.NewProvider(nil,
		openai.WithHTTPClient(recRec.HTTPClient()),
		openai.WithBaseURL(srv.URL),
	)
	got := p.Call(context.Background(), &provpkg.Lease[string]{Value: secretAPIKey}, "prompt", nil)
	if got.Outcome != provpkg.OutcomeSuccess {
		t.Fatalf("record Call outcome = %v (err %v), want OutcomeSuccess", got.Outcome, got.Err)
	}
	if got.Text != "hello-openai" {
		t.Errorf("record Call text = %q, want %q", got.Text, "hello-openai")
	}
	if err := recRec.Stop(); err != nil {
		t.Fatalf("Stop(record) = %v", err)
	}

	// The Authorization bearer token must not be persisted.
	if c := readCassette(t, cassettePath+".yaml"); strings.Contains(c, secretAPIKey) {
		t.Errorf("cassette leaked OpenAI key:\n%s", c)
	}

	// Replay: offline, against an unreachable base URL with a matching path.
	replayRec, err := llmvcr.New(cassettePath, llmvcr.ModeReplayOnly)
	if err != nil {
		t.Fatalf("New(replay) = %v", err)
	}
	t.Cleanup(func() { stopRec(t, replayRec) })
	rp := openai.NewProvider(nil,
		openai.WithHTTPClient(replayRec.HTTPClient()),
		openai.WithBaseURL("https://example.invalid"),
	)
	rgot := rp.Call(context.Background(), &provpkg.Lease[string]{Value: "unused"}, "prompt", nil)
	if rgot.Outcome != provpkg.OutcomeSuccess {
		t.Fatalf("replay Call outcome = %v (err %v), want OutcomeSuccess", rgot.Outcome, rgot.Err)
	}
	if rgot.Text != "hello-openai" {
		t.Errorf("replay Call text = %q, want %q", rgot.Text, "hello-openai")
	}
}

func newGeminiClient(t *testing.T, endpoint string, httpClient *http.Client) *genai.Client {
	t.Helper()
	client, err := genai.NewClient(context.Background(),
		option.WithHTTPClient(httpClient),
		option.WithEndpoint(endpoint),
		option.WithAPIKey("fake-gemini-key"),
	)
	if err != nil {
		t.Fatalf("genai.NewClient = %v", err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("client.Close = %v", err)
		}
	})
	return client
}

func TestIntegration_Gemini_RecordReplay(t *testing.T) {
	t.Parallel()
	cassettePath := filepath.Join(t.TempDir(), "gemini")

	// Record against a stub Gemini server.
	srv := newJSONServer(t, geminiSuccessBody)
	recRec, err := llmvcr.New(cassettePath, llmvcr.ModeRecordOnce)
	if err != nil {
		t.Fatalf("New(record) = %v", err)
	}
	client := newGeminiClient(t, srv.URL, recRec.HTTPClient())
	p := gemini.NewProvider(gemini.DefaultModel, []*genai.Client{client})
	got := p.Call(context.Background(), &provpkg.Lease[*genai.Client]{Value: client}, "prompt", nil)
	if got.Outcome != provpkg.OutcomeSuccess {
		t.Fatalf("record Call outcome = %v (err %v), want OutcomeSuccess", got.Outcome, got.Err)
	}
	if got.Text != "hello-gemini" {
		t.Errorf("record Call text = %q, want %q", got.Text, "hello-gemini")
	}
	if err := recRec.Stop(); err != nil {
		t.Fatalf("Stop(record) = %v", err)
	}

	// The API key must not be persisted (query param or x-goog-api-key header).
	if c := readCassette(t, cassettePath+".yaml"); strings.Contains(c, "fake-gemini-key") {
		t.Errorf("cassette leaked Gemini key:\n%s", c)
	}

	// Replay offline against an unreachable endpoint with a matching path.
	replayRec, err := llmvcr.New(cassettePath, llmvcr.ModeReplayOnly)
	if err != nil {
		t.Fatalf("New(replay) = %v", err)
	}
	t.Cleanup(func() { stopRec(t, replayRec) })
	rclient := newGeminiClient(t, "https://generativelanguage.googleapis.com", replayRec.HTTPClient())
	rp := gemini.NewProvider(gemini.DefaultModel, []*genai.Client{rclient})
	rgot := rp.Call(context.Background(), &provpkg.Lease[*genai.Client]{Value: rclient}, "prompt", nil)
	if rgot.Outcome != provpkg.OutcomeSuccess {
		t.Fatalf("replay Call outcome = %v (err %v), want OutcomeSuccess", rgot.Outcome, rgot.Err)
	}
	if rgot.Text != "hello-gemini" {
		t.Errorf("replay Call text = %q, want %q", rgot.Text, "hello-gemini")
	}
}
