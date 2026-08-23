//go:build integration

package ollama_test

import (
	"cmp"
	"os"
	"testing"
	"time"

	"github.com/vmontagh/go-llm-pipeline/pkg/provider/ollama"
)

// TestEmbedder_Live is an opt-in smoke test against a real Ollama server. It is
// gated behind the `integration` build tag and skips unless OLLAMA_HOST is set,
// so it never runs in the default `make test` lane. Run it with:
//
//	OLLAMA_HOST=http://localhost:11434 go test -tags=integration -run TestEmbedder_Live ./pkg/provider/ollama/...
//
// It is an interim real-model check until the godog harness (#52) lands.
func TestEmbedder_Live(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live Ollama smoke test in short mode")
	}
	host := os.Getenv("OLLAMA_HOST")
	if host == "" {
		t.Skip("set OLLAMA_HOST to run the live Ollama embedder smoke test")
	}
	model := cmp.Or(os.Getenv("OLLAMA_EMBED_MODEL"), "nomic-embed-text")

	emb := ollama.NewEmbedder(host, model, ollama.WithHTTPClientTimeout(30*time.Second))

	vec, err := emb.Embed(t.Context(), "the quick brown fox")
	if err != nil {
		t.Fatalf("Embed error = %v, want nil", err)
	}
	if len(vec) == 0 {
		t.Fatal("Embed returned an empty vector")
	}

	vecs, err := emb.EmbedBatch(t.Context(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("EmbedBatch error = %v, want nil", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("EmbedBatch returned %d vectors, want 2", len(vecs))
	}
	if len(vecs[0]) != len(vec) {
		t.Errorf("batch vector dimension = %d, want %d (single embed dimension)", len(vecs[0]), len(vec))
	}
}
