package pipeline_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/vmontagh/go-llm-pipeline/pkg/pipeline"
)

const (
	dlqFileName = "dlq.jsonl"
	// concurrentRecords is enough writers to interleave if Record were not
	// serialized.
	concurrentRecords = 64
	writeFailedMsg    = "writing to DLQ failed"
)

var (
	recordedAt = time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC)
	errFetch   = errors.New("fetch timed out")
)

func TestDeadLetterQueueAppendsEntriesAcrossReopen(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nested", "dir", dlqFileName)

	first := openDLQ(t, path, nil)
	first.Record(recordedAt, "item-1", "fetch", errFetch)
	first.Record(recordedAt.Add(time.Second), "item-2", "parse", nil)
	closeDLQ(t, first)

	second := openDLQ(t, path, nil)
	second.Record(recordedAt.Add(2*time.Second), "item-3", "store", errFetch)
	closeDLQ(t, second)

	want := []pipeline.DLQEntry{
		{Timestamp: recordedAt, ItemID: "item-1", Stage: "fetch", Error: errFetch.Error()},
		{Timestamp: recordedAt.Add(time.Second), ItemID: "item-2", Stage: "parse"},
		{Timestamp: recordedAt.Add(2 * time.Second), ItemID: "item-3", Stage: "store", Error: errFetch.Error()},
	}
	if diff := cmp.Diff(want, readEntries(t, path)); diff != "" {
		t.Errorf("DLQ entries at %q mismatch (-want +got):\n%s", path, diff)
	}
}

func TestDeadLetterQueueConcurrentRecordKeepsLinesIntact(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), dlqFileName)
	dlq := openDLQ(t, path, nil)

	want := recordConcurrently(dlq, concurrentRecords)
	closeDLQ(t, dlq)

	got := make([]string, 0, concurrentRecords)
	for _, e := range readEntries(t, path) {
		got = append(got, e.ItemID)
	}
	slices.Sort(got)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("item IDs recorded by %d concurrent writers mismatch (-want +got):\n%s", concurrentRecords, diff)
	}
}

func TestDeadLetterQueueRecordAfterCloseLogsError(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	appLogger := slog.New(slog.NewJSONHandler(&logs, nil))
	dlq := openDLQ(t, filepath.Join(t.TempDir(), dlqFileName), appLogger)
	closeDLQ(t, dlq)

	dlq.Record(recordedAt, "item-1", "fetch", errFetch)

	var got logRecord
	if err := json.Unmarshal(logs.Bytes(), &got); err != nil {
		t.Fatalf("decoding log record %q: %v", logs.String(), err)
	}
	want := logRecord{Level: slog.LevelError.String(), Msg: writeFailedMsg, ItemID: "item-1"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("log record for a failed DLQ write mismatch (-want +got):\n%s", diff)
	}
}

// logRecord is the subset of a JSON slog record the tests inspect.
type logRecord struct {
	Level  string `json:"level"`
	Msg    string `json:"msg"`
	ItemID string `json:"itemID"`
}

func openDLQ(t *testing.T, path string, appLogger *slog.Logger) *pipeline.DeadLetterQueue {
	t.Helper()
	dlq, err := pipeline.NewDeadLetterQueue(path, appLogger)
	if err != nil {
		t.Fatalf("NewDeadLetterQueue(%q): %v", path, err)
	}
	return dlq
}

func closeDLQ(t *testing.T, dlq *pipeline.DeadLetterQueue) {
	t.Helper()
	if err := dlq.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
}

// recordConcurrently records n entries from n goroutines and returns their
// item IDs in sorted order.
func recordConcurrently(dlq *pipeline.DeadLetterQueue, n int) []string {
	ids := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		ids[i] = fmt.Sprintf("item-%03d", i)
		wg.Go(func() { dlq.Record(recordedAt, ids[i], "fetch", errFetch) })
	}
	wg.Wait()
	return ids
}

// readEntries decodes every JSON line in the DLQ file at path.
func readEntries(t *testing.T, path string) []pipeline.DLQEntry {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening %q: %v", path, err)
	}
	defer f.Close()

	var entries []pipeline.DLQEntry
	dec := json.NewDecoder(f)
	for {
		var e pipeline.DLQEntry
		err := dec.Decode(&e)
		if errors.Is(err, io.EOF) {
			return entries
		}
		if err != nil {
			t.Fatalf("decoding entry %d of %q: %v", len(entries)+1, path, err)
		}
		entries = append(entries, e)
	}
}
