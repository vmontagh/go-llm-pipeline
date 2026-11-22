package pipeline_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
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
func openCheckpoint(t *testing.T, path string) *pipeline.FileCheckpoint {
	t.Helper()
	cp, err := pipeline.OpenFileCheckpoint(path)
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
	tests := []struct {
		name     string
		journal  string // existing content; empty means no file yet
		mark     []string
		wantDone []string
	}{
		{name: "new journal", mark: []string{"a", "b", "a"}, wantDone: []string{"a", "b"}},
		{name: "existing records", journal: "{\"id\":\"a\"}\n\n{\"id\":\"b\"}\n", mark: []string{"c"}, wantDone: []string{"a", "b", "c"}},
		{name: "partial last record dropped", journal: "{\"id\":\"a\"}\n{\"id\":", mark: []string{"c"}, wantDone: []string{"a", "c"}},
		{name: "only a partial record", journal: "{\"id\":\"a\"", mark: []string{"c"}, wantDone: []string{"c"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), journalName)
			if tt.journal != "" {
				writeJournal(t, path, tt.journal)
			}
			cp := openCheckpoint(t, path)
			markDone(t, cp, tt.mark...)

			got := reopen(t, cp, path)

			if diff := cmp.Diff(tt.wantDone, doneOf(got, "a", "b", "c")); diff != "" {
				t.Errorf("Done after reopen (-want +got):\n%s", diff)
			}
			if got.Len() != len(tt.wantDone) {
				t.Errorf("Len() = %d, want %d", got.Len(), len(tt.wantDone))
			}
		})
	}
}

func TestOpenFileCheckpointErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		journal     string
		pathIsDir   bool
		alreadyOpen bool
		wantErr     error
	}{
		{name: "corrupt middle record", journal: "oops\n{\"id\":\"a\"}\n", wantErr: pipeline.ErrCorruptCheckpoint},
		{name: "record without id", journal: "{}\n", wantErr: pipeline.ErrCorruptCheckpoint},
		{name: "invalid UTF-8 id", journal: "{\"id\":\"asset-\xff\"}\n", wantErr: pipeline.ErrCorruptCheckpoint},
		{name: "path is a directory", pathIsDir: true, wantErr: appendfile.ErrNotRegularFile},
		{name: "already open", alreadyOpen: true, wantErr: pipeline.ErrCheckpointInUse},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), journalName)
			switch {
			case tt.pathIsDir:
				path = t.TempDir()
			case tt.alreadyOpen:
				openCheckpoint(t, path)
			default:
				writeJournal(t, path, tt.journal)
			}

			_, err := pipeline.OpenFileCheckpoint(path)

			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("OpenFileCheckpoint() error (-want +got):\n%s", diff)
			}
		})
	}
}

// TestFileCheckpointConcurrentWorkers shares one checkpoint across workers,
// with every ID queued twice, and checks the journal after reopening.
func TestFileCheckpointConcurrentWorkers(t *testing.T) {
	const (
		itemCount = 50
		workers   = 8
	)
	path := filepath.Join(t.TempDir(), journalName)
	cp := openCheckpoint(t, path)
	var ids []string
	for i := range itemCount {
		ids = append(ids, strconv.Itoa(i))
	}
	process := pipeline.Checkpointed(cp, func(id string) string { return id },
		func(context.Context, string) error { return nil },
		func(id string, err error) { t.Errorf("item %q: %v", id, err) })

	if err := pipeline.ConsumeAll(t.Context(), append(ids, ids...), workers, process); err != nil {
		t.Fatalf("ConsumeAll() = %v", err)
	}
	cp = reopen(t, cp, path)

	if diff := cmp.Diff(ids, doneOf(cp, ids...)); diff != "" {
		t.Errorf("Done after reopen (-want +got):\n%s", diff)
	}
	if got := cp.Len(); got != itemCount {
		t.Errorf("Len() after reopen = %d, want %d", got, itemCount)
	}
}

func TestOpenFileCheckpointUnreadableAncestor(t *testing.T) {
	const permTraverseOnly = 0o311
	tests := []struct {
		name         string
		journalExist bool
		wantErr      error
	}{
		{name: "existingDirectory", journalExist: true},
		{name: "createsDirectory", wantErr: fs.ErrPermission},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, journalName)
			if tt.journalExist {
				writeJournal(t, path, "")
			}
			if err := os.Chmod(root, permTraverseOnly); err != nil {
				t.Fatalf("Chmod() = %v", err)
			}
			t.Cleanup(func() {
				if err := os.Chmod(root, dirPerm); err != nil {
					t.Errorf("restoring permissions: %v", err)
				}
			})

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

func TestOpenFileCheckpointNilOption(t *testing.T) {
	cp, err := pipeline.OpenFileCheckpoint(filepath.Join(t.TempDir(), journalName), nil)
	if err != nil {
		t.Fatalf("OpenFileCheckpoint(nil option) = %v", err)
	}
	if err := cp.Close(); err != nil {
		t.Errorf("Close() = %v", err)
	}
}

func TestFileCheckpointMarkDoneErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		id      string
		closed  bool
		wantErr error
	}{
		{name: "empty id", wantErr: pipeline.ErrInvalidCheckpointID},
		{name: "invalid UTF-8 id", id: "asset-\xff", wantErr: pipeline.ErrInvalidCheckpointID},
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
		want        outcome
	}{
		{name: "already done is skipped", alreadyDone: true, want: outcome{Done: true}},
		{name: "success is marked", want: outcome{Processed: true, Done: true}},
		{name: "process failure is reported", processErr: errProcess, want: outcome{Processed: true, Err: errProcess}},
		{name: "checkpoint failure is reported", markErr: errMark, want: outcome{Processed: true, Err: errMark}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cp := &fakeCheckpoint{done: map[string]bool{id: tt.alreadyDone}, markErr: tt.markErr}
			var got outcome
			process := pipeline.Checkpointed(cp, func(s string) string { return s },
				func(context.Context, string) error { got.Processed = true; return tt.processErr },
				func(_ string, err error) { got.Err = err })

			process(t.Context(), id)

			got.Done = cp.Done(id)
			if diff := cmp.Diff(tt.want, got, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Checkpointed outcome (-want +got):\n%s", diff)
			}
		})
	}
}
