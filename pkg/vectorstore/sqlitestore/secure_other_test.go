//go:build !unix

package sqlitestore

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestOpenRejectsUnsupportedPlatform(t *testing.T) {
	_, err := Open(t.Context(), filepath.Join(t.TempDir(), "store.db"))
	if !errors.Is(err, errOwnerOnlyUnsupported) {
		t.Errorf("Open() on unsupported platform = %v, want errors.Is(errOwnerOnlyUnsupported)", err)
	}
}
