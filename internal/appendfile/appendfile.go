// Package appendfile opens caller-configured append-only files (logs, dead
// letter queues) without blocking on special files or following symlinks.
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
)

// Open opens path for appending, creating it with perm if missing. The open is
// non-blocking and does not follow a final-component symlink (on Unix), and the
// handle is checked with fstat, so a FIFO or device fails fast instead of
// hanging an uncancelable open.
func Open(path string, perm os.FileMode) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND|extraOpenFlags, perm)
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
