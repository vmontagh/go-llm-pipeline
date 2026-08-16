//go:build unix

package doctor

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// defaultFileLimitReader reads the open-file rlimit via the unix syscall.
type defaultFileLimitReader struct{}

// ReadFileLimit returns the soft and hard RLIMIT_NOFILE values.
func (defaultFileLimitReader) ReadFileLimit() (soft, hard uint64, err error) {
	var lim unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &lim); err != nil {
		return 0, 0, fmt.Errorf("reading RLIMIT_NOFILE: %w", err)
	}
	return uint64(lim.Cur), uint64(lim.Max), nil
}
