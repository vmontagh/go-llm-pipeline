package logger_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/vmontagh/go-llm-pipeline/pkg/logger"
)

const (
	logFileName  = "app.log"
	permOwnerRW  = 0o600
	previousMsg  = "from a previous run"
	savedMsg     = "photo saved"
	debugMsg     = "debug detail"
	warnMsg      = "slow response"
	errorMsg     = "upload failed"
	levelInfo    = "INFO"
	levelWarning = "WARN"
	levelError   = "ERROR"
)

// logRecord is the subset of a JSON slog record the tests inspect.
type logRecord struct {
	Level string `json:"level"`
	Msg   string `json:"msg"`
}

// logCall is one call made through the logger under test.
type logCall struct {
	level slog.Level
	msg   string
}

func TestNewFileLogger_WritesRecords(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		seed  []logRecord
		calls []logCall
		want  []logRecord
	}{
		{
			name:  "newFile_infoWritten",
			calls: []logCall{{slog.LevelInfo, savedMsg}},
			want:  []logRecord{{levelInfo, savedMsg}},
		},
		{
			name:  "existingFile_appended",
			seed:  []logRecord{{levelInfo, previousMsg}},
			calls: []logCall{{slog.LevelInfo, savedMsg}},
			want:  []logRecord{{levelInfo, previousMsg}, {levelInfo, savedMsg}},
		},
		{
			name:  "debugBelowDefaultLevel_dropped",
			calls: []logCall{{slog.LevelDebug, debugMsg}},
		},
		{
			name:  "warnAndError_writtenInOrder",
			calls: []logCall{{slog.LevelWarn, warnMsg}, {slog.LevelError, errorMsg}},
			want:  []logRecord{{levelWarning, warnMsg}, {levelError, errorMsg}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), logFileName)
			writeRecords(t, path, tt.seed)

			fileLogger, err := logger.NewFileLogger(path)
			if err != nil {
				t.Fatalf("NewFileLogger(%q): %v", path, err)
			}
			for _, c := range tt.calls {
				fileLogger.Log(t.Context(), c.level, c.msg)
			}

			if diff := cmp.Diff(tt.want, readRecords(t, path)); diff != "" {
				t.Errorf("records in %q after %v mismatch (-want +got):\n%s", path, tt.calls, diff)
			}
		})
	}
}

// writeRecords creates path holding records as JSON lines; with no records it
// leaves path absent.
func writeRecords(t *testing.T, path string, records []logRecord) {
	t.Helper()
	if len(records) == 0 {
		return
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, r := range records {
		if err := enc.Encode(r); err != nil {
			t.Fatalf("encoding %v: %v", r, err)
		}
	}
	if err := os.WriteFile(path, buf.Bytes(), permOwnerRW); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}

// readRecords decodes every JSON line in path; it returns nil for no lines.
func readRecords(t *testing.T, path string) []logRecord {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	var records []logRecord
	dec := json.NewDecoder(bytes.NewReader(data))
	for {
		var r logRecord
		err := dec.Decode(&r)
		if errors.Is(err, io.EOF) {
			return records
		}
		if err != nil {
			t.Fatalf("decoding record %d of %q: %v", len(records)+1, path, err)
		}
		records = append(records, r)
	}
}
