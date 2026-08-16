package vectorstore_test

import (
	"context"
	"fmt"
	"log"

	"github.com/vmontagh/go-llm-pipeline/pkg/vectorstore"
)

// ExampleMemoryStore shows the typical flow: the caller computes embeddings
// (here, precomputed vectors stand in for an embedding model) and stores them
// locally, then runs a nearest-neighbor query fully offline.
func ExampleMemoryStore() {
	store := vectorstore.NewMemoryStore()
	ctx := context.Background()

	docs := []vectorstore.Item{
		{ID: "cat", Vector: vectorstore.Vector{1, 0, 0}},
		{ID: "kitten", Vector: vectorstore.Vector{0.9, 0.1, 0}},
		{ID: "airplane", Vector: vectorstore.Vector{0, 0, 1}},
	}
	for _, d := range docs {
		if err := store.Add(ctx, d); err != nil {
			log.Fatal(err)
		}
	}

	neighbors, err := store.Nearest(ctx, vectorstore.Vector{1, 0, 0}, 2)
	if err != nil {
		log.Fatal(err)
	}
	for _, n := range neighbors {
		fmt.Printf("%s %.2f\n", n.Item.ID, n.Score)
	}
	// Output:
	// cat 1.00
	// kitten 0.99
}

// ExampleFindDuplicates shows near-duplicate detection over a slice of items
// without needing a store.
func ExampleFindDuplicates() {
	items := []vectorstore.Item{
		{ID: "a", Vector: vectorstore.Vector{1, 0}},
		{ID: "b", Vector: vectorstore.Vector{1, 0}},
		{ID: "c", Vector: vectorstore.Vector{0, 1}},
	}
	dups, err := vectorstore.FindDuplicates(context.Background(), items, 0.95)
	if err != nil {
		log.Fatal(err)
	}
	for _, d := range dups {
		fmt.Printf("%s~%s %.2f\n", d.A.ID, d.B.ID, d.Score)
	}
	// Output:
	// a~b 1.00
}
