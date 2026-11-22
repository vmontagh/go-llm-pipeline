package pipeline_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/internal/appendfile"
	"github.com/vmontagh/go-llm-pipeline/pkg/pipeline"
)

const journalName = "run/checkpoint.jsonl"

var (
	errProcess = errors.New("process failed")
	errMark    = errors.New("disk full")
)

// openCheckpoint opens the journal at path and closes it on cleanup.
func openCheckpoint(t *testing.T, path string, opts ...pipeline.CheckpointOption) *pipeline.FileCheckpoint {
	t.Helper()
	cp, err := pipeline.OpenFileCheckpoint(path, opts...)
	if err != nil {
		t.Fatalf("OpenFileCheckpoint(%q) = %v", path, err)
	}
	t.Cleanup(func() {
		if err := cp.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Errorf("Close() = %v", err)
		}
	})
	return cp
}

// markDone marks every id, failing the test on error.
func markDone(t *testing.T, cp pipeline.Checkpoint, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if err := cp.MarkDone(id); err != nil {
			t.Fatalf("MarkDone(%q) = %v", id, err)
		}
	}
}

// doneOf returns which of ids cp reports as done.
func doneOf(cp pipeline.Checkpoint, ids ...string) map[string]bool {
	got := make(map[string]bool, len(ids))
	for _, id := range ids {
		got[id] = cp.Done(id)
	}
	return got
}

// writeJournal writes raw journal content at path.
func writeJournal(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), permOwnerRW); err != nil {
		t.Fatal(err)
	}
}

func TestFileCheckpointResume(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), journalName)
	first := openCheckpoint(t, path, pipeline.WithSyncEvery(2))
	markDone(t, first, "a", "b", "a")
	if err := first.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	resumed := openCheckpoint(t, path)

	want := map[string]bool{"a": true, "b": true, "c": false}
	if diff := cmp.Diff(want, doneOf(resumed, "a", "b", "c")); diff != "" {
		t.Errorf("Done after reopen (-want +got):\n%s", diff)
	}
	if got := resumed.Len(); got != 2 {
		t.Errorf("Len() = %d, want 2", got)
	}
	journal, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("{\"id\":\"a\"}\n{\"id\":\"b\"}\n", string(journal)); diff != "" {
		t.Errorf("journal (-want +got):\n%s", diff)
	}
}

func TestOpenFileCheckpointJournal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		content  string
		wantDone []string
		wantErr  error
	}{
		{name: "empty"},
		{name: "records and blank lines", content: "{\"id\":\"a\"}\n\n{\"id\":\"b\"}\n", wantDone: []string{"a", "b"}},
		{name: "partial last record", content: "{\"id\":\"a\"}\n{\"id\":", wantDone: []string{"a"}},
		{name: "only a partial record", content: "{\"id\":\"a\""},
		{name: "corrupt middle record", content: "oops\n{\"id\":\"a\"}\n", wantErr: pipeline.ErrCorruptCheckpoint},
		{name: "record without id", content: "{}\n", wantErr: pipeline.ErrCorruptCheckpoint},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), journalName)
			writeJournal(t, path, tt.content)

			cp, err := pipeline.OpenFileCheckpoint(path)
			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("OpenFileCheckpoint() error (-want +got):\n%s", diff)
			}
			if err != nil {
				return
			}
			// A record appended after recovery must survive another reopen.
			markDone(t, cp, "new")
			if err := cp.Close(); err != nil {
				t.Fatalf("Close() = %v", err)
			}
			got := openCheckpoint(t, path)

			want := append([]string{"new"}, tt.wantDone...)
			if diff := cmp.Diff(len(want), got.Len()); diff != "" {
				t.Errorf("Len() after reopen (-want +got):\n%s", diff)
			}
			for _, id := range want {
				if !got.Done(id) {
					t.Errorf("Done(%q) = false after reopen, want true", id)
				}
			}
		})
	}
}

func TestFileCheckpointErrors(t *testing.T) {
	t.Parallel()
	t.Run("directory path", func(t *testing.T) {
		t.Parallel()
		_, err := pipeline.OpenFileCheckpoint(t.TempDir())
		if diff := cmp.Diff(appendfile.ErrNotRegularFile, err, cmpopts.EquateErrors()); diff != "" {
			t.Errorf("OpenFileCheckpoint(dir) error (-want +got):\n%s", diff)
		}
	})
	t.Run("empty id", func(t *testing.T) {
		t.Parallel()
		cp := openCheckpoint(t, filepath.Join(t.TempDir(), journalName))
		if diff := cmp.Diff(pipeline.ErrEmptyCheckpointID, cp.MarkDone(""), cmpopts.EquateErrors()); diff != "" {
			t.Errorf("MarkDone(\"\") error (-want +got):\n%s", diff)
		}
	})
	t.Run("closed", func(t *testing.T) {
		t.Parallel()
		cp := openCheckpoint(t, filepath.Join(t.TempDir(), journalName))
		if err := cp.Close(); err != nil {
			t.Fatalf("Close() = %v", err)
		}
		if diff := cmp.Diff(os.ErrClosed, cp.MarkDone("a"), cmpopts.EquateErrors()); diff != "" {
			t.Errorf("MarkDone after Close error (-want +got):\n%s", diff)
		}
		if cp.Done("a") {
			t.Error("Done(\"a\") = true after a failed MarkDone, want false")
		}
	})
}

// fakeCheckpoint is an in-memory Checkpoint whose MarkDone can fail.
type fakeCheckpoint struct {
	mu      sync.Mutex
	done    map[string]bool
	markErr error
}

func (f *fakeCheckpoint) Done(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.done[id]
}

func (f *fakeCheckpoint) MarkDone(id string) error {
	if f.markErr != nil {
		return f.markErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.done[id] = true
	return nil
}

func TestCheckpointed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		alreadyDone bool
		processErr  error
		markErr     error
		wantCalls   int
		wantDone    bool
		wantErr     error
	}{
		{name: "already done is skipped", alreadyDone: true, wantDone: true},
		{name: "success is marked", wantCalls: 1, wantDone: true},
		{name: "failure is reported and not marked", processErr: errProcess, wantCalls: 1, wantErr: errProcess},
		{name: "checkpoint failure is reported", markErr: errMark, wantCalls: 1, wantErr: errMark},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			const item = 7
			cp := &fakeCheckpoint{done: map[string]bool{strconv.Itoa(item): tt.alreadyDone}, markErr: tt.markErr}
			var calls int
			var gotErr error
			process := pipeline.Checkpointed(cp, strconv.Itoa,
				func(context.Context, int) error {
					calls++
					return tt.processErr
				},
				func(_ int, err error) { gotErr = err })

			process(t.Context(), item)

			if calls != tt.wantCalls {
				t.Errorf("process calls = %d, want %d", calls, tt.wantCalls)
			}
			if got := cp.Done(strconv.Itoa(item)); got != tt.wantDone {
				t.Errorf("Done(%d) = %t, want %t", item, got, tt.wantDone)
			}
			if diff := cmp.Diff(tt.wantErr, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("onError (-want +got):\n%s", diff)
			}
		})
	}
}

// consumeAll runs process over items with Consume and waits for it.
func consumeAll(t *testing.T, items []int, process func(context.Context, int)) {
	t.Helper()
	input := make(chan int)
	var wg sync.WaitGroup
	pipeline.Consume(t.Context(), input, 4, &wg, process)
	for _, item := range items {
		input <- item
	}
	close(input)
	wg.Wait()
}

func TestCheckpointedResumesConsume(t *testing.T) {
	t.Parallel()
	const total = 50
	items := make([]int, total)
	for i := range items {
		items[i] = i
	}
	path := filepath.Join(t.TempDir(), journalName)
	var calls atomic.Int64
	count := func(_ context.Context, n int) error {
		calls.Add(1)
		if n%2 == 1 {
			return errProcess
		}
		return nil
	}
	first := openCheckpoint(t, path)
	consumeAll(t, items, pipeline.Checkpointed(first, strconv.Itoa, count, nil))
	if err := first.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	resumed := openCheckpoint(t, path)
	consumeAll(t, items, pipeline.Checkpointed(resumed, strconv.Itoa, func(context.Context, int) error {
		calls.Add(1)
		return nil
	}, nil))

	// The first run tries all 50 and completes the 25 even items; the resumed
	// run retries only the 25 odd ones.
	if got, want := calls.Load(), int64(total+total/2); got != want {
		t.Errorf("process calls across both runs = %d, want %d", got, want)
	}
	if got := resumed.Len(); got != total {
		t.Errorf("Len() after resume = %d, want %d", got, total)
	}
}
