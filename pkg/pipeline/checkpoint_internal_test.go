package pipeline

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

var (
	errWrite    = errors.New("disk full")
	errSync     = errors.New("fsync failed")
	errTruncate = errors.New("truncate failed")
	errClose    = errors.New("close failed")
)

// fakeJournal is an in-memory journal whose operations can fail once. A
// failed write still stores half the record, like a short write.
type fakeJournal struct {
	data                                  []byte
	writeErr, syncErr, truncErr, closeErr error
	syncs                                 int
	closed                                bool
}

func (f *fakeJournal) WriteAt(p []byte, off int64) (int, error) {
	f.data = f.data[:off]
	if err := f.writeErr; err != nil {
		f.writeErr = nil
		f.data = append(f.data, p[:len(p)/2]...)
		return len(p) / 2, err
	}
	f.data = append(f.data, p...)
	return len(p), nil
}

func (f *fakeJournal) Sync() error {
	f.syncs++
	err := f.syncErr
	f.syncErr = nil
	return err
}

func (f *fakeJournal) Truncate(size int64) error {
	if err := f.truncErr; err != nil {
		return err
	}
	f.data = f.data[:size]
	return nil
}

func (f *fakeJournal) Close() error {
	f.closed = true
	return f.closeErr
}

// newFakeCheckpoint returns a FileCheckpoint that writes to an in-memory
// journal.
func newFakeCheckpoint(syncEvery int) (*FileCheckpoint, *fakeJournal) {
	journal := &fakeJournal{}
	c := &FileCheckpoint{syncEvery: defaultSyncEvery, file: journal, done: map[string]struct{}{}}
	WithSyncEvery(syncEvery)(c)
	return c, journal
}

// markDone marks every id, failing the test on error.
func markDone(t *testing.T, c *FileCheckpoint, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if err := c.MarkDone(id); err != nil {
			t.Fatalf("MarkDone(%q) = %v", id, err)
		}
	}
}

// TestFileCheckpointMarkDoneFailure is white-box because a short write or a
// failed fsync cannot be produced through a real file in a unit test.
func TestFileCheckpointMarkDoneFailure(t *testing.T) {
	t.Parallel()
	const first = `{"id":"a"}` + "\n"
	type outcome struct {
		Err      error
		Unusable bool
		Done     bool
		Journal  string
		RetryErr error
	}
	tests := []struct {
		name      string
		syncEvery int
		fail      fakeJournal
		want      outcome
	}{
		{name: "writeRolledBack", syncEvery: 1, fail: fakeJournal{writeErr: errWrite}, want: outcome{Err: errWrite, Journal: first}},
		{name: "rollbackFailed", syncEvery: 1, fail: fakeJournal{writeErr: errWrite, truncErr: errTruncate}, want: outcome{Err: errTruncate, Unusable: true, Journal: first + `{"id"`, RetryErr: ErrCheckpointUnusable}},
		{name: "syncFailed", syncEvery: 1, fail: fakeJournal{syncErr: errSync}, want: outcome{Err: errSync, Unusable: true, Journal: first, RetryErr: ErrCheckpointUnusable}},
		{name: "scheduledSyncFailed", syncEvery: 2, fail: fakeJournal{syncErr: errSync}, want: outcome{Err: errSync, Unusable: true, Journal: first, RetryErr: ErrCheckpointUnusable}},
		{name: "syncAndRollbackFailed", syncEvery: 1, fail: fakeJournal{syncErr: errSync, truncErr: errTruncate}, want: outcome{Err: errTruncate, Unusable: true, Journal: first + `{"id":"b"}` + "\n", RetryErr: ErrCheckpointUnusable}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, journal := newFakeCheckpoint(tt.syncEvery)
			markDone(t, c, "a")
			journal.writeErr, journal.syncErr, journal.truncErr = tt.fail.writeErr, tt.fail.syncErr, tt.fail.truncErr

			err := c.MarkDone("b")

			got := outcome{Err: err, Unusable: errors.Is(err, ErrCheckpointUnusable), Done: c.Done("b"), Journal: string(journal.data)}
			journal.truncErr = nil
			got.RetryErr = c.MarkDone("b")
			if diff := cmp.Diff(tt.want, got, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("MarkDone(\"b\") after failure (-want +got):\n%s", diff)
			}
		})
	}
}

// TestFileCheckpointSyncCadence is white-box because fsync calls are not
// observable through a real file.
func TestFileCheckpointSyncCadence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		syncEvery int
		wantSyncs int // including the one in Close
	}{
		{name: "everyRecord", syncEvery: 1, wantSyncs: 8},
		{name: "everyThirdRecord", syncEvery: 3, wantSyncs: 3},
		{name: "onlyOnClose", syncEvery: 0, wantSyncs: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, journal := newFakeCheckpoint(tt.syncEvery)
			markDone(t, c, "a", "b", "a", "c", "d", "e", "f", "g") // 7 unique IDs

			if err := c.Close(); err != nil {
				t.Fatalf("Close() = %v", err)
			}

			if journal.syncs != tt.wantSyncs {
				t.Errorf("Sync calls = %d, want %d", journal.syncs, tt.wantSyncs)
			}
		})
	}
}

// TestFileCheckpointClose is white-box because Sync and Close failures cannot
// be produced through a real file.
func TestFileCheckpointClose(t *testing.T) {
	t.Parallel()
	type outcome struct {
		Err    error
		Closed bool
	}
	tests := []struct {
		name string
		fail fakeJournal
		want outcome
	}{
		{name: "closes", want: outcome{Closed: true}},
		{name: "syncFailed", fail: fakeJournal{syncErr: errSync}, want: outcome{Err: errSync, Closed: true}},
		{name: "closeFailed", fail: fakeJournal{closeErr: errClose}, want: outcome{Err: errClose, Closed: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, journal := newFakeCheckpoint(0)
			markDone(t, c, "a")
			journal.syncErr, journal.closeErr = tt.fail.syncErr, tt.fail.closeErr

			err := c.Close()

			got := outcome{Err: err, Closed: journal.closed}
			if diff := cmp.Diff(tt.want, got, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Close() (-want +got):\n%s", diff)
			}
		})
	}
}

// TestSyncDirs is white-box because which directories get fsynced is not
// observable through a real file.
func TestSyncDirs(t *testing.T) {
	t.Parallel()
	const permDir = 0o755
	tests := []struct {
		name string
		path string // the journal, relative to the temp dir
		want []string
	}{
		{name: "plainPath", path: "real/run/j.jsonl", want: []string{"real/run", "real", "."}},
		{name: "symlinkedDirectory", path: "link/run/j.jsonl", want: []string{"deep/real/run", "deep/real", "deep", "."}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatalf("EvalSymlinks() = %v", err)
			}
			for _, dir := range []string{"real/run", "deep/real/run"} {
				if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(dir)), permDir); err != nil {
					t.Fatalf("MkdirAll() = %v", err)
				}
			}
			if err := os.Symlink(filepath.Join(root, "deep", "real"), filepath.Join(root, "link")); err != nil {
				t.Skipf("Symlink() = %v", err)
			}
			path := filepath.Join(root, filepath.FromSlash(tt.path))
			var got []string
			record := func(dir string) error {
				if rel, err := filepath.Rel(root, dir); err == nil && filepath.IsLocal(rel) {
					got = append(got, filepath.ToSlash(rel))
				}
				return nil
			}

			if err := syncDirs(path, filepath.Dir(path), record); err != nil {
				t.Fatalf("syncDirs() = %v", err)
			}

			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("syncDirs(%q) synced (-want +got):\n%s", tt.path, diff)
			}
		})
	}
}
