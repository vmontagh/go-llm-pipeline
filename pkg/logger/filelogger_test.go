package logger_test

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/vmontagh/go-llm-pipeline/pkg/logger"
)

const (
	logFileName   = "app.log"
	permWritable  = 0o600
	previousLine  = "line from a previous run"
	savedMsg      = "photo saved"
	debugOnlyMsg  = "debug detail"
	savedItemID   = "photo-1"
	itemIDLogAttr = "itemID"
)

// logRecord is the subset of a JSON slog record the tests inspect.
type logRecord struct {
	Level  string `json:"level"`
	Msg    string `json:"msg"`
	ItemID string `json:"itemID"`
}

func TestNewFileLoggerAppendsJSONRecordsAtInfo(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), logFileName)
	if err := os.WriteFile(path, []byte(previousLine+"\n"), permWritable); err != nil {
		t.Fatalf("seeding %q: %v", path, err)
	}

	log, err := logger.NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger(%q): %v", path, err)
	}
	log.Debug(debugOnlyMsg, itemIDLogAttr, savedItemID)
	log.Info(savedMsg, itemIDLogAttr, savedItemID)

	lines := readLines(t, path)
	if len(lines) != 2 {
		t.Fatalf("%q has %d lines, want the seeded line plus one record:\n%s", path, len(lines), strings.Join(lines, "\n"))
	}
	if lines[0] != previousLine {
		t.Errorf("first line of %q = %q, want the seeded %q", path, lines[0], previousLine)
	}
	var got logRecord
	if err := json.Unmarshal([]byte(lines[1]), &got); err != nil {
		t.Fatalf("decoding record %q: %v", lines[1], err)
	}
	want := logRecord{Level: slog.LevelInfo.String(), Msg: savedMsg, ItemID: savedItemID}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("record written to %q mismatch (-want +got):\n%s", path, diff)
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %q: %v", path, err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}
