package pipeline_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/internal/appendfile"
	"github.com/vmontagh/go-llm-pipeline/pkg/pipeline"
)

const (
	journalName = "run/checkpoint.jsonl"
	dirPerm     = 0o700
)

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

// markAll marks ids through Checkpointed with the given number of workers.
func markAll(t *testing.T, cp pipeline.Checkpoint, ids []string, workers int) {
	t.Helper()
	process := pipeline.Checkpointed(cp, func(id string) string { return id },
		func(context.Context, string) error { return nil },
		func(id string, err error) { t.Errorf("item %q: %v", id, err) })
	if err := pipeline.ConsumeAll(t.Context(), ids, workers, process); err != nil {
		t.Fatalf("ConsumeAll() = %v", err)
	}
}

// doneOf returns the ids cp reports as done.
func doneOf(cp pipeline.Checkpoint, ids ...string) []string {
	var done []string
	for _, id := range ids {
		if cp.Done(id) {
			done = append(done, id)
		}
	}
	return done
}

// reopen closes cp and opens its journal at path again.
func reopen(t *testing.T, cp *pipeline.FileCheckpoint, path string) *pipeline.FileCheckpoint {
	t.Helper()
	if err := cp.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	return openCheckpoint(t, path)
}

// writeJournal writes raw journal content at path.
func writeJournal(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), permOwnerRW); err != nil {
		t.Fatal(err)
	}
}

func TestFileCheckpointReopen(t *testing.T) {
	t.Parallel()
	const manyItems = 50
	many := make([]string, 0, manyItems)
	for i := range manyItems {
		many = append(many, strconv.Itoa(i))
	}
	slices.Sort(many)
	tests := []struct {
		name     string
		journal  string // existing content; empty means no file yet
		opts     []pipeline.CheckpointOption
		mark     []string
		workers  int
		wantDone []string
	}{
		{name: "newJournal", mark: []string{"a", "b", "a"}, wantDone: []string{"a", "b"}},
		{name: "existingRecords", journal: "{\"id\":\"a\"}\n\n{\"id\":\"b\"}\n", mark: []string{"c"}, wantDone: []string{"a", "b", "c"}},
		{name: "partialLastRecordDropped", journal: "{\"id\":\"a\"}\n{\"id\":", mark: []string{"c"}, wantDone: []string{"a", "c"}},
		{name: "onlyPartialRecord", journal: "{\"id\":\"a\"", mark: []string{"c"}, wantDone: []string{"c"}},
		{name: "nilOptionIgnored", opts: []pipeline.CheckpointOption{nil}, mark: []string{"a"}, wantDone: []string{"a"}},
		{name: "concurrentWorkersWithDuplicates", mark: slices.Concat(many, many), workers: 8, wantDone: many},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), journalName)
			if tt.journal != "" {
				writeJournal(t, path, tt.journal)
			}
			cp := openCheckpoint(t, path, tt.opts...)
			markAll(t, cp, tt.mark, tt.workers)

			got := reopen(t, cp, path)

			ids := slices.Sorted(slices.Values(slices.Concat([]string{"a", "b", "c"}, tt.mark)))
			if diff := cmp.Diff(tt.wantDone, doneOf(got, slices.Compact(ids)...)); diff != "" {
				t.Errorf("Done after reopen (-want +got):\n%s", diff)
			}
			if got.Len() != len(tt.wantDone) {
				t.Errorf("Len() = %d, want %d", got.Len(), len(tt.wantDone))
			}
		})
	}
}

func TestOpenFileCheckpoint(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		journal        string
		noJournal      bool // the journal's directory does not exist yet
		pathIsDir      bool
		alreadyOpen    bool
		unreadableRoot bool // the temp dir above the journal's directory
		wantErr        error
	}{
		{name: "unreadableAncestorSkipped", unreadableRoot: true},
		{name: "createsDirectoryInUnreadable", noJournal: true, unreadableRoot: true, wantErr: fs.ErrPermission},
		{name: "corruptMiddleRecord", journal: "oops\n{\"id\":\"a\"}\n", wantErr: pipeline.ErrCorruptCheckpoint},
		{name: "recordWithoutID", journal: "{}\n", wantErr: pipeline.ErrCorruptCheckpoint},
		{name: "invalidUTF8ID", journal: "{\"id\":\"asset-\xff\"}\n", wantErr: pipeline.ErrCorruptCheckpoint},
		{name: "pathIsDirectory", pathIsDir: true, wantErr: appendfile.ErrNotRegularFile},
		{name: "alreadyOpen", alreadyOpen: true, wantErr: pipeline.ErrCheckpointInUse},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			path := filepath.Join(root, journalName)
			switch {
			case tt.pathIsDir:
				path = root
			case tt.alreadyOpen:
				openCheckpoint(t, path)
			case !tt.noJournal:
				writeJournal(t, path, tt.journal)
			}
			if tt.unreadableRoot {
				makeUnreadable(t, root)
			}

			cp, err := pipeline.OpenFileCheckpoint(path)

			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("OpenFileCheckpoint() error (-want +got):\n%s", diff)
			}
			if err == nil {
				if err := cp.Close(); err != nil {
					t.Errorf("Close() = %v", err)
				}
			}
		})
	}
}

// makeUnreadable removes read access to dir, skipping the test where that
// is not enforced (Windows, root).
func makeUnreadable(t *testing.T, dir string) {
	t.Helper()
	const permTraverseOnly = 0o311
	if err := os.Chmod(dir, permTraverseOnly); err != nil {
		t.Fatalf("Chmod() = %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, dirPerm); err != nil {
			t.Errorf("restoring permissions: %v", err)
		}
	})
	f, err := os.Open(dir)
	if err != nil {
		return
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	t.Skip("directory read permission is not enforced")
}

func TestFileCheckpointMarkDoneErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		id      string
		closed  bool
		wantErr error
	}{
		{name: "emptyID", wantErr: pipeline.ErrInvalidCheckpointID},
		{name: "invalidUTF8ID", id: "asset-\xff", wantErr: pipeline.ErrInvalidCheckpointID},
		{name: "closed", id: "a", closed: true, wantErr: os.ErrClosed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cp := openCheckpoint(t, filepath.Join(t.TempDir(), journalName))
			if tt.closed {
				if err := cp.Close(); err != nil {
					t.Fatalf("Close() = %v", err)
				}
			}

			err := cp.MarkDone(tt.id)

			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("MarkDone(%q) error (-want +got):\n%s", tt.id, diff)
			}
			if cp.Done(tt.id) {
				t.Errorf("Done(%q) = true after a failed MarkDone, want false", tt.id)
			}
		})
	}
}

// fakeCheckpoint is an in-memory Checkpoint whose MarkDone can fail.
type fakeCheckpoint struct {
	done    map[string]bool
	markErr error
}

func (f *fakeCheckpoint) Done(id string) bool { return f.done[id] }

func (f *fakeCheckpoint) MarkDone(id string) error {
	if f.markErr != nil {
		return f.markErr
	}
	f.done[id] = true
	return nil
}

func TestCheckpointed(t *testing.T) {
	t.Parallel()
	const id = "asset-1"
	type outcome struct {
		Processed, Done bool
		Err             error
	}
	tests := []struct {
		name        string
		alreadyDone bool
		processErr  error
		markErr     error
		nilOnError  bool
		want        outcome
	}{
		{name: "alreadyDoneSkipped", alreadyDone: true, want: outcome{Done: true}},
		{name: "successMarked", want: outcome{Processed: true, Done: true}},
		{name: "processFailureReported", processErr: errProcess, want: outcome{Processed: true, Err: errProcess}},
		{name: "checkpointFailureReported", markErr: errMark, want: outcome{Processed: true, Err: errMark}},
		{name: "processFailureWithoutHandler", processErr: errProcess, nilOnError: true, want: outcome{Processed: true}},
		{name: "checkpointFailureWithoutHandler", markErr: errMark, nilOnError: true, want: outcome{Processed: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cp := &fakeCheckpoint{done: map[string]bool{id: tt.alreadyDone}, markErr: tt.markErr}
			var got outcome
			onError := func(_ string, err error) { got.Err = err }
			if tt.nilOnError {
				onError = nil
			}
			process := pipeline.Checkpointed(cp, func(s string) string { return s },
				func(context.Context, string) error { got.Processed = true; return tt.processErr },
				onError)

			process(t.Context(), id)

			got.Done = cp.Done(id)
			if diff := cmp.Diff(tt.want, got, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Checkpointed outcome (-want +got):\n%s", diff)
			}
		})
	}
}
