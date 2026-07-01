package openai

// This file exists to give the openai package a non-test source file
// alongside openai_internal_test.go so that internal (white-box) tests
// can live next to package-private helpers without violating Go's
// "_test.go files cannot be the only files in a package" expectations.
