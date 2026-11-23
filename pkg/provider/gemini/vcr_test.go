package gemini_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	provpkg "github.com/vmontagh/go-llm-pipeline/pkg/provider"
	"github.com/vmontagh/go-llm-pipeline/pkg/provider/gemini"
	"github.com/vmontagh/go-llm-pipeline/pkg/vcrtest"
)

// TestStreamReplay replays a stub-only cassette from testdata/cassettes, so
// no network is used. Re-record it (after deleting its file) with:
//
//	VCR_MODE=record go test -run TestStreamReplay ./pkg/provider/gemini/...
func TestStreamReplay(t *testing.T) {
	t.Parallel()
	baseURL := vcrtest.StubURL(t, "text/event-stream", "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"Hel\"}]}}],\"usageMetadata\":{\"promptTokenCount\":5},\"modelVersion\":\"gemini-3.5-flash\"}\r\n\r\n"+
		"data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"lo\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":5,\"candidatesTokenCount\":2},\"modelVersion\":\"gemini-3.5-flash\"}\r\n\r\n")
	p := gemini.NewProvider([]string{testAPIKey}, gemini.WithBaseURL(baseURL+apiVersionPath), gemini.WithHTTPClient(vcrtest.Client(t, "stream_generate_content")))
	s, ok := provpkg.AsStreamer(p)
	if !ok {
		t.Fatal("AsStreamer() = false, want true")
	}

	seq, _ := s.Stream(t.Context(), &provpkg.Lease[string]{Value: testAPIKey}, []provpkg.Message{provpkg.TextMessage(provpkg.RoleUser, testPrompt)})
	var got []provpkg.Chunk
	for c, err := range seq {
		if err != nil {
			t.Fatalf("ranging over stream: %v", err)
		}
		got = append(got, c)
	}

	want := []provpkg.Chunk{{Text: "Hel"}, {Text: "lo", Usage: &provpkg.Usage{Input: 5, Output: 2}}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Stream() mismatch (-want +got):\n%s", diff)
	}
}
