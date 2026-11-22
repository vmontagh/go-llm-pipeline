//go:build !unix

package appendfile

// SyncDir is a no-op where directories cannot be synced, such as Windows.
func SyncDir(string) error { return nil }
