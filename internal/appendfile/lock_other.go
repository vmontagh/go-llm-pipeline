//go:build !unix && !windows

package appendfile

import (
	"errors"
	"os"
)

// Lock returns [errors.ErrUnsupported] on platforms without file locking, so
// callers that need exclusive ownership refuse to proceed unprotected.
func Lock(*os.File) error { return errors.ErrUnsupported }
