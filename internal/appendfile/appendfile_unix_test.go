//go:build unix

package appendfile_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/internal/appendfile"
)

const (
	permOwnerOnlyDir = 0o700
	// rejectTimeout bounds how long a rejected open may take; a blocking open
	// of a FIFO would otherwise hang the test forever.
	rejectTimeout = 5 * time.Second
	devNull       = "/dev/null"
)

func TestOpenCreatesOwnerOnlyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.log")

	f, err := appendfile.Open(path, permOwnerOnly)
	if err != nil {
		t.Fatalf("Open(%q) error = %v", path, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("closing %q: %v", path, err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q): %v", path, err)
	}
	if diff := cmp.Diff(os.FileMode(permOwnerOnly), info.Mode()); diff != "" {
		t.Errorf("mode of %q mismatch (-want +got):\n%s", path, diff)
	}
}

func TestOpenRejectsUnsafePath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		setup   func(t *testing.T, dir string) string
		wantErr error
	}{
		{
			name: "directory",
			setup: func(t *testing.T, dir string) string {
				t.Helper()
				path := filepath.Join(dir, "subdir")
				if err := os.Mkdir(path, permOwnerOnlyDir); err != nil {
					t.Fatalf("Mkdir(%q): %v", path, err)
				}
				return path
			},
			wantErr: appendfile.ErrNotRegularFile,
		},
		{
			name:    "fifo without reader",
			setup:   makeFIFO,
			wantErr: appendfile.ErrNotRegularFile,
		},
		{
			name: "fifo with reader",
			setup: func(t *testing.T, dir string) string {
				t.Helper()
				path := makeFIFO(t, dir)
				openReader(t, path)
				return path
			},
			wantErr: appendfile.ErrNotRegularFile,
		},
		{
			name:    "character device",
			setup:   func(*testing.T, string) string { return devNull },
			wantErr: appendfile.ErrNotRegularFile,
		},
		{
			name: "symlink to regular file",
			setup: func(t *testing.T, dir string) string {
				t.Helper()
				target := filepath.Join(dir, "target.log")
				if err := os.WriteFile(target, nil, permOwnerOnly); err != nil {
					t.Fatalf("WriteFile(%q): %v", target, err)
				}
				return makeSymlink(t, target, filepath.Join(dir, "link.log"))
			},
			wantErr: appendfile.ErrSymlink,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := tt.setup(t, t.TempDir())

			err := openWithin(t, path, rejectTimeout)

			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Open(%q) error mismatch (-want +got):\n%s", path, diff)
			}
		})
	}
}

// openWithin calls appendfile.Open and fails the test if it does not return
// within timeout, so a regression to a blocking open fails instead of hanging.
func openWithin(t *testing.T, path string, timeout time.Duration) error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		f, err := appendfile.Open(path, permOwnerOnly)
		if err == nil {
			err = f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		t.Fatalf("Open(%q) did not return within %v", path, timeout)
		return nil
	}
}

func makeFIFO(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(path, permOwnerOnly); err != nil {
		t.Fatalf("Mkfifo(%q): %v", path, err)
	}
	return path
}

// openReader holds a non-blocking reader on the FIFO for the rest of the test,
// so a writer's open succeeds and only the fstat check can reject it.
func openReader(t *testing.T, path string) {
	t.Helper()
	r, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatalf("opening reader on %q: %v", path, err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Errorf("closing reader on %q: %v", path, err)
		}
	})
}

func makeSymlink(t *testing.T, target, link string) string {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink(%q, %q): %v", target, link, err)
	}
	return link
}
