package ollama_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/vmontagh/go-llm-pipeline/pkg/provider/ollama"
	"github.com/vmontagh/go-llm-pipeline/pkg/vcrtest"
)

// embedModel is the model name recorded into the cassette; on replay it only has
// to match what the recorded request carried.
const embedModel = "nomic-embed-text"

// wantVector is the embedding the stub server returns and the cassette therefore
// replays. Keeping it tiny and fixed makes the committed cassette deterministic.
var wantVector = []float64{0.1, 0.2, 0.3}

// newEmbedServer returns a stub Ollama /api/embed server used only while
// (re)recording the cassette.
func newEmbedServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"embeddings":[[0.1,0.2,0.3]]}`)); err != nil {
			t.Errorf("w.Write() = %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestEmbedder_VCRReplay demonstrates the vcrtest harness on the Ollama embedder:
// the Embedder's *http.Client is backed by a committed cassette and the call is
// served entirely from disk with no network access.
//
// The cassette testdata/cassettes/embed_single.yaml is recorded against the
// in-process stub above. Re-record it against a real Ollama instance with:
//
//	VCR_MODE=record OLLAMA_HOST=http://localhost:11434 \
//	  go test -run TestEmbedder_VCRReplay ./pkg/provider/ollama/...
//
// after deleting the existing cassette (record-once leaves an existing file
// untouched). When not recording, host is an unreachable placeholder: the call
// only succeeds because the recorded interaction matches on method and path.
func TestEmbedder_VCRReplay(t *testing.T) {
	t.Parallel()

	host := "http://ollama.invalid"
	if vcrtest.Recording() {
		host = newEmbedServer(t).URL
	}

	emb := ollama.NewEmbedder(host, embedModel,
		ollama.WithHTTPClient(vcrtest.Client(t, "embed_single")),
	)

	got, err := emb.Embed(t.Context(), "the quick brown fox")
	if err != nil {
		t.Fatalf("Embed() error = %v, want nil", err)
	}
	if diff := cmp.Diff(wantVector, got); diff != "" {
		t.Errorf("Embed() vector mismatch (-want +got):\n%s", diff)
	}
}
