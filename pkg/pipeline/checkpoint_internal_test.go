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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			journal := &fakeJournal{}
			c := &FileCheckpoint{syncEvery: 1, file: journal, done: map[string]struct{}{}}
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
