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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/pkg/pipeline"
)

const (
	dlqFileName    = "dlq.jsonl"
	permOwnerRW    = 0o600
	writeFailedMsg = "writing to DLQ failed"
	levelError     = "ERROR"
	stageFetch     = "fetch"
	stageParse     = "parse"
	itemOld        = "item-0"
	itemFirst      = "item-1"
	itemSecond     = "item-2"
)

var (
	recordedAt = time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC)
	errFetch   = errors.New("fetch timed out")
)

// logRecord is the subset of a JSON slog record the tests inspect.
type logRecord struct {
	Level  string `json:"level"`
	Msg    string `json:"msg"`
	ItemID string `json:"itemID"`
}

// recordCall is one Record call; every call uses recordedAt as its timestamp.
type recordCall struct {
	itemID string
	stage  string
	err    error
}

func TestDeadLetterQueue_Record(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		seed []pipeline.DLQEntry
		// closed closes the queue before the calls, so every write fails.
		closed   bool
		calls    []recordCall
		want     []pipeline.DLQEntry
		wantLogs []logRecord
	}{
		{
			name:  "newFile_entryWritten",
			calls: []recordCall{{itemFirst, stageFetch, errFetch}},
			want:  []pipeline.DLQEntry{{Timestamp: recordedAt, ItemID: itemFirst, Stage: stageFetch, Error: errFetch.Error()}},
		},
		{
			name:  "nilError_recordedAsEmpty",
			calls: []recordCall{{itemID: itemFirst, stage: stageParse}},
			want:  []pipeline.DLQEntry{{Timestamp: recordedAt, ItemID: itemFirst, Stage: stageParse}},
		},
		{
			name:  "existingFile_appended",
			seed:  []pipeline.DLQEntry{{Timestamp: recordedAt, ItemID: itemOld, Stage: stageFetch}},
			calls: []recordCall{{itemFirst, stageFetch, errFetch}},
			want: []pipeline.DLQEntry{
				{Timestamp: recordedAt, ItemID: itemOld, Stage: stageFetch},
				{Timestamp: recordedAt, ItemID: itemFirst, Stage: stageFetch, Error: errFetch.Error()},
			},
		},
		{
			name:  "multipleCalls_writtenInOrder",
			calls: []recordCall{{itemSecond, stageParse, errFetch}, {itemFirst, stageFetch, errFetch}},
			want: []pipeline.DLQEntry{
				{Timestamp: recordedAt, ItemID: itemSecond, Stage: stageParse, Error: errFetch.Error()},
				{Timestamp: recordedAt, ItemID: itemFirst, Stage: stageFetch, Error: errFetch.Error()},
			},
		},
		{
			name:     "closedQueue_failureLogged",
			closed:   true,
			calls:    []recordCall{{itemFirst, stageFetch, errFetch}},
			wantLogs: []logRecord{{Level: levelError, Msg: writeFailedMsg, ItemID: itemFirst}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), dlqFileName)
			writeEntries(t, path, tt.seed)
			var logs bytes.Buffer
			dlq := openDLQ(t, path, slog.New(slog.NewJSONHandler(&logs, nil)), tt.closed)

			for _, c := range tt.calls {
				dlq.Record(recordedAt, c.itemID, c.stage, c.err)
			}

			if diff := cmp.Diff(tt.want, decodeLines[pipeline.DLQEntry](t, readFile(t, path))); diff != "" {
				t.Errorf("DLQ entries in %q mismatch (-want +got):\n%s", path, diff)
			}
			if diff := cmp.Diff(tt.wantLogs, decodeLines[logRecord](t, logs.Bytes())); diff != "" {
				t.Errorf("application log records mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDeadLetterQueue_ConcurrentRecord_KeepsLinesIntact(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		writers int
	}{
		{name: "twoWriters", writers: 2},
		{name: "manyWriters", writers: 64},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), dlqFileName)
			dlq := openDLQ(t, path, nil, false)

			want := recordConcurrently(dlq, tt.writers)

			if diff := cmp.Diff(want, sortedItemIDs(t, path)); diff != "" {
				t.Errorf("item IDs from %d concurrent Record calls mismatch (-want +got):\n%s", tt.writers, diff)
			}
		})
	}
}

// openDLQ opens a queue at path. A closed queue is closed before returning;
// otherwise it is closed when the test ends.
func openDLQ(t *testing.T, path string, appLogger *slog.Logger, closed bool) *pipeline.DeadLetterQueue {
	t.Helper()
	dlq, err := pipeline.NewDeadLetterQueue(path, appLogger)
	if err != nil {
		t.Fatalf("NewDeadLetterQueue(%q): %v", path, err)
	}
	closeDLQ := func() {
		if err := dlq.Close(); err != nil {
			t.Errorf("Close() on %q: %v", path, err)
		}
	}
	if closed {
		closeDLQ()
		return dlq
	}
	t.Cleanup(closeDLQ)
	return dlq
}

// recordConcurrently makes n Record calls from n goroutines and returns their
// item IDs in sorted order.
func recordConcurrently(dlq *pipeline.DeadLetterQueue, n int) []string {
	ids := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		ids[i] = fmt.Sprintf("item-%03d", i)
		wg.Go(func() { dlq.Record(recordedAt, ids[i], stageFetch, errFetch) })
	}
	wg.Wait()
	return ids
}

func sortedItemIDs(t *testing.T, path string) []string {
	t.Helper()
	entries := decodeLines[pipeline.DLQEntry](t, readFile(t, path))
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		ids = append(ids, e.ItemID)
	}
	slices.Sort(ids)
	return ids
}

// writeEntries creates path holding entries as JSON lines; with no entries it
// leaves path absent.
func writeEntries(t *testing.T, path string, entries []pipeline.DLQEntry) {
	t.Helper()
	if len(entries) == 0 {
		return
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, e := range entries {
		if err := enc.Encode(e); err != nil {
			t.Fatalf("encoding %v: %v", e, err)
		}
	}
	if err := os.WriteFile(path, buf.Bytes(), permOwnerRW); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	return data
}

// decodeLines decodes every JSON value in data; it returns nil for none.
func decodeLines[T any](t *testing.T, data []byte) []T {
	t.Helper()
	var out []T
	dec := json.NewDecoder(bytes.NewReader(data))
	for {
		var v T
		err := dec.Decode(&v)
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("decoding JSON value %d: %v", len(out)+1, err)
		}
		out = append(out, v)
	}
}

func TestDeadLetterQueueAddReadDLQ(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), dlqFileName)
	dlq, err := pipeline.NewDeadLetterQueue(path, nil)
	if err != nil {
		t.Fatalf("NewDeadLetterQueue() = %v", err)
	}
	want := []pipeline.DLQEntry{
		{Timestamp: recordedAt, ItemID: itemFirst, Stage: stageFetch, Error: errFetch.Error(), Attempts: 3},
		{Timestamp: recordedAt, ItemID: itemSecond, Stage: stageParse, Error: "bad json"},
	}
	for _, e := range want {
		if err := dlq.Add(&e); err != nil {
			t.Fatalf("Add(%+v) = %v", e, err)
		}
	}
	if err := dlq.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })

	got, err := pipeline.ReadDLQ(file)
	if err != nil {
		t.Fatalf("ReadDLQ() = %v", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ReadDLQ() (-want +got):\n%s", diff)
	}
}

func TestReadDLQMalformed(t *testing.T) {
	t.Parallel()
	_, err := pipeline.ReadDLQ(strings.NewReader("{\"item_id\":\"a\"}\nnot json\n"))
	var syntaxErr *json.SyntaxError
	if !errors.As(err, &syntaxErr) {
		t.Errorf("ReadDLQ(malformed) = %v, want a *json.SyntaxError", err)
	}
}

func TestDeadLetterQueueAddClosed(t *testing.T) {
	t.Parallel()
	dlq, err := pipeline.NewDeadLetterQueue(filepath.Join(t.TempDir(), dlqFileName), nil)
	if err != nil {
		t.Fatalf("NewDeadLetterQueue() = %v", err)
	}
	if err := dlq.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if diff := cmp.Diff(os.ErrClosed, dlq.Add(&pipeline.DLQEntry{ItemID: itemFirst}), cmpopts.EquateErrors()); diff != "" {
		t.Errorf("Add after Close error (-want +got):\n%s", diff)
	}
}
