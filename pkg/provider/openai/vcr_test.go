package openai_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	provpkg "github.com/vmontagh/go-llm-pipeline/pkg/provider"
	"github.com/vmontagh/go-llm-pipeline/pkg/provider/openai"
	"github.com/vmontagh/go-llm-pipeline/pkg/vcrtest"
)

// TestStreamReplay replays a stub-only cassette from testdata/cassettes, so
// no network is used. Re-record it (after deleting its file) with:
//
//	VCR_MODE=record go test -run TestStreamReplay ./pkg/provider/openai/...
func TestStreamReplay(t *testing.T) {
	t.Parallel()
	baseURL := vcrtest.StubURL(t, "text/event-stream", "data: {\"model\":\"gpt-4o-mini\",\"choices\":[{\"delta\":{\"content\":\"Hel\"}}],\"usage\":null}\n\n"+
		"data: {\"model\":\"gpt-4o-mini\",\"choices\":[{\"delta\":{\"content\":\"lo\"},\"finish_reason\":\"stop\"}],\"usage\":null}\n\n"+
		"data: {\"model\":\"gpt-4o-mini\",\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2}}\n\n"+
		"data: [DONE]\n\n")
	p := openai.NewProvider([]string{"test-key"}, openai.WithBaseURL(baseURL), openai.WithHTTPClient(vcrtest.Client(t, "chat_stream")))
	s, ok := provpkg.AsStreamer(p)
	if !ok {
		t.Fatal("AsStreamer() = false, want true")
	}

	seq, _ := s.Stream(t.Context(), &provpkg.Lease[string]{Value: "test-key"}, []provpkg.Message{provpkg.TextMessage(provpkg.RoleUser, "hi")})
	var got []provpkg.Chunk
	for c, err := range seq {
		if err != nil {
			t.Fatalf("ranging over stream: %v", err)
		}
		got = append(got, c)
	}

	want := []provpkg.Chunk{{Text: "Hel"}, {Text: "lo"}, {Usage: &provpkg.Usage{Input: 5, Output: 2}}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Stream() mismatch (-want +got):\n%s", diff)
	}
}
