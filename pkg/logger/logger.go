// Package logger provides helpers for creating *slog.Logger instances.
// Callers receive a *slog.Logger directly; no custom interface wraps it.
package logger

import (
"fmt"
"io"
"log/slog"
"os"
"path/filepath"

"gopkg.in/natefinch/lumberjack.v2"
)

// New returns a text-format slog.Logger writing to w at the given minimum level.
func New(w io.Writer, level slog.Level) *slog.Logger {
return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}

// NewNoOp returns a slog.Logger that discards all output.
func NewNoOp() *slog.Logger {
return slog.New(slog.DiscardHandler)
}

const (
defaultLogMaxSizeMB  = 10
defaultLogMaxBackups = 5
defaultLogMaxAgeDays = 28

logDirPerm  = 0o700 // permissions for created log directories
logFilePerm = 0o600 // permissions for created log files
)

// DefaultLogFilePath returns the path used when no --log-file is provided.
// It honours os.UserConfigDir (which respects XDG_CONFIG_HOME on Linux and
// the platform-appropriate location elsewhere) and falls back to the OS temp
// directory when the user config directory cannot be determined.
func DefaultLogFilePath(appName string) string {
fileName := appName + ".log"
if dir, err := os.UserConfigDir(); err == nil && dir != "" {
return filepath.Join(dir, appName, fileName)
}
return filepath.Join(os.TempDir(), appName, fileName)
}

// NewFileLogger returns a slog.Logger that writes JSON-formatted records to
// filename with size-based rotation. It creates the parent directory if it
// does not exist and verifies the file is writable before returning, so
// callers can fail fast on invalid paths.
func NewFileLogger(filename string) (*slog.Logger, error) {
dir := filepath.Dir(filename)
if err := os.MkdirAll(dir, logDirPerm); err != nil {
return nil, fmt.Errorf("creating log directory %q: %w", dir, err)
}

// Verify the file is actually writable by opening (or creating) it.
f, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_APPEND, logFilePerm)
if err != nil {
return nil, fmt.Errorf("opening log file %q: %w", filename, err)
}
if err := f.Close(); err != nil {
return nil, fmt.Errorf("closing log file %q: %w", filename, err)
}

lj := &lumberjack.Logger{
Filename:   filename,
MaxSize:    defaultLogMaxSizeMB,
MaxBackups: defaultLogMaxBackups,
MaxAge:     defaultLogMaxAgeDays,
Compress:   true,
}
return slog.New(slog.NewJSONHandler(lj, &slog.HandlerOptions{Level: slog.LevelInfo})), nil
}
