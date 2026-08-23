package vcrtest

import (
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/vmontagh/go-llm-pipeline/pkg/llmvcr"
)

func TestModeFromEnv(t *testing.T) {
	tests := []struct {
		name    string
		envSet  bool
		envVal  string
		want    llmvcr.Mode
		wantRec bool
	}{
		{name: "unset replays", envSet: false, want: llmvcr.ModeReplayOnly, wantRec: false},
		{name: "empty replays", envSet: true, envVal: "", want: llmvcr.ModeReplayOnly, wantRec: false},
		{name: "replay value replays", envSet: true, envVal: "replay", want: llmvcr.ModeReplayOnly, wantRec: false},
		{name: "record value records", envSet: true, envVal: "record", want: llmvcr.ModeRecordOnce, wantRec: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envSet {
				t.Setenv(EnvVCRMode, tt.envVal)
			} else {
				// Ensure a value inherited from the environment does not leak in.
				t.Setenv(EnvVCRMode, "")
			}
			if diff := cmp.Diff(tt.want, modeFromEnv()); diff != "" {
				t.Errorf("modeFromEnv() mismatch (-want +got):\n%s", diff)
			}
			if got := Recording(); got != tt.wantRec {
				t.Errorf("Recording() = %v, want %v", got, tt.wantRec)
			}
		})
	}
}

func TestCassettePath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cassett string
		want    string
	}{
		{name: "flat name", cassett: "embed_single", want: filepath.Join("testdata", "cassettes", "embed_single")},
		{name: "nested name", cassett: "ollama/embed_single", want: filepath.Join("testdata", "cassettes", "ollama", "embed_single")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(tt.want, cassettePath(tt.cassett)); diff != "" {
				t.Errorf("cassettePath(%q) mismatch (-want +got):\n%s", tt.cassett, diff)
			}
		})
	}
}
