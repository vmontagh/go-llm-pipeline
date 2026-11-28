package vectorstoretest_test

import (
	"testing"

	"github.com/vmontagh/go-llm-pipeline/pkg/vectorstore"
	"github.com/vmontagh/go-llm-pipeline/pkg/vectorstore/vectorstoretest"
)

// TestRunStoreConformanceMemoryStore runs the harness against the reference
// in-memory store, so the kit itself is exercised (and a broken check is
// caught) in its own package rather than only from downstream backends.
func TestRunStoreConformanceMemoryStore(t *testing.T) {
	t.Parallel()
	vectorstoretest.RunStoreConformance(t, func(tb testing.TB) vectorstoretest.Store {
		tb.Helper()
		return vectorstore.NewMemoryStore()
	})
}
