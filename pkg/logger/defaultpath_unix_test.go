//go:build unix

package logger_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vmontagh/go-llm-pipeline/pkg/logger"
)

const appName = "photoapp"

// TestDefaultLogFilePath sets HOME and XDG_CONFIG_HOME, which os.UserConfigDir
// reads on every Unix, so it cannot run in parallel.
func TestDefaultLogFilePath(t *testing.T) {
	wantFile := filepath.Join(appName, appName+".log")

	t.Run("under user config dir", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

		got := logger.DefaultLogFilePath(appName)

		if !strings.HasPrefix(got, home+string(filepath.Separator)) || !strings.HasSuffix(got, wantFile) {
			t.Errorf("DefaultLogFilePath(%q) = %q, want %q under %q", appName, got, wantFile, home)
		}
		if _, err := logger.NewFileLogger(got); err != nil {
			t.Errorf("NewFileLogger(%q) = %v, want nil", got, err)
		}
	})

	t.Run("falls back to temp dir without config dir", func(t *testing.T) {
		t.Setenv("HOME", "")
		t.Setenv("XDG_CONFIG_HOME", "")

		got := logger.DefaultLogFilePath(appName)

		if want := filepath.Join(os.TempDir(), wantFile); got != want {
			t.Errorf("DefaultLogFilePath(%q) with no config dir = %q, want %q", appName, got, want)
		}
	})
}
