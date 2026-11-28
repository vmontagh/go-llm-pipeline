//go:build (unix && !aix) || windows

package appendfile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/internal/appendfile"
)

// TestLock_ExcludesOtherHandlesUntilClosed checks that the lock is exclusive
// across independent handles to the same file, fails fast with ErrLocked
// instead of blocking, and is released when the holder closes its handle.
func TestLock_ExcludesOtherHandlesUntilClosed(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), fileName)
	holder := openReadWrite(t, path)
	contender := openReadWrite(t, path)

	if err := appendfile.Lock(holder); err != nil {
		t.Fatalf("Lock(holder) = %v, want nil", err)
	}
	if diff := cmp.Diff(appendfile.ErrLocked, appendfile.Lock(contender), cmpopts.EquateErrors()); diff != "" {
		t.Errorf("Lock(contender) while held mismatch (-want +got):\n%s", diff)
	}

	if err := holder.Close(); err != nil {
		t.Fatalf("closing holder: %v", err)
	}
	if err := appendfile.Lock(contender); err != nil {
		t.Errorf("Lock(contender) after holder closed = %v, want nil", err)
	}
}

func openReadWrite(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := appendfile.OpenReadWrite(path, permOwnerOnly)
	if err != nil {
		t.Fatalf("OpenReadWrite(%q) = %v, want nil", path, err)
	}
	t.Cleanup(func() { _ = f.Close() }) // second Close after an explicit one is harmless
	return f
}
