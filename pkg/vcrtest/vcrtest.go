// Package vcrtest provides a small testing.T-aware helper that backs a provider
// adapter's *http.Client with a named YAML cassette: recorded HTTP interactions
// are replayed offline by default, and recorded once against a real backend when
// a developer opts in.
//
// # Workflow: record against a real backend, replay in CI
//
// Replay (default): with VCR_MODE unset, cassettes under testdata/cassettes are
// replayed with no network access, so `make test` is deterministic and runs
// fully offline. A request that matches no recorded interaction fails fast
// instead of reaching the network.
//
// Record (developer-only): set VCR_MODE=record and point the adapter at a real
// backend to record any missing cassette exactly once. Secrets are redacted (via
// pkg/scrub through pkg/llmvcr) before anything is written to disk, so no
// credentials land in the YAML. An existing cassette is left untouched; to
// refresh one, delete its file and re-record.
//
// # Layout and naming
//
// Cassettes live beside the test package that uses them, under
// testdata/cassettes/<name>.yaml. Names follow <operation>[_<variant>], for
// example embed_single or embed_batch. The name passed to Client may contain
// forward-slash separated subdirectories.
//
// # Request matching
//
// The default matcher (llmvcr.DefaultMatcher) matches on HTTP method, URL path,
// and query, tolerating a changed host so a cassette recorded against a real
// backend still replays against an unreachable placeholder host. Bodies are not
// matched, so each cassette should hold a single interaction; tests that need
// several variants use one cassette file per variant. Pass llmvcr.WithMatcher to
// opt into body-aware matching.
package vcrtest

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/vmontagh/go-llm-pipeline/pkg/llmvcr"
)

// EnvVCRMode is the environment variable that opts into recording. Set it to
// "record" to record missing cassettes; any other value replays.
const EnvVCRMode = "VCR_MODE"

// recordValue is the EnvVCRMode value that selects record mode.
const recordValue = "record"

// cassetteDir is the directory, relative to the test package, that holds
// cassettes.
const cassetteDir = "testdata/cassettes"

// cassetteExt is the extension go-vcr appends to a cassette name on disk.
const cassetteExt = ".yaml"

// Recording reports whether VCR_MODE selects record mode. Tests use it to guard
// setup that is only needed while recording, such as starting a real or stub
// backend; on replay that setup is skipped and the cassette is used instead.
func Recording() bool {
	return os.Getenv(EnvVCRMode) == recordValue
}

// modeFromEnv maps the VCR_MODE environment variable to an llmvcr.Mode. Record
// mode records a cassette only when it is absent (record-once); every other
// value replays.
func modeFromEnv() llmvcr.Mode {
	if Recording() {
		return llmvcr.ModeRecordOnce
	}
	return llmvcr.ModeReplayOnly
}

// cassettePath resolves a cassette name to its path (without extension) under
// the test package's testdata/cassettes directory.
func cassettePath(name string) string {
	return filepath.Join(cassetteDir, filepath.FromSlash(name))
}

// Client returns an *http.Client whose transport is backed by the cassette at
// testdata/cassettes/<name>.yaml. It replays offline by default and records the
// cassette once when VCR_MODE=record (see the package documentation). Pass the
// returned client to any adapter that accepts one (for example through a
// WithHTTPClient option). The recorder is stopped automatically via t.Cleanup,
// which flushes a freshly recorded cassette to disk.
//
// Additional llmvcr.Options (extra scrub rules, a custom matcher, and so on) are
// forwarded to the underlying recorder. On replay, a missing cassette fails the
// test with guidance to re-record.
func Client(t *testing.T, name string, opts ...llmvcr.Option) *http.Client {
	t.Helper()

	mode := modeFromEnv()
	path := cassettePath(name)

	if mode == llmvcr.ModeReplayOnly {
		if _, err := os.Stat(path + cassetteExt); err != nil {
			t.Fatalf("vcrtest: cassette %q not found: %v; run the test with %s=%s against a real backend to record it",
				path+cassetteExt, err, EnvVCRMode, recordValue)
		}
	}

	rec, err := llmvcr.New(path, mode, opts...)
	if err != nil {
		t.Fatalf("vcrtest: creating recorder for cassette %q: %v", name, err)
	}
	t.Cleanup(func() {
		if err := rec.Stop(); err != nil {
			t.Errorf("vcrtest: stopping recorder for cassette %q: %v", name, err)
		}
	})
	return rec.HTTPClient()
}
