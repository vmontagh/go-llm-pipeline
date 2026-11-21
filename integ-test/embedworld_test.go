package integtest

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/vmontagh/go-llm-pipeline/pkg/vectorstore"
	"github.com/vmontagh/go-llm-pipeline/pkg/vectorstore/sqlitestore"
)

var errStepFailed = errors.New("step failed")

// probeItem is a minimal valid item used to check whether a store handle is
// still usable.
var probeItem = vectorstore.Item{ID: "probe", Vector: vectorstore.Vector{1, 0}}

func TestEmbedWorldAfterScenario(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		stepErr   error
		openStore bool
	}{
		{name: "stepFailed_noStore", stepErr: errStepFailed},
		{name: "stepFailed_storeOpen", stepErr: errStepFailed, openStore: true},
		{name: "stepPassed_storeOpen", openStore: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := &embedWorld{}
			var store *sqlitestore.Store
			if tt.openStore {
				if !sqliteSupported {
					t.Skip("sqlitestore refuses to open outside Unix")
				}
				store = openSQLiteStore(t)
				w.sqlite = store
			}

			_, err := w.afterScenario(t.Context(), nil, tt.stepErr)

			if err != nil {
				t.Errorf("afterScenario(stepErr=%v) error = %v, want nil", tt.stepErr, err)
			}
			if w.sqlite != nil {
				t.Error("afterScenario left w.sqlite set, want it cleared")
			}
			if store != nil && store.Add(t.Context(), probeItem) == nil {
				t.Error("store.Add after afterScenario succeeded, want the handle closed")
			}
		})
	}
}

// openSQLiteStore opens a usable store in a per-test temp dir. A cleanup
// closes it again so a failing assertion never leaks the handle; closing an
// already-closed store is a no-op.
func openSQLiteStore(t *testing.T) *sqlitestore.Store {
	t.Helper()
	store, err := sqlitestore.Open(t.Context(), filepath.Join(t.TempDir(), sqliteStoreFile))
	if err != nil {
		t.Fatalf("sqlitestore.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("closing sqlite store in cleanup: %v", err)
		}
	})
	if err := store.Add(t.Context(), probeItem); err != nil {
		t.Fatalf("store.Add before afterScenario: %v", err)
	}
	return store
}
