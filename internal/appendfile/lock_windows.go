//go:build windows

package appendfile

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockOffsetHigh locks a byte past the data; Windows locks would block readers.
const lockOffsetHigh = 0x7fff_ffff

// Lock takes an exclusive lock on f without blocking. It returns
// [ErrLocked] if another open handle holds it. Closing f releases it.
func Lock(f *os.File) error {
	ol := windows.Overlapped{OffsetHigh: lockOffsetHigh}
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &ol)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return ErrLocked
	}
	return err
}
