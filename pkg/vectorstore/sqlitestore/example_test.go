//go:build unix

package sqlitestore_test

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/vmontagh/go-llm-pipeline/pkg/vectorstore"
	"github.com/vmontagh/go-llm-pipeline/pkg/vectorstore/sqlitestore"
)

func Example() {
	if err := runExample(); err != nil {
		log.Fatal(err)
	}
	// Output: nearest: east (score 1.0)
}

// runExample opens a throwaway store, indexes two vectors, and prints the
// nearest neighbor. It returns errors so its deferred cleanup always runs.
func runExample() error {
	ctx := context.Background()

	// A real caller would pass a durable path; this example uses a throwaway
	// directory so it leaves nothing behind.
	dir, err := os.MkdirTemp("", "sqlitestore-example")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("removing temp dir %q: %v", dir, err)
		}
	}()

	store, err := sqlitestore.Open(ctx, filepath.Join(dir, "vectors.db"))
	if err != nil {
		return err
	}
	defer func() {
		if err := store.Close(); err != nil {
			log.Printf("closing store: %v", err)
		}
	}()

	items := []vectorstore.Item{
		{ID: "east", Vector: vectorstore.Vector{1, 0}},
		{ID: "north", Vector: vectorstore.Vector{0, 1}},
	}
	for _, item := range items {
		if err := store.Add(ctx, item); err != nil {
			return err
		}
	}

	neighbors, err := store.Nearest(ctx, vectorstore.Vector{1, 0}, 1)
	if err != nil {
		return err
	}
	fmt.Printf("nearest: %s (score %.1f)\n", neighbors[0].Item.ID, neighbors[0].Score)
	return nil
}
