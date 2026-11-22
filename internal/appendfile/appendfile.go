// Package appendfile opens caller-configured append-only files (logs, dead
// letter queues) and rejects paths that are not regular files. On Unix the open
// also never blocks on a special file and refuses a final-component symlink.
package appendfile

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

var (
	// ErrNotRegularFile is returned when the path is a directory, FIFO, device
	// or other non-regular file.
	ErrNotRegularFile = errors.New("not a regular file")
	// ErrSymlink is returned when the final path component is a symlink, which
	// is rejected so a swapped link cannot redirect writes.
	ErrSymlink = errors.New("path is a symlink")
	// ErrLocked is returned by [Lock] when another handle holds the lock.
	ErrLocked = errors.New("file is locked")
)

// Open opens path for appending, creating it with perm if missing, and checks
// the handle with fstat so a directory, FIFO or device is rejected with
// [ErrNotRegularFile]. On Unix the open is also non-blocking and refuses a
// final-component symlink with [ErrSymlink], so a FIFO fails fast instead of
// hanging an uncancelable open. Other platforms get only the fstat check.
func Open(path string, perm os.FileMode) (*os.File, error) {
	return open(path, os.O_WRONLY|os.O_APPEND, perm)
}

// OpenReadWrite is [Open] for read and write without O_APPEND, for journals
// that are replayed and truncated (Windows refuses truncation in append
// mode). Callers write at explicit offsets.
func OpenReadWrite(path string, perm os.FileMode) (*os.File, error) {
	return open(path, os.O_RDWR, perm)
}

func open(path string, flags int, perm os.FileMode) (*os.File, error) {
	f, err := os.OpenFile(path, flags|os.O_CREATE|extraOpenFlags, perm)
	if err != nil {
		return nil, classifyOpenError(err)
	}
	info, err := f.Stat()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("checking %q: %w", path, err), f.Close())
	}
	if !info.Mode().IsRegular() {
		return nil, errors.Join(fmt.Errorf("opening %q: %w", path, ErrNotRegularFile), f.Close())
	}
	return f, nil
}

// classifyOpenError maps the errno values a special path produces before fstat
// can run onto this package's sentinels, keeping the original error wrapped.
func classifyOpenError(err error) error {
	switch {
	case errors.Is(err, syscall.ELOOP):
		return errors.Join(ErrSymlink, err)
	// EISDIR: Linux refuses a writable open of a directory. ENXIO: a FIFO with
	// no reader, or a device without a driver, under O_NONBLOCK.
	case errors.Is(err, syscall.EISDIR), errors.Is(err, syscall.ENXIO):
		return errors.Join(ErrNotRegularFile, err)
	default:
		return err
	}
}
