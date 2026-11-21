//go:build unix

package integtest

// unsupportedPlatformTags is empty on Unix, where every scenario can run.
const unsupportedPlatformTags = ""

// sqliteSupported reports whether sqlitestore can open a store here.
const sqliteSupported = true
