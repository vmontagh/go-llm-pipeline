//go:build !unix

package sqlitestore

import (
	"errors"
	"os"
)

// precreateOpenFlags mirrors the Unix set for compilation only; Open rejects
// non-Unix platforms before precreateSecure is ever reached.
const precreateOpenFlags = os.O_CREATE

// fileOwnerUID is unreachable on non-Unix platforms (Open rejects them before
// resolving the store path); it exists only so the shared path-safety code
// compiles.
func fileOwnerUID(os.FileInfo) (int, error) {
	return 0, errOwnerOnlyUnsupported
}

// errOwnerOnlyUnsupported reports that owner-only file permissions cannot be
// enforced on this platform. Non-Unix platforms lack the enforced permission
// model this store relies on (Windows uses ACLs rather than Unix permission
// bits, so os.Chmod cannot restrict the database file to its owner; others lack
// the model entirely), so the store refuses to open rather than claiming a
// protection it does not provide.
var errOwnerOnlyUnsupported = errors.New("owner-only file permissions are unsupported on this platform")

// ensureOwnerOnlySupported rejects non-Unix platforms, where os.Chmod cannot
// enforce owner-only access to the database file.
func ensureOwnerOnlySupported() error {
	return errOwnerOnlyUnsupported
}
