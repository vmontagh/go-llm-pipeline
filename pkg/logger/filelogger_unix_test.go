//go:build unix

package logger_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/internal/appendfile"
	"github.com/vmontagh/go-llm-pipeline/pkg/logger"
)

const (
	permOwnerOnly    = 0o600
	permOwnerOnlyDir = 0o700
	// rejectTimeout bounds the call; a blocking open of a FIFO would otherwise
	// hang the test forever.
	rejectTimeout = 5 * time.Second
)

func TestNewFileLoggerRejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	if err := syscall.Mkfifo(path, permOwnerOnly); err != nil {
		t.Fatalf("Mkfifo(%q): %v", path, err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := logger.NewFileLogger(path)
		done <- err
	}()

	select {
	case err := <-done:
		if diff := cmp.Diff(appendfile.ErrNotRegularFile, err, cmpopts.EquateErrors()); diff != "" {
			t.Errorf("NewFileLogger(%q) error mismatch (-want +got):\n%s", path, diff)
		}
	case <-time.After(rejectTimeout):
		t.Fatalf("NewFileLogger(%q) did not return within %v", path, rejectTimeout)
	}
}

func TestNewFileLoggerCreatesOwnerOnlyPaths(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "logs")
	path := filepath.Join(dir, logFileName)

	if _, err := logger.NewFileLogger(path); err != nil {
		t.Fatalf("NewFileLogger(%q): %v", path, err)
	}

	want := map[string]os.FileMode{dir: os.ModeDir | permOwnerOnlyDir, path: permOwnerOnly}
	got := map[string]os.FileMode{dir: modeOf(t, dir), path: modeOf(t, path)}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("NewFileLogger(%q) path modes mismatch (-want +got):\n%s", path, diff)
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
