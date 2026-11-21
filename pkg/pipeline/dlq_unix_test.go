//go:build unix

package pipeline_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/internal/appendfile"
	"github.com/vmontagh/go-llm-pipeline/pkg/pipeline"
)

const (
	permOwnerOnlyDir = 0o700
	permGroupReadDir = 0o750
	dlqDirName       = "dlq"
	nestedDirName    = "nested"
)

// TestNewDeadLetterQueue_InvalidPath_Rejected covers the queue's own directory
// step and proves the open goes through appendfile; appendfile's tests cover
// every special-file case. It has no timeout of its own: a regression to a
// blocking open hangs the FIFO row until `go test -timeout` fails the run.
func TestNewDeadLetterQueue_InvalidPath_Rejected(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		setup   func(t *testing.T, dir string) string
		wantErr error
	}{
		{name: "fifo", setup: makeFIFO, wantErr: appendfile.ErrNotRegularFile},
		{name: "parentIsRegularFile", setup: underRegularFile, wantErr: syscall.ENOTDIR},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := tt.setup(t, t.TempDir())

			_, err := pipeline.NewDeadLetterQueue(path, nil)

			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("NewDeadLetterQueue(%q) error mismatch (-want +got):\n%s", path, diff)
			}
		})
	}
}

func TestNewDeadLetterQueue_PathModes(t *testing.T) {
	t.Parallel()
	nestedDir := filepath.Join(dlqDirName, nestedDirName)
	tests := []struct {
		name string
		// existingDirMode, when set, pre-creates dlqDirName with this mode.
		existingDirMode os.FileMode
		dlqDir          string
		want            map[string]os.FileMode
	}{
		{
			name:   "missingDir_createdOwnerOnly",
			dlqDir: dlqDirName,
			want: map[string]os.FileMode{
				dlqDirName:                             os.ModeDir | permOwnerOnlyDir,
				filepath.Join(dlqDirName, dlqFileName): permOwnerRW,
			},
		},
		{
			name:   "nestedMissingDirs_createdOwnerOnly",
			dlqDir: nestedDir,
			want: map[string]os.FileMode{
				dlqDirName:                            os.ModeDir | permOwnerOnlyDir,
				nestedDir:                             os.ModeDir | permOwnerOnlyDir,
				filepath.Join(nestedDir, dlqFileName): permOwnerRW,
			},
		},
		{
			name:            "existingDir_modeKept",
			existingDirMode: permGroupReadDir,
			dlqDir:          dlqDirName,
			want: map[string]os.FileMode{
				dlqDirName:                             os.ModeDir | permGroupReadDir,
				filepath.Join(dlqDirName, dlqFileName): permOwnerRW,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if tt.existingDirMode != 0 {
				mkdirWithMode(t, filepath.Join(root, dlqDirName), tt.existingDirMode)
			}
			path := filepath.Join(root, tt.dlqDir, dlqFileName)

			openDLQ(t, path, nil, false)

			if diff := cmp.Diff(tt.want, modesUnder(t, root, tt.want)); diff != "" {
				t.Errorf("NewDeadLetterQueue(%q) path modes mismatch (-want +got):\n%s", path, diff)
			}
		})
	}
}

func makeFIFO(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, dlqFileName)
	if err := syscall.Mkfifo(path, permOwnerRW); err != nil {
		t.Fatalf("Mkfifo(%q): %v", path, err)
	}
	return path
}

// underRegularFile returns a queue path whose parent is a regular file, so the
// directory cannot be created.
func underRegularFile(t *testing.T, dir string) string {
	t.Helper()
	parent := filepath.Join(dir, dlqDirName)
	if err := os.WriteFile(parent, nil, permOwnerRW); err != nil {
		t.Fatalf("WriteFile(%q): %v", parent, err)
	}
	return filepath.Join(parent, dlqFileName)
}

// mkdirWithMode creates dir with exactly mode, independent of the umask.
func mkdirWithMode(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()
	if err := os.Mkdir(dir, mode); err != nil {
		t.Fatalf("Mkdir(%q): %v", dir, err)
	}
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatalf("Chmod(%q, %v): %v", dir, mode, err)
	}
}

// modesUnder returns the mode of each path in want, relative to root.
func modesUnder(t *testing.T, root string, want map[string]os.FileMode) map[string]os.FileMode {
	t.Helper()
	got := make(map[string]os.FileMode, len(want))
	for rel := range want {
		info, err := os.Stat(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("Stat(%q): %v", rel, err)
		}
		got[rel] = info.Mode()
	}
	return got
}
