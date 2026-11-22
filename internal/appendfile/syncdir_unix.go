//go:build unix

package appendfile

import (
	"errors"
	"fmt"
	"os"
)

// SyncDir flushes dir's entries to stable storage, so a file or directory
// just created in it survives a machine crash.
func SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("opening directory: %w", err)
	}
	return errors.Join(d.Sync(), d.Close())
}
