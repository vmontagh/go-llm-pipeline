//go:build !unix

package sqlitestore

import (
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestOpenRejectsUnsupportedPlatform(t *testing.T) {
	_, err := Open(t.Context(), filepath.Join(t.TempDir(), "store.db"))
	if diff := cmp.Diff(errOwnerOnlyUnsupported, err, cmpopts.EquateErrors()); diff != "" {
		t.Errorf("Open() on unsupported platform error mismatch (-want +got):\n%s", diff)
	}
}
