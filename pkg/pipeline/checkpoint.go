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

// ErrCorruptCheckpoint is returned when a checkpoint journal has an
// unreadable record before its last line.
var ErrCorruptCheckpoint = errors.New("corrupt checkpoint journal")

// ErrCheckpointUnusable is returned by MarkDone after the journal could not be
// synced or a failed write could not be rolled back. The journal on disk can
// no longer be trusted to match Done, so the checkpoint must be reopened.
var ErrCheckpointUnusable = errors.New("checkpoint unusable; reopen it")

// ErrCheckpointInUse is returned when another FileCheckpoint, in this process
// or another, already has the journal open.
var ErrCheckpointInUse = errors.New("checkpoint journal in use")

// ErrInvalidCheckpointID is returned when MarkDone is given an empty ID or
// one that is not valid UTF-8, which the JSON journal could not store
// losslessly.
var ErrInvalidCheckpointID = errors.New("invalid checkpoint ID")

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
	file     journal
	size     int64 // journal length, so a failed append can be rolled back
	done     map[string]struct{}
	unsynced int
	broken   error // set once the journal may not match done
}

// journal is the file a FileCheckpoint appends to.
type journal interface {
	io.Writer
	Sync() error
	Truncate(size int64) error
	Close() error
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
// ErrCorruptCheckpoint if a record before the last line is unreadable, and
// ErrCheckpointInUse if another FileCheckpoint holds the journal; the journal
// stays locked until Close. Directories and the journal it creates are synced before it returns, so
// they survive a machine crash.
func OpenFileCheckpoint(path string, opts ...CheckpointOption) (*FileCheckpoint, error) {
	created, err := missingPaths(path)
	if err != nil {
		return nil, fmt.Errorf("checking checkpoint path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), dirPermissions); err != nil {
		return nil, fmt.Errorf("creating checkpoint directory: %w", err)
	}
	file, err := appendfile.OpenReadWrite(path, filePermissions)
	if err != nil {
		return nil, fmt.Errorf("opening checkpoint: %w", err)
	}
	// Two writers would each roll back to their own cached size and could
	// truncate the other's synced records.
	if err := appendfile.Lock(file); err != nil {
		if errors.Is(err, appendfile.ErrLocked) {
			err = ErrCheckpointInUse
		}
		return nil, errors.Join(fmt.Errorf("locking checkpoint %q: %w", path, err), file.Close())
	}
	for _, p := range created {
		if err := appendfile.SyncDir(filepath.Dir(p)); err != nil {
			return nil, errors.Join(fmt.Errorf("syncing checkpoint directory: %w", err), file.Close())
		}
	}
	c := &FileCheckpoint{syncEvery: defaultSyncEvery, file: file}
	for _, opt := range opts {
		opt(c)
	}
	if c.done, c.size, err = load(file); err != nil {
		return nil, errors.Join(fmt.Errorf("loading checkpoint %q: %w", path, err), file.Close())
	}
	return c, nil
}

// missingPaths returns path and each of its ancestors that does not exist
// yet, deepest first: the entries OpenFileCheckpoint will create.
func missingPaths(path string) ([]string, error) {
	var missing []string
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		_, err := os.Lstat(p)
		if err == nil {
			return missing, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		missing = append(missing, p)
		if filepath.Dir(p) == p {
			return missing, nil
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

// Len returns the number of completed IDs, for example to report how much of
// a resumed run is already done.
func (c *FileCheckpoint) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.done)
}

// MarkDone appends id to the journal. Marking an ID that is already done is a
// no-op. An empty or invalid UTF-8 id is rejected with
// ErrInvalidCheckpointID because it could not be reloaded unchanged. On error id is not marked done. A failed write is rolled back so
// MarkDone can be retried; a failed sync, or a rollback that fails, makes
// every later MarkDone return ErrCheckpointUnusable.
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
	if _, err := c.file.Write(line); err != nil {
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

// rollback truncates the journal to its last good length after a failed
// append and returns cause. If the truncation fails, the checkpoint becomes
// unusable. c.mu must be held.
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
