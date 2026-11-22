//go:build !unix

package appendfile

// SyncDir is a no-op where directories cannot be opened for syncing, such as
// Windows, whose file systems journal directory metadata themselves.
func SyncDir(string) error { return nil }
