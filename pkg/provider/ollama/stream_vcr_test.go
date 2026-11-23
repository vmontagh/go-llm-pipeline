package ollama_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-cmp/cmp"

	provpkg "github.com/vmontagh/go-llm-pipeline/pkg/provider"
	"github.com/vmontagh/go-llm-pipeline/pkg/provider/ollama"
	"github.com/vmontagh/go-llm-pipeline/pkg/vcrtest"
)

// newChatStreamServer returns a stub Ollama /api/chat server that streams
// NDJSON, used only while (re)recording the cassette.
func newChatStreamServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		lines := []string{
			`{"message":{"role":"assistant","content":"Hel"},"done":false}`,
			`{"message":{"role":"assistant","content":"lo"},"done":false}`,
			`{"message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":5,"eval_count":2}`,
		}
		for _, l := range lines {
			if _, err := w.Write([]byte(l + "\n")); err != nil {
				t.Errorf("w.Write() = %v", err)
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestProvider_Stream_VCRReplay replays a recorded NDJSON stream from the
// stub-only cassette testdata/cassettes/chat_stream.yaml. Re-record it (after
// deleting the file) with:
//
//	VCR_MODE=record go test -run TestProvider_Stream_VCRReplay ./pkg/provider/ollama/...
func TestProvider_Stream_VCRReplay(t *testing.T) {
	t.Parallel()
	host := "http://ollama.invalid"
	if vcrtest.Recording() {
		host = newChatStreamServer(t).URL
	}
	p := ollama.NewProvider(host, "llama3", ollama.WithHTTPClient(vcrtest.Client(t, "chat_stream")))
	s, ok := provpkg.AsStreamer(p)
	if !ok {
		t.Fatal("AsStreamer() = false, want true")
	}

	seq, res := s.Stream(t.Context(), nil, []provpkg.Message{provpkg.TextMessage(provpkg.RoleUser, "hi")})
	if res.Outcome != provpkg.OutcomeSuccess {
		t.Fatalf("Stream() = %+v, want success", res)
	}
	var got []provpkg.Chunk
	for c, err := range seq {
		if err != nil {
			t.Fatalf("ranging over stream: %v", err)
		}
		got = append(got, c)
	}

	want := []provpkg.Chunk{{Text: "Hel"}, {Text: "lo"}, {Usage: &provpkg.Usage{Input: 5, Output: 2}}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Stream() chunks mismatch (-want +got):\n%s", diff)
	}
}
