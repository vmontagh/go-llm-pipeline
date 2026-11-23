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

// TestVCRReplay replays stub-only cassettes from testdata/cassettes, so no
// network is used. Re-record one (after deleting its file) with:
//
//	VCR_MODE=record go test -run TestVCRReplay ./pkg/provider/ollama/...
func TestVCRReplay(t *testing.T) {
	t.Parallel()

	t.Run("embed", func(t *testing.T) {
		t.Parallel()
		host := replayHost(t, "application/json", `{"embeddings":[[0.1,0.2,0.3]]}`)
		emb := ollama.NewEmbedder(host, "nomic-embed-text", ollama.WithHTTPClient(vcrtest.Client(t, "embed_single")))

		got, err := emb.Embed(t.Context(), "the quick brown fox")

		if err != nil {
			t.Fatalf("Embed() error = %v, want nil", err)
		}
		if diff := cmp.Diff([]float64{0.1, 0.2, 0.3}, got); diff != "" {
			t.Errorf("Embed() mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("stream", func(t *testing.T) {
		t.Parallel()
		host := replayHost(t, "application/x-ndjson", `{"message":{"role":"assistant","content":"Hel"},"done":false}
{"message":{"role":"assistant","content":"lo"},"done":false}
{"message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":5,"eval_count":2}
`)
		p := ollama.NewProvider(host, "llama3", ollama.WithHTTPClient(vcrtest.Client(t, "chat_stream")))
		s, ok := provpkg.AsStreamer(p)
		if !ok {
			t.Fatal("AsStreamer() = false, want true")
		}

		seq, _ := s.Stream(t.Context(), nil, []provpkg.Message{provpkg.TextMessage(provpkg.RoleUser, "hi")})
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
	})
}

// replayHost returns an unreachable host for replay, where the cassette
// matches on method and path. While recording, it returns a stub server that
// answers every request with body.
func replayHost(t *testing.T, contentType, body string) string {
	t.Helper()
	if !vcrtest.Recording() {
		return "http://ollama.invalid"
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("w.Write() = %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}
