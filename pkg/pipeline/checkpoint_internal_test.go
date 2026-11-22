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

func (f *fakeJournal) Write(p []byte) (int, error) {
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

// TestFileCheckpointMarkDoneFailure is white-box because a short write or a
// failed fsync cannot be produced through a real file in a unit test.
func TestFileCheckpointMarkDoneFailure(t *testing.T) {
	t.Parallel()
	const first = `{"id":"a"}` + "\n"
	tests := []struct {
		name        string
		syncEvery   int
		journal     fakeJournal
		wantErrs    []error
		wantJournal string
		wantRetry   error
	}{
		{
			name:        "write rolled back",
			journal:     fakeJournal{writeErr: errWrite},
			wantErrs:    []error{errWrite},
			wantJournal: first,
		},
		{
			name:        "write not rolled back",
			journal:     fakeJournal{writeErr: errWrite, truncErr: errTruncate},
			wantErrs:    []error{errWrite, errTruncate},
			wantJournal: first + `{"id"`,
			wantRetry:   ErrCheckpointUnusable,
		},
		{
			name:        "sync failed",
			journal:     fakeJournal{syncErr: errSync},
			wantErrs:    []error{errSync},
			wantJournal: first,
			wantRetry:   ErrCheckpointUnusable,
		},
		{
			name:        "scheduled sync failed",
			syncEvery:   2,
			journal:     fakeJournal{syncErr: errSync},
			wantErrs:    []error{errSync},
			wantJournal: first,
			wantRetry:   ErrCheckpointUnusable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			journal := &fakeJournal{}
			c := &FileCheckpoint{syncEvery: max(tt.syncEvery, 1), file: journal, done: map[string]struct{}{}}
			if err := c.MarkDone("a"); err != nil {
				t.Fatalf("MarkDone(\"a\") = %v", err)
			}
			journal.writeErr, journal.syncErr, journal.truncErr = tt.journal.writeErr, tt.journal.syncErr, tt.journal.truncErr

			err := c.MarkDone("b")

			for _, want := range tt.wantErrs {
				if !errors.Is(err, want) {
					t.Errorf("MarkDone(\"b\") = %v, want it to wrap %v", err, want)
				}
			}
			if c.Done("b") {
				t.Error("Done(\"b\") = true after a failed MarkDone, want false")
			}
			if diff := cmp.Diff(tt.wantJournal, string(journal.data)); diff != "" {
				t.Errorf("journal after failure (-want +got):\n%s", diff)
			}
			journal.truncErr = nil
			if diff := cmp.Diff(tt.wantRetry, c.MarkDone("b"), cmpopts.EquateErrors()); diff != "" {
				t.Errorf("retried MarkDone(\"b\") error (-want +got):\n%s", diff)
			}
		})
	}
}

// TestFileCheckpointSyncCadence is white-box because fsync calls are not
// observable through a real file.
func TestFileCheckpointSyncCadence(t *testing.T) {
	t.Parallel()
	ids := []string{"a", "b", "a", "c", "d", "e", "f", "g"} // "a" repeats; 7 unique
	tests := []struct {
		name      string
		syncEvery int
		wantSyncs int // during MarkDone; Close adds one more
	}{
		{name: "every record", syncEvery: 1, wantSyncs: 7},
		{name: "every third record", syncEvery: 3, wantSyncs: 2},
		{name: "only on close", syncEvery: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			journal := &fakeJournal{}
			c := &FileCheckpoint{syncEvery: tt.syncEvery, file: journal, done: map[string]struct{}{}}

			for _, id := range ids {
				if err := c.MarkDone(id); err != nil {
					t.Fatalf("MarkDone(%q) = %v", id, err)
				}
			}
			gotSyncs := journal.syncs
			if err := c.Close(); err != nil {
				t.Fatalf("Close() = %v", err)
			}

			if diff := cmp.Diff([]int{tt.wantSyncs, tt.wantSyncs + 1}, []int{gotSyncs, journal.syncs}); diff != "" {
				t.Errorf("syncs after MarkDone, after Close (-want +got):\n%s", diff)
			}
		})
	}
}
