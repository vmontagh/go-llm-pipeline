//go:build unix

package appendfile_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/internal/appendfile"
)

const (
	permOwnerReadOnly = 0o400
	permOwnerOnlyDir  = 0o700
	permWorldReadable = 0o644
	devNull           = "/dev/null"
)

func TestOpen_FileMode(t *testing.T) {
	t.Parallel()
	// Modes are chosen so no common umask changes them.
	tests := []struct {
		name         string
		existingMode os.FileMode
		perm         os.FileMode
		want         os.FileMode
	}{
		{name: "newFile_ownerReadWrite", perm: permOwnerOnly, want: permOwnerOnly},
		{name: "newFile_ownerReadOnly", perm: permOwnerReadOnly, want: permOwnerReadOnly},
		{name: "existingFile_modeKept", existingMode: permWorldReadable, perm: permOwnerOnly, want: permWorldReadable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), fileName)
			if tt.existingMode != 0 {
				createWithMode(t, path, tt.existingMode)
			}

			f, err := appendfile.Open(path, tt.perm)
			if err != nil {
				t.Fatalf("Open(%q, %v): %v", path, tt.perm, err)
			}
			if err := f.Close(); err != nil {
				t.Fatalf("Close(%q): %v", path, err)
			}

			if diff := cmp.Diff(tt.want, modeOf(t, path)); diff != "" {
				t.Errorf("Open(%q, %v) file mode mismatch (-want +got):\n%s", path, tt.perm, diff)
			}
		})
	}
}

// TestOpen_InvalidPath_Rejected has no timeout of its own: a regression to a
// blocking open hangs the FIFO rows until `go test -timeout` fails the run with
// a goroutine dump pointing at Open.
func TestOpen_InvalidPath_Rejected(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		setup   func(t *testing.T, dir string) string
		wantErr error
	}{
		{name: "directory", setup: makeDir, wantErr: appendfile.ErrNotRegularFile},
		{name: "fifoWithoutReader", setup: makeFIFO, wantErr: appendfile.ErrNotRegularFile},
		{name: "fifoWithReader", setup: makeFIFOWithReader, wantErr: appendfile.ErrNotRegularFile},
		{name: "characterDevice", setup: func(*testing.T, string) string { return devNull }, wantErr: appendfile.ErrNotRegularFile},
		{name: "symlinkToRegularFile", setup: makeSymlink, wantErr: appendfile.ErrSymlink},
		{name: "missingParentDir", setup: missingParent, wantErr: fs.ErrNotExist},
	}
	// OpenReadWrite gets the same rows: with O_RDWR a FIFO opens without a
	// reader on Linux, so only the fstat check rejects it.
	opens := []struct {
		name string
		open func(string, os.FileMode) (*os.File, error)
	}{
		{name: "Open", open: appendfile.Open},
		{name: "OpenReadWrite", open: appendfile.OpenReadWrite},
	}
	for _, o := range opens {
		for _, tt := range tests {
			t.Run(o.name+"/"+tt.name, func(t *testing.T) {
				t.Parallel()
				path := tt.setup(t, t.TempDir())

				f, err := o.open(path, permOwnerOnly)
				if err == nil {
					closeFile(t, f)
				}

				if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
					t.Errorf("%s(%q) error mismatch (-want +got):\n%s", o.name, path, diff)
				}
			})
		}
	}
}

func createWithMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	writeFile(t, path, "")
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("Chmod(%q, %v): %v", path, mode, err)
	}
}

func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q): %v", path, err)
	}
	return info.Mode()
}

func closeFile(t *testing.T, f *os.File) {
	t.Helper()
	if err := f.Close(); err != nil {
		t.Errorf("Close(%q): %v", f.Name(), err)
	}
}

func makeDir(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "subdir")
	if err := os.Mkdir(path, permOwnerOnlyDir); err != nil {
		t.Fatalf("Mkdir(%q): %v", path, err)
	}
	return path
}

func makeFIFO(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(path, permOwnerOnly); err != nil {
		t.Fatalf("Mkfifo(%q): %v", path, err)
	}
	return path
}

// makeFIFOWithReader holds a non-blocking reader on a new FIFO for the rest of
// the test, so a writer's open succeeds and only the fstat check can reject it.
func makeFIFOWithReader(t *testing.T, dir string) string {
	t.Helper()
	path := makeFIFO(t, dir)
	r, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatalf("opening reader on %q: %v", path, err)
	}
	t.Cleanup(func() { closeFile(t, r) })
	return path
}

func makeSymlink(t *testing.T, dir string) string {
	t.Helper()
	target := filepath.Join(dir, "target.log")
	writeFile(t, target, "")
	link := filepath.Join(dir, "link.log")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink(%q, %q): %v", target, link, err)
	}
	return link
}

func missingParent(_ *testing.T, dir string) string {
	return filepath.Join(dir, "missing", fileName)
}
