package pipeline

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

var (
	errWrite    = errors.New("disk full")
	errSync     = errors.New("fsync failed")
	errTruncate = errors.New("truncate failed")
)

// fakeJournal is an in-memory journal whose operations can fail once. A
// failed write still stores half the record, like a short write.
type fakeJournal struct {
	data                        []byte
	writeErr, syncErr, truncErr error
	syncs                       int
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

func (*fakeJournal) Close() error { return nil }

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
		{name: "write rolled back", syncEvery: 1, fail: fakeJournal{writeErr: errWrite}, want: outcome{Err: errWrite, Journal: first}},
		{name: "rollback failed", syncEvery: 1, fail: fakeJournal{writeErr: errWrite, truncErr: errTruncate}, want: outcome{Err: errTruncate, Journal: first + `{"id"`, RetryErr: ErrCheckpointUnusable}},
		{name: "sync failed", syncEvery: 1, fail: fakeJournal{syncErr: errSync}, want: outcome{Err: errSync, Journal: first, RetryErr: ErrCheckpointUnusable}},
		{name: "scheduled sync failed", syncEvery: 2, fail: fakeJournal{syncErr: errSync}, want: outcome{Err: errSync, Journal: first, RetryErr: ErrCheckpointUnusable}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, journal := newFakeCheckpoint(tt.syncEvery)
			markDone(t, c, "a")
			journal.writeErr, journal.syncErr, journal.truncErr = tt.fail.writeErr, tt.fail.syncErr, tt.fail.truncErr

			err := c.MarkDone("b")

			got := outcome{Err: err, Done: c.Done("b"), Journal: string(journal.data)}
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
		{name: "every record", syncEvery: 1, wantSyncs: 8},
		{name: "every third record", syncEvery: 3, wantSyncs: 3},
		{name: "only on close", syncEvery: 0, wantSyncs: 1},
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
