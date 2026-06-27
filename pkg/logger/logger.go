// Package logger provides logging functionality.
package logger

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

// Logger defines the interface for logging
type Logger interface {
	Debugf(format string, v ...any)
	Infof(format string, v ...any)
	Warnf(format string, v ...any)
	Errorf(format string, v ...any)
	EnableDebug()
}

// StandardLogger is the default implementation of Logger.
type StandardLogger struct {
	debugEnabled bool
	outLogger    *log.Logger
	errLogger    *log.Logger
}

var (
	instance Logger
	once     sync.Once
)

// formatMessage creates a consistent, centralized logging format.
func formatMessage(level, format string) string {
	now := time.Now().Format("2006/01/02 15:04:05")
	_, file, line, ok := runtime.Caller(2)
	caller := ""
	if ok {
		caller = filepath.Base(file) + ":" + strconv.Itoa(line)
	}
	return fmt.Sprintf("%-5s %s %s: %s", level, now, caller, format)
}

// EnableDebug turns on debug logging
func (l *StandardLogger) EnableDebug() {
	l.debugEnabled = true
}

// Debugf logs a debug message
func (l *StandardLogger) Debugf(format string, v ...any) {
	if l.debugEnabled {
		l.outLogger.Printf(formatMessage("DEBUG", format), v...)
	}
}

// Infof logs an info message
func (l *StandardLogger) Infof(format string, v ...any) {
	l.outLogger.Printf(formatMessage("INFO", format), v...)
}

// Warnf logs a warning message
func (l *StandardLogger) Warnf(format string, v ...any) {
	l.outLogger.Printf(formatMessage("WARN", format), v...)
}

// Errorf logs an error message
func (l *StandardLogger) Errorf(format string, v ...any) {
	l.errLogger.Printf(formatMessage("ERROR", format), v...)
}

// Default returns the singleton instance of Logger
func Default() Logger {
	once.Do(func() {
		instance = &StandardLogger{
			debugEnabled: false,
			outLogger:    log.New(os.Stdout, "", 0),
			errLogger:    log.New(os.Stderr, "", 0),
		}
	})
	return instance
}

// NewWithWriter returns a new Logger that writes all logs to the given io.Writer.
func NewWithWriter(w io.Writer) Logger {
	return &StandardLogger{
		debugEnabled: false,
		outLogger:    log.New(w, "", 0),
		errLogger:    log.New(w, "", 0),
	}
}

const (
	defaultLogMaxSizeMB  = 10
	defaultLogMaxBackups = 5
	defaultLogMaxAgeDays = 28

	logDirPerm  = 0o700 // permissions for created log directories
	logFilePerm = 0o600 // permissions for created log files
)

// DefaultLogFilePath returns the path used when no --log-file is provided.
// It honors os.UserConfigDir (which respects XDG_CONFIG_HOME on Linux and
// the platform-appropriate location elsewhere) and falls back to the OS
// temp directory when the user config directory cannot be determined.
func DefaultLogFilePath(appName string) string {
	fileName := appName + ".log"
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		return filepath.Join(dir, appName, fileName)
	}
	return filepath.Join(os.TempDir(), appName, fileName)
}

// NewFileLogger returns a new Logger that writes to the specified file with size-based
// rotation. It creates the parent directory if it does not exist and verifies the file
// is writable before returning, so callers can fail fast on invalid paths.
func NewFileLogger(filename string) (Logger, error) {
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

	lumberjackLogger := &lumberjack.Logger{
		Filename:   filename,
		MaxSize:    defaultLogMaxSizeMB,
		MaxBackups: defaultLogMaxBackups,
		MaxAge:     defaultLogMaxAgeDays,
		Compress:   true,
	}
	return NewWithWriter(lumberjackLogger), nil
}

// NoOpLogger is a logger that discards all log messages.
type NoOpLogger struct{}

// NewNoOpLogger returns a new NoOpLogger.
func NewNoOpLogger() Logger {
	return &NoOpLogger{}
}

// Debugf implements Logger.Debugf.
func (l *NoOpLogger) Debugf(format string, _ ...any) { _ = format }

// Infof implements Logger.Infof.
func (l *NoOpLogger) Infof(format string, _ ...any) { _ = format }

// Warnf implements Logger.Warnf.
func (l *NoOpLogger) Warnf(format string, _ ...any) { _ = format }

// Errorf implements Logger.Errorf.
func (l *NoOpLogger) Errorf(format string, _ ...any) { _ = format }

// EnableDebug implements Logger.EnableDebug.
func (l *NoOpLogger) EnableDebug() {}
