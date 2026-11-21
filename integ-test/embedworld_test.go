package integtest

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/vmontagh/go-llm-pipeline/pkg/vectorstore/sqlitestore"
)

var errStepFailed = errors.New("step failed")

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
			if tt.openStore {
				if !sqliteSupported {
					t.Skip("sqlitestore refuses to open outside Unix")
				}
				w.sqlite = openSQLiteStore(t)
			}

			_, err := w.afterScenario(t.Context(), nil, tt.stepErr)

			if err != nil {
				t.Errorf("afterScenario(stepErr=%v) error = %v, want nil", tt.stepErr, err)
			}
			if w.sqlite != nil {
				t.Error("afterScenario left the sqlite store open, want it closed and cleared")
			}
		})
	}
}

// openSQLiteStore opens a store in a per-test temp dir.
func openSQLiteStore(t *testing.T) *sqlitestore.Store {
	t.Helper()
	store, err := sqlitestore.Open(t.Context(), filepath.Join(t.TempDir(), sqliteStoreFile))
	if err != nil {
		t.Fatalf("sqlitestore.Open: %v", err)
	}
	return store
}
