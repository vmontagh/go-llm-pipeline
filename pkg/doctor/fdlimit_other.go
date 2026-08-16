//go:build !unix

package doctor

// defaultFileLimitReader is the fallback for platforms without rlimit support.
type defaultFileLimitReader struct{}

// ReadFileLimit reports that file-descriptor limits are unavailable.
func (defaultFileLimitReader) ReadFileLimit() (soft, hard uint64, err error) {
	return 0, 0, ErrFileLimitUnsupported
}
