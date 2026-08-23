//go:build live

package integtest

// This file configures the developer-only live lane, enabled with the `live`
// build tag. It selects @live-local scenarios that execute against a real local
// Ollama. Combined with VCR_MODE=record (see the Makefile test-bdd-live target),
// each scenario records its cassette exactly once so the same behaviour can be
// replayed offline by the fake lane.
const (
	// defaultGodogTags selects the live-local lane.
	defaultGodogTags = "@live-local"
	// isFakeMode reports that this build records against a live backend rather
	// than replaying cassettes.
	isFakeMode = false
)
