//go:build unix

package appendfile

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// Lock takes an exclusive advisory lock on f without blocking. It returns
// [ErrLocked] if another open handle holds it. Closing f releases it.
func Lock(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return ErrLocked
	}
	return err
}
