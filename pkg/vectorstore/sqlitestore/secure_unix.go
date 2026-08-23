//go:build unix

package sqlitestore

import (
	"errors"
	"os"
	"syscall"
)

// precreateOpenFlags opens the store file non-blocking and without following a
// final-component symlink, so a FIFO or other special file is rejected promptly
// by the fstat that follows (instead of blocking an uncancelable open) and a
// swapped symlink cannot redirect the create. O_CREATE makes a missing regular
// file without truncating an existing one.
const precreateOpenFlags = os.O_CREATE | syscall.O_NONBLOCK | syscall.O_NOFOLLOW

// errOwnershipUnavailable reports that a file's owning uid could not be
// determined from its FileInfo, so the directory-component ownership safety
// check cannot be satisfied.
var errOwnershipUnavailable = errors.New("file ownership information unavailable")

// fileOwnerUID returns the UID that owns the file described by info, used to
// verify that every directory component on the store path is owned by the
// caller or root.
func fileOwnerUID(info os.FileInfo) (int, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errOwnershipUnavailable
	}
	return int(stat.Uid), nil
}

// ensureOwnerOnlySupported reports whether this platform can enforce owner-only
// file permissions. Unix-like systems apply os.Chmod permission bits directly,
// so it is always supported.
func ensureOwnerOnlySupported() error {
	return nil
}
