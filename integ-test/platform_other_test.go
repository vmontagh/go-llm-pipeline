//go:build !unix

package integtest

// unsupportedPlatformTags skips @unix scenarios, such as those opening a
// sqlitestore, which refuses to open outside Unix.
const unsupportedPlatformTags = "~@unix"
