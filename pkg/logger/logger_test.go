package logger

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

const (
	levelINFO      = "INFO"
	levelWARN      = "WARN"
	levelERROR     = "ERROR"
	levelDEBUG     = "DEBUG"
	testLogMessage = "hello from test"
)

func TestLogger(t *testing.T) {
	tests := []struct {
		name          string
		level         string
		debugEnabled  bool
		message       string
		args          []any
		expectOut     string
		expectErr     string
		expectNoMatch bool // True if we expect no output
	}{
		{
			name:         "Infof standard log",
			level:        levelINFO,
			debugEnabled: false,
			message:      "test message %d",
			args:         []any{123},
			expectOut:    levelINFO,
		},
		{
			name:         "Warnf standard log",
			level:        levelWARN,
			debugEnabled: false,
			message:      "warning here",
			args:         nil,
			expectOut:    levelWARN,
		},
		{
			name:         "Errorf standard log",
			level:        levelERROR,
			debugEnabled: false,
			message:      "error occurred: %v",
			args:         []any{"bad things"},
			expectErr:    levelERROR,
		},
		{
			name:          "Debugf ignored when disabled",
			level:         levelDEBUG,
			debugEnabled:  false,
			message:       "secret debug",
			args:          nil,
			expectNoMatch: true,
		},
		{
			name:         "Debugf printed when enabled",
			level:        levelDEBUG,
			debugEnabled: true,
			message:      "secret debug printed",
			args:         nil,
			expectOut:    levelDEBUG,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var outBuf, errBuf bytes.Buffer

			l := &StandardLogger{
				debugEnabled: tt.debugEnabled,
				outLogger:    log.New(&outBuf, "", 0),
				errLogger:    log.New(&errBuf, "", 0),
			}

			// Call the appropriate method
			switch tt.level {
			case levelINFO:
				l.Infof(tt.message, tt.args...)
			case levelWARN:
				l.Warnf(tt.message, tt.args...)
			case levelERROR:
				l.Errorf(tt.message, tt.args...)
			case levelDEBUG:
				l.Debugf(tt.message, tt.args...)
			}

			outStr := outBuf.String()
			errStr := errBuf.String()

			if tt.expectNoMatch {
				if diff := cmp.Diff("", outStr); diff != "" {
					t.Errorf("want no stdout, got diff:\n%s", diff)
				}
				if diff := cmp.Diff("", errStr); diff != "" {
					t.Errorf("want no stderr, got diff:\n%s", diff)
				}
				return
			}

			// Check stdout
			if tt.expectOut != "" {
				if !strings.Contains(outStr, tt.expectOut) {
					t.Errorf("stdout missing want level %q. Got: %q", tt.expectOut, outStr)
				}
				if len(tt.args) > 0 && tt.level == levelINFO {
					if !strings.Contains(outStr, "test message 123") {
						t.Errorf("stdout missing formatted message. Got: %q", outStr)
					}
				}
			}

			// Check stderr
			if tt.expectErr != "" {
				if !strings.Contains(errStr, tt.expectErr) {
					t.Errorf("stderr missing want level %q. Got: %q", tt.expectErr, errStr)
				}
			}
		})
	}
}

func TestNoOpLogger(_ *testing.T) {
	l := &NoOpLogger{}
	l.Debugf("test")
	l.Infof("test")
	l.Warnf("test")
	l.Errorf("test")
}

func TestLoggerDefaultAndEnableDebug(t *testing.T) {
	l := Default()
	if l == nil {
		t.Fatal("Default() returned nil")
	}
	sl, ok := l.(*StandardLogger)
	if !ok {
		t.Fatalf("Default() returned %T, want *StandardLogger", l)
	}
	if sl.debugEnabled {
		t.Fatal("want debug to be disabled initially")
	}
	l.EnableDebug()
	if !sl.debugEnabled {
		t.Fatal("want debug to be enabled after EnableDebug()")
	}
}

func TestLoggerCallerSiteAttribution(t *testing.T) {
	var outBuf bytes.Buffer
	l := &StandardLogger{
		outLogger: log.New(&outBuf, "", 0),
		errLogger: log.New(&outBuf, "", 0),
	}
	l.Infof("hello")
	if !strings.Contains(outBuf.String(), "logger_test.go:") {
		t.Errorf("want caller to be attributed to logger_test.go, got: %q", outBuf.String())
	}
}

func TestNewFileLogger(t *testing.T) {
	tests := []struct {
		name    string
		path    func(dir string) string
		wantErr error
		wantMsg string
	}{
		{
			name:    "valid path creates logger and writes",
			path:    func(dir string) string { return filepath.Join(dir, "test.log") },
			wantMsg: testLogMessage,
		},
		{
			name:    "auto-creates parent directory and writes",
			path:    func(dir string) string { return filepath.Join(dir, "sub", "dir", "test.log") },
			wantMsg: testLogMessage,
		},
		{
			name:    "invalid path returns error",
			path:    func(_ string) string { return "/dev/null/impossible/test.log" },
			wantErr: cmpopts.AnyError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logFile := tt.path(t.TempDir())

			l, err := NewFileLogger(logFile)
			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("NewFileLogger() error mismatch (-want +got):\n%s", diff)
			}
			if err != nil {
				return
			}

			if tt.wantMsg != "" {
				l.Infof("%s", tt.wantMsg)
				data, readErr := os.ReadFile(logFile)
				if readErr != nil {
					t.Fatalf("reading log file: %v", readErr)
				}
				if diff := cmp.Diff(true, strings.Contains(string(data), tt.wantMsg)); diff != "" {
					t.Errorf("log file content mismatch (-want +got):\n%s\ngot: %q", diff, string(data))
				}
			}
		})
	}
}

func TestDefaultLogFilePath(t *testing.T) {
	appName := "flickr-organizer"
	defaultLogFileName := appName + ".log"
	wantSuffix := filepath.Join(appName, defaultLogFileName)

	xdgBase := t.TempDir()
	homeBase := t.TempDir()

	tests := []struct {
		name      string
		xdgConfig string // value for XDG_CONFIG_HOME
		appData   string // value for APPDATA
		home      string // value for HOME
	}{
		{
			name:      "uses user config dir when available",
			xdgConfig: xdgBase,
			appData:   t.TempDir(),
			home:      t.TempDir(),
		},
		{
			name: "uses HOME when available and XDG_CONFIG_HOME is unset",
			home: homeBase,
		},
		{
			name: "falls back to os.TempDir when config dir is unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", tt.xdgConfig)
			t.Setenv("APPDATA", tt.appData)
			t.Setenv("HOME", tt.home)

			got := DefaultLogFilePath(appName)
			baseDir, err := os.UserConfigDir()
			if err != nil || baseDir == "" {
				baseDir = os.TempDir()
			}
			want := filepath.Join(baseDir, appName, defaultLogFileName)

			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("DefaultLogFilePath() mismatch (-want +got):\n%s", diff)
			}

			if !strings.HasSuffix(got, wantSuffix) {
				t.Errorf("DefaultLogFilePath() = %q, want suffix %q", got, wantSuffix)
			}
			if !filepath.IsAbs(got) {
				t.Errorf("DefaultLogFilePath() = %q, want absolute path", got)
			}
		})
	}
}
