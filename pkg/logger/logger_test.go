package logger_test

import (
"bytes"
"log/slog"
"strings"
"testing"

"github.com/google/go-cmp/cmp"

"github.com/vmontagh/go-llm-pipeline/pkg/logger"
)

func TestNew_LevelFiltering(t *testing.T) {
t.Parallel()
tests := []struct {
name       string
minLevel   slog.Level
logLevel   slog.Level
logMsg     string
wantOutput bool
}{
{
name:       "debug logged when level is debug",
minLevel:   slog.LevelDebug,
logLevel:   slog.LevelDebug,
logMsg:     "debug-msg",
wantOutput: true,
},
{
name:       "debug suppressed when level is info",
minLevel:   slog.LevelInfo,
logLevel:   slog.LevelDebug,
logMsg:     "suppressed",
wantOutput: false,
},
{
name:       "warn logged when level is info",
minLevel:   slog.LevelInfo,
logLevel:   slog.LevelWarn,
logMsg:     "warn-msg",
wantOutput: true,
},
{
name:       "info logged at info level",
minLevel:   slog.LevelInfo,
logLevel:   slog.LevelInfo,
logMsg:     "info-msg",
wantOutput: true,
},
}

for _, tt := range tests {
t.Run(tt.name, func(t *testing.T) {
t.Parallel()
var buf bytes.Buffer
l := logger.New(&buf, tt.minLevel)

switch tt.logLevel {
case slog.LevelDebug:
l.Debug(tt.logMsg)
case slog.LevelInfo:
l.Info(tt.logMsg)
case slog.LevelWarn:
l.Warn(tt.logMsg)
case slog.LevelError:
l.Error(tt.logMsg)
}

got := buf.String()
if tt.wantOutput {
if !strings.Contains(got, tt.logMsg) {
t.Errorf("New(%v).Log(%v, %q): want output containing %q, got %q", tt.minLevel, tt.logLevel, tt.logMsg, tt.logMsg, got)
}
} else {
if diff := cmp.Diff("", got); diff != "" {
t.Errorf("New(%v).Log(%v, %q): want no output (-want +got):\n%s", tt.minLevel, tt.logLevel, tt.logMsg, diff)
}
}
})
}
}

func TestNewNoOp_DiscardsAllLevels(t *testing.T) {
t.Parallel()
l := logger.NewNoOp()
if l == nil {
t.Fatal("NewNoOp() returned nil")
}
// All levels must be silently swallowed — no panic, no output.
l.Debug("d")
l.Info("i")
l.Warn("w")
l.Error("e")
}

func TestNew_StructuredAttributes(t *testing.T) {
t.Parallel()
var buf bytes.Buffer
l := logger.New(&buf, slog.LevelInfo)
l.Info("hello", "key", "value")
got := buf.String()
if !strings.Contains(got, "key") || !strings.Contains(got, "value") {
t.Errorf("expected structured attribute in output, got %q", got)
}
}
