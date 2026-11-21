//go:build !unix

package appendfile

// extraOpenFlags is empty where O_NONBLOCK and O_NOFOLLOW do not exist; the
// fstat regular-file check still applies.
const extraOpenFlags = 0
