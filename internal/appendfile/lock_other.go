//go:build (!unix && !windows) || aix

package appendfile

import (
	"errors"
	"os"
)

// Lock returns [errors.ErrUnsupported]: this platform has no file locking.
func Lock(*os.File) error { return errors.ErrUnsupported }
