package doctor

import "errors"

// ErrFileLimitUnsupported is returned by a FileLimitReader on platforms that
// do not expose an open-file rlimit. The file-descriptor check reports
// StatusSkip when it sees this error.
var ErrFileLimitUnsupported = errors.New("file-descriptor limit not supported on this platform")
