package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/vmontagh/go-llm-pipeline/internal/appendfile"
)

// ErrCorruptCheckpoint is returned when a checkpoint journal has an
// unreadable record before its last line.
var ErrCorruptCheckpoint = errors.New("corrupt checkpoint journal")

// ErrEmptyCheckpointID is returned when MarkDone is given an empty ID.
var ErrEmptyCheckpointID = errors.New("checkpoint ID is empty")

// Checkpoint records which items a run has completed, so a restarted run can
// skip them. Implementations must be safe for concurrent use.
type Checkpoint interface {
	// Done reports whether id was marked done, in this run or an earlier one.
	Done(id string) bool
	// MarkDone durably records id as done.
	MarkDone(id string) error
}

// checkpointRecord is one journal line.
type checkpointRecord struct {
	ID string `json:"id"`
}

// defaultSyncEvery syncs every record: an LLM call costs far more than an
// fsync, and a lost record means paying for the call again.
const defaultSyncEvery = 1

// FileCheckpoint is a [Checkpoint] backed by an append-only JSON-lines
// journal with one {"id": ...} record per completed item. A crash can lose at
// most the record being written; a truncated final line is dropped when the
// journal is reopened. It is safe for concurrent use.
type FileCheckpoint struct {
	syncEvery int

	mu       sync.Mutex
	file     *os.File
	done     map[string]struct{}
	unsynced int
}

// CheckpointOption configures a FileCheckpoint.
type CheckpointOption func(*FileCheckpoint)

// WithSyncEvery syncs the journal to stable storage after every n records
// instead of after each one. Records are written to the operating system
// immediately, so a process crash loses none; up to n-1 records can be lost
// only if the machine itself crashes. n <= 0 syncs only on Close.
func WithSyncEvery(n int) CheckpointOption {
	return func(c *FileCheckpoint) { c.syncEvery = n }
}

// OpenFileCheckpoint opens or creates the journal at path and loads the IDs
// it already holds. The path rules match [NewDeadLetterQueue]. It returns
// ErrCorruptCheckpoint if a record before the last line is unreadable.
func OpenFileCheckpoint(path string, opts ...CheckpointOption) (*FileCheckpoint, error) {
	if err := os.MkdirAll(filepath.Dir(path), dirPermissions); err != nil {
		return nil, fmt.Errorf("creating checkpoint directory: %w", err)
	}
	file, err := appendfile.OpenReadWrite(path, filePermissions)
	if err != nil {
		return nil, fmt.Errorf("opening checkpoint: %w", err)
	}
	c := &FileCheckpoint{syncEvery: defaultSyncEvery, file: file}
	for _, opt := range opts {
		opt(c)
	}
	if c.done, err = load(file); err != nil {
		return nil, errors.Join(fmt.Errorf("loading checkpoint %q: %w", path, err), file.Close())
	}
	return c, nil
}

// load reads every complete record and truncates a partial final line, so
// the next record starts on a line of its own.
func load(file *os.File) (map[string]struct{}, error) {
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("reading: %w", err)
	}
	complete := bytes.LastIndexByte(data, '\n') + 1
	if complete < len(data) {
		if err := file.Truncate(int64(complete)); err != nil {
			return nil, fmt.Errorf("dropping partial record: %w", err)
		}
	}
	done := make(map[string]struct{})
	for n, line := range bytes.Split(data[:complete], []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rec checkpointRecord
		if err := json.Unmarshal(line, &rec); err != nil || rec.ID == "" {
			return nil, fmt.Errorf("%w: line %d", ErrCorruptCheckpoint, n+1)
		}
		done[rec.ID] = struct{}{}
	}
	return done, nil
}

// Done reports whether id was marked done.
func (c *FileCheckpoint) Done(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.done[id]
	return ok
}

// Len returns the number of completed IDs, for example to report how much of
// a resumed run is already done.
func (c *FileCheckpoint) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.done)
}

// MarkDone appends id to the journal. Marking an ID that is already done is a
// no-op. An empty id is rejected because it could not be told apart on
// reload.
func (c *FileCheckpoint) MarkDone(id string) error {
	if id == "" {
		return ErrEmptyCheckpointID
	}
	line, err := json.Marshal(checkpointRecord{ID: id})
	if err != nil {
		return fmt.Errorf("encoding checkpoint record: %w", err)
	}
	line = append(line, '\n')
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.done[id]; ok {
		return nil
	}
	if _, err := c.file.Write(line); err != nil {
		return fmt.Errorf("writing checkpoint record: %w", err)
	}
	c.done[id] = struct{}{}
	c.unsynced++
	if c.syncEvery > 0 && c.unsynced >= c.syncEvery {
		if err := c.file.Sync(); err != nil {
			return fmt.Errorf("syncing checkpoint: %w", err)
		}
		c.unsynced = 0
	}
	return nil
}

// Close syncs and closes the journal.
func (c *FileCheckpoint) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return errors.Join(c.file.Sync(), c.file.Close())
}

// Checkpointed adapts process for [Consume] so a run can resume: items whose
// ID cp already holds are skipped, and an item is marked done only after
// process succeeds. A process or checkpoint error goes to onError, for
// example to record the item in a [DeadLetterQueue]; the item stays
// unmarked, so the next run retries it. onError may be nil to ignore errors.
func Checkpointed[T any](cp Checkpoint, id func(T) string, process func(context.Context, T) error, onError func(T, error)) func(context.Context, T) {
	if onError == nil {
		onError = func(T, error) {}
	}
	return func(ctx context.Context, item T) {
		key := id(item)
		if cp.Done(key) {
			return
		}
		if err := process(ctx, item); err != nil {
			onError(item, err)
			return
		}
		if err := cp.MarkDone(key); err != nil {
			onError(item, fmt.Errorf("checkpointing %q: %w", key, err))
		}
	}
}
