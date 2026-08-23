package embeddings_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/vmontagh/go-llm-pipeline/pkg/provider/ollama"
	"github.com/vmontagh/go-llm-pipeline/pkg/vectorstore"
)

var errNonStringInput = errors.New("input element is not a string")

// cannedTransport maps known inputs to fixed embeddings so the wiring between
// the Ollama embedder and pkg/vectorstore can be exercised fully offline.
type cannedTransport map[string][]float64

func (c cannedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	var req struct {
		Input any `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return nil, err
	}
	var inputs []string
	switch in := req.Input.(type) {
	case string:
		inputs = []string{in}
	case []any:
		for _, v := range in {
			s, ok := v.(string)
			if !ok {
				return nil, errNonStringInput
			}
			inputs = append(inputs, s)
		}
	}
	embeddings := make([][]float64, len(inputs))
	for i, in := range inputs {
		vec, ok := c[in]
		if !ok {
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Body:       io.NopCloser(strings.NewReader("unknown input")),
				Header:     make(http.Header),
			}, nil
		}
		embeddings[i] = vec
	}
	body, err := json.Marshal(struct {
		Embeddings [][]float64 `json:"embeddings"`
	}{Embeddings: embeddings})
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(string(body))),
		Header:     make(http.Header),
	}, nil
}

// TestEmbedderVectorStoreWiring verifies the offline embedder -> MemoryStore ->
// Nearest/FindDuplicates loop end to end with canned embeddings.
func TestEmbedderVectorStoreWiring(t *testing.T) {
	t.Parallel()
	canned := cannedTransport{
		"cat":      {1, 0, 0},
		"kitten":   {0.99, 0.14, 0},
		"airplane": {0, 0, 1},
	}
	emb := ollama.NewEmbedder("http://ollama.test", "nomic",
		ollama.WithHTTPClient(&http.Client{Transport: canned}))

	texts := []string{"cat", "kitten", "airplane"}
	vecs, err := emb.EmbedBatch(t.Context(), texts)
	if err != nil {
		t.Fatalf("EmbedBatch error = %v, want nil", err)
	}

	store := vectorstore.NewMemoryStore()
	items := make([]vectorstore.Item, len(texts))
	for i, text := range texts {
		item := vectorstore.Item{ID: text, Vector: vecs[i]}
		items[i] = item
		if err := store.Add(t.Context(), item); err != nil {
			t.Fatalf("Add(%q) error = %v, want nil", text, err)
		}
	}

	query, err := emb.Embed(t.Context(), "cat")
	if err != nil {
		t.Fatalf("Embed(cat) error = %v, want nil", err)
	}
	neighbors, err := store.Nearest(t.Context(), query, 2)
	if err != nil {
		t.Fatalf("Nearest error = %v, want nil", err)
	}
	gotIDs := []string{neighbors[0].Item.ID, neighbors[1].Item.ID}
	if diff := cmp.Diff([]string{"cat", "kitten"}, gotIDs); diff != "" {
		t.Errorf("Nearest IDs mismatch (-want +got):\n%s", diff)
	}

	dupes, err := vectorstore.FindDuplicates(t.Context(), items, 0.9)
	if err != nil {
		t.Fatalf("FindDuplicates error = %v, want nil", err)
	}
	if len(dupes) != 1 {
		t.Fatalf("FindDuplicates returned %d pairs, want 1", len(dupes))
	}
	gotPair := []string{dupes[0].A.ID, dupes[0].B.ID}
	if diff := cmp.Diff([]string{"cat", "kitten"}, gotPair); diff != "" {
		t.Errorf("duplicate pair mismatch (-want +got):\n%s", diff)
	}

	clusters, err := vectorstore.ClusterItems(items, 0.9)
	if err != nil {
		t.Fatalf("ClusterItems error = %v, want nil", err)
	}
	gotClusters := make([][]string, len(clusters))
	for i, c := range clusters {
		ids := make([]string, len(c.Items))
		for j, item := range c.Items {
			ids[j] = item.ID
		}
		gotClusters[i] = ids
	}
	// Clusters are sorted by their first member's ID, so the airplane singleton
	// precedes the cat/kitten group.
	wantClusters := [][]string{{"airplane"}, {"cat", "kitten"}}
	if diff := cmp.Diff(wantClusters, gotClusters); diff != "" {
		t.Errorf("clusters mismatch (-want +got):\n%s", diff)
	}
}

// Example wires the Ollama embedder into an in-memory vector store to find the
// stored document nearest to a query. It talks to a local Ollama server, so it
// is compiled but not run as part of the test suite.
func Example() {
	ctx := context.Background()
	emb := ollama.NewEmbedder("http://localhost:11434", "nomic-embed-text")

	store := vectorstore.NewMemoryStore()
	for _, doc := range []string{"the cat sat on the mat", "a plane took off"} {
		vec, err := emb.Embed(ctx, doc)
		if err != nil {
			panic(err)
		}
		// vectorstore.Vector is []float64, so the embedder output is assigned
		// directly with no conversion.
		if err := store.Add(ctx, vectorstore.Item{ID: doc, Vector: vec}); err != nil {
			panic(err)
		}
	}

	query, err := emb.Embed(ctx, "a kitten on a rug")
	if err != nil {
		panic(err)
	}
	neighbors, err := store.Nearest(ctx, query, 1)
	if err != nil {
		panic(err)
	}
	fmt.Println(neighbors[0].Item.ID)
}
