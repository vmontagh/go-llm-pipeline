//go:build unix

package logger_test

import (
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
	permOwnerOnly = 0o600
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
