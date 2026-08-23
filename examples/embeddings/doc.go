// Package embeddings shows how to wire the local Ollama embedder
// ([github.com/vmontagh/go-llm-pipeline/pkg/provider/ollama.Embedder]) into the
// dependency-free [github.com/vmontagh/go-llm-pipeline/pkg/vectorstore] for a
// fully local-first semantic search and deduplication loop.
//
// The two packages are deliberately decoupled: the embedder returns plain
// []float64 and never imports the vector store, while the vector store is a
// leaf that never imports a provider. This package is the composition root that
// joins them, the same role a semantic cache or application would play.
package embeddings
