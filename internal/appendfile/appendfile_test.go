package appendfile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/vmontagh/go-llm-pipeline/internal/appendfile"
)

const permOwnerOnly = 0o600

func TestOpenAppendsToExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.log")
	if err := os.WriteFile(path, []byte("first\n"), permOwnerOnly); err != nil {
		t.Fatalf("seeding %q: %v", path, err)
	}

	f, err := appendfile.Open(path, permOwnerOnly)
	if err != nil {
		t.Fatalf("Open(%q) error = %v", path, err)
	}
	if _, err := f.WriteString("second\n"); err != nil {
		t.Fatalf("writing %q: %v", path, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("closing %q: %v", path, err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %q: %v", path, err)
	}
	if diff := cmp.Diff("first\nsecond\n", string(got)); diff != "" {
		t.Errorf("contents of %q mismatch (-want +got):\n%s", path, diff)
	}
}
