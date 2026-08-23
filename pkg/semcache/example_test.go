package semcache_test

import (
	"context"
	"fmt"
	"log"

	"github.com/vmontagh/go-llm-pipeline/pkg/semcache"
)

// ExampleCache shows the default exact-match-only cache: no embeddings, fully
// offline. A repeated (model, params, prompt) key is served from the cache.
func ExampleCache() {
	cache, err := semcache.New()
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()

	key := semcache.Key{
		Model:  "llama3",
		Params: map[string]string{"temperature": "0.2"},
		Prompt: "summarize the release notes",
	}

	// First call misses; the caller invokes the provider and stores the result.
	if _, hit, err := cache.Get(ctx, key); err != nil {
		log.Fatal(err)
	} else if !hit {
		if err := cache.Put(ctx, key, "three bug fixes and one feature"); err != nil {
			log.Fatal(err)
		}
	}

	// A later identical request is served from the cache.
	response, hit, err := cache.Get(ctx, key)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("hit=%t response=%q\n", hit, response)
	// Output: hit=true response="three bug fixes and one feature"
}

// staticEmbedder is a trivial [semcache.Embedder] standing in for a real
// embedding model; production callers inject one backed by, for example, the
// Ollama embeddings adapter.
type staticEmbedder struct {
	vectors map[string][]float64
}

func (e staticEmbedder) Embed(_ context.Context, input string) ([]float64, error) {
	v, ok := e.vectors[input]
	if !ok {
		return nil, fmt.Errorf("%w: %q", errNoVector, input)
	}
	return v, nil
}

// ExampleCache_semantic shows the opt-in semantic fallback: a near-duplicate
// prompt whose embedding is within the similarity threshold reuses a prior
// response.
func ExampleCache_semantic() {
	embedder := staticEmbedder{vectors: map[string][]float64{
		"how do I reset my password":    {1, 0, 0},
		"steps to reset my password":    {0.98, 0.2, 0},
		"what is the capital of France": {0, 1, 0},
	}}
	cache, err := semcache.New(semcache.WithSemantic(embedder, 0.9))
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()

	if err := cache.Put(ctx, semcache.Key{Model: "llama3", Prompt: "how do I reset my password"}, "open Settings > Security"); err != nil {
		log.Fatal(err)
	}

	response, hit, err := cache.Get(ctx, semcache.Key{Model: "llama3", Prompt: "steps to reset my password"})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("hit=%t response=%q\n", hit, response)
	// Output: hit=true response="open Settings > Security"
}
