//go:build !unix && !windows

package appendfile

import "os"

// Lock is a no-op on platforms without file locking.
func Lock(*os.File) error { return nil }
