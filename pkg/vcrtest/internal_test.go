package vcrtest

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestResolveCassettePath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		in        string
		want      string
		wantErrIs error
	}{
		{name: "flat name", in: "embed_single", want: filepath.Join(cassetteDir, "embed_single")},
		{name: "nested name", in: "ollama/embed_single", want: filepath.Join(cassetteDir, "ollama", "embed_single")},
		{name: "current directory rejected", in: ".", wantErrIs: errInvalidCassetteName},
		{name: "empty name rejected", in: "", wantErrIs: errInvalidCassetteName},
		{name: "collapsing traversal rejected", in: "nested/..", wantErrIs: errInvalidCassetteName},
		{name: "parent traversal rejected", in: "../escape", wantErrIs: errInvalidCassetteName},
		{name: "deep traversal rejected", in: "../../outside", wantErrIs: errInvalidCassetteName},
		{name: "absolute path rejected", in: "/etc/passwd", wantErrIs: errInvalidCassetteName},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := resolveCassettePath(tt.in)
			if tt.wantErrIs != nil {
				if !errors.Is(err, tt.wantErrIs) {
					t.Errorf("resolveCassettePath(%q) error = %v, want errors.Is(_, %v)", tt.in, err, tt.wantErrIs)
				}
				if got != "" {
					t.Errorf("resolveCassettePath(%q) = %q, want empty path on error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveCassettePath(%q) = %v, want nil", tt.in, err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("resolveCassettePath(%q) mismatch (-want +got):\n%s", tt.in, diff)
			}
		})
	}
}

func TestCheckReplayCassette(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		mode      llmvcr.Mode
		create    bool
		wantErrIs error
	}{
		{name: "replay missing errors", mode: llmvcr.ModeReplayOnly, create: false, wantErrIs: errCassetteNotFound},
		{name: "replay present is nil", mode: llmvcr.ModeReplayOnly, create: true},
		{name: "record missing is nil", mode: llmvcr.ModeRecordOnce, create: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "cass")
			if tt.create {
				if err := os.WriteFile(path+cassetteExt, []byte("---\n"), 0o644); err != nil {
					t.Fatalf("WriteFile() = %v", err)
				}
			}
			err := checkReplayCassette(path, tt.mode)
			if tt.wantErrIs == nil {
				if err != nil {
					t.Errorf("checkReplayCassette() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErrIs) {
				t.Fatalf("checkReplayCassette() error = %v, want errors.Is(_, %v)", err, tt.wantErrIs)
			}
			// The diagnostic must name the missing file and the VCR_MODE override.
			if !strings.Contains(err.Error(), path+cassetteExt) {
				t.Errorf("checkReplayCassette() error %q does not name cassette path %q", err, path+cassetteExt)
			}
			if !strings.Contains(err.Error(), EnvVCRMode) {
				t.Errorf("checkReplayCassette() error %q does not mention %s guidance", err, EnvVCRMode)
			}
		})
	}
}
