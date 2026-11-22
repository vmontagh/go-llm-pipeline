package pipeline

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"unicode/utf8"

	"github.com/vmontagh/go-llm-pipeline/internal/appendfile"
)

var (
	// ErrCorruptCheckpoint is returned when a checkpoint journal has an
	// unreadable record before its last line.
	ErrCorruptCheckpoint = errors.New("corrupt checkpoint journal")
	// ErrCheckpointUnusable is returned by MarkDone after a failed sync or
	// rollback; reopen the checkpoint to continue.
	ErrCheckpointUnusable = errors.New("checkpoint unusable; reopen it")
	// ErrCheckpointInUse is returned when another FileCheckpoint has the
	// journal open.
	ErrCheckpointInUse = errors.New("checkpoint journal in use")
	// ErrInvalidCheckpointID is returned for an empty or non-UTF-8 ID.
	ErrInvalidCheckpointID = errors.New("invalid checkpoint ID")
)

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

// defaultSyncEvery is 1 because an fsync is far cheaper than a repeated LLM call.
const defaultSyncEvery = 1

// FileCheckpoint is a [Checkpoint] backed by an append-only JSON-lines
// journal. A crash loses at most the record being written. It is safe for
// concurrent use.
type FileCheckpoint struct {
	syncEvery int

	mu       sync.Mutex
	file     journal
	size     int64 // journal length, so a failed append can be rolled back
	done     map[string]struct{}
	unsynced int
	broken   error // set once the journal may not match done
}

// journal is the file a FileCheckpoint appends to.
type journal interface {
	io.WriterAt
	Sync() error
	Truncate(size int64) error
	Close() error
}

// CheckpointOption configures a FileCheckpoint.
type CheckpointOption func(*FileCheckpoint)

// WithSyncEvery fsyncs every n records instead of every record; a machine
// crash can then lose up to n-1. n <= 0 syncs only on Close.
func WithSyncEvery(n int) CheckpointOption {
	return func(c *FileCheckpoint) { c.syncEvery = n }
}

// OpenFileCheckpoint opens or creates the journal at path, loads its IDs, and
// locks it until Close. It returns ErrCorruptCheckpoint for an unreadable
// record, ErrCheckpointInUse if the journal is locked, and
// errors.ErrUnsupported where file locking is unavailable.
func OpenFileCheckpoint(path string, opts ...CheckpointOption) (*FileCheckpoint, error) {
	if err := os.MkdirAll(filepath.Dir(path), dirPermissions); err != nil {
		return nil, fmt.Errorf("creating checkpoint directory: %w", err)
	}
	file, err := appendfile.OpenReadWrite(path, filePermissions)
	if err != nil {
		return nil, fmt.Errorf("opening checkpoint: %w", err)
	}
	// A second writer's rollback could truncate this one's records.
	if err := appendfile.Lock(file); err != nil {
		if errors.Is(err, appendfile.ErrLocked) {
			err = ErrCheckpointInUse
		}
		return nil, errors.Join(fmt.Errorf("locking checkpoint %q: %w", path, err), file.Close())
	}
	if err := syncDirs(path); err != nil {
		return nil, errors.Join(fmt.Errorf("syncing checkpoint directories: %w", err), file.Close())
	}
	c := &FileCheckpoint{syncEvery: defaultSyncEvery, file: file}
	for _, opt := range opts {
		opt(c)
	}
	if c.done, c.size, err = load(file); err != nil {
		return nil, errors.Join(fmt.Errorf("loading checkpoint %q: %w", path, err), file.Close())
	}
	// A previous run may have crashed before syncing what was just replayed.
	if err := file.Sync(); err != nil {
		return nil, errors.Join(fmt.Errorf("syncing checkpoint %q: %w", path, err), file.Close())
	}
	return c, nil
}

// syncDirs fsyncs every directory above path, so entries an earlier run
// created survive a machine crash even if that run crashed before syncing.
// Ancestors we cannot read are skipped: we did not create them.
func syncDirs(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolving %q: %w", path, err)
	}
	for dir := filepath.Dir(abs); ; dir = filepath.Dir(dir) {
		if err := appendfile.SyncDir(dir); err != nil && !errors.Is(err, fs.ErrPermission) {
			return fmt.Errorf("syncing %q: %w", dir, err)
		}
		if filepath.Dir(dir) == dir {
			return nil
		}
	}
}

// load reads every complete record and truncates a partial final line, so
// the next record starts on a line of its own.
func load(file *os.File) (done map[string]struct{}, size int64, err error) {
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, 0, fmt.Errorf("reading: %w", err)
	}
	complete := bytes.LastIndexByte(data, '\n') + 1
	if complete < len(data) {
		if err := file.Truncate(int64(complete)); err != nil {
			return nil, 0, fmt.Errorf("dropping partial record: %w", err)
		}
	}
	done = make(map[string]struct{})
	for n, line := range bytes.Split(data[:complete], []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rec checkpointRecord
		// json.Unmarshal would silently replace invalid UTF-8 with U+FFFD.
		if !utf8.Valid(line) || json.Unmarshal(line, &rec) != nil || rec.ID == "" {
			return nil, 0, fmt.Errorf("%w: line %d", ErrCorruptCheckpoint, n+1)
		}
		done[rec.ID] = struct{}{}
	}
	return done, int64(complete), nil
}

// Done reports whether id was marked done.
func (c *FileCheckpoint) Done(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.done[id]
	return ok
}

// Len returns the number of completed IDs.
func (c *FileCheckpoint) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.done)
}

// MarkDone appends id to the journal; repeating an ID is a no-op. On error id
// stays unmarked and MarkDone can be retried, unless it returns
// ErrCheckpointUnusable.
func (c *FileCheckpoint) MarkDone(id string) error {
	if id == "" || !utf8.ValidString(id) {
		return ErrInvalidCheckpointID
	}
	line, err := json.Marshal(checkpointRecord{ID: id})
	if err != nil {
		return fmt.Errorf("encoding checkpoint record: %w", err)
	}
	line = append(line, '\n')
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.broken != nil {
		return fmt.Errorf("%w: %w", ErrCheckpointUnusable, c.broken)
	}
	if _, ok := c.done[id]; ok {
		return nil
	}
	if _, err := c.file.WriteAt(line, c.size); err != nil {
		return c.rollback(fmt.Errorf("writing checkpoint record: %w", err))
	}
	if c.syncEvery > 0 && c.unsynced+1 >= c.syncEvery {
		if err := c.file.Sync(); err != nil {
			// After a failed fsync the kernel may have dropped earlier
			// unsynced records too, so no later write can be trusted.
			err = fmt.Errorf("syncing checkpoint: %w", err)
			c.broken = err
			return errors.Join(err, c.rollback(nil))
		}
		c.unsynced = 0
	} else {
		c.unsynced++
	}
	c.size += int64(len(line))
	c.done[id] = struct{}{}
	return nil
}

// rollback truncates a failed append and returns cause. c.mu must be held.
func (c *FileCheckpoint) rollback(cause error) error {
	if err := c.file.Truncate(c.size); err != nil {
		err = fmt.Errorf("rolling back checkpoint record: %w", err)
		c.broken = cmp.Or(c.broken, err)
		return errors.Join(cause, err)
	}
	return cause
}

// Close syncs and closes the journal.
func (c *FileCheckpoint) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return errors.Join(c.file.Sync(), c.file.Close())
}

// Checkpointed adapts process for [Consume]: it skips items already done and
// marks an item done only after process succeeds. Errors go to onError (which
// may be nil), and the item is retried on the next run.
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
