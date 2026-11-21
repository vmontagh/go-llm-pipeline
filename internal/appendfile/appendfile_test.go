package appendfile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/vmontagh/go-llm-pipeline/internal/appendfile"
)

const (
	permOwnerOnly = 0o600
	fileName      = "out.log"
	appendedText  = "appended\n"
	existingText  = "existing\n"
)

func TestOpen_WritesAppendAfterExistingContent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		existing bool
		seed     string
		want     string
	}{
		{name: "missingFile_created", want: appendedText},
		{name: "emptyFile_written", existing: true, want: appendedText},
		{name: "existingContent_kept", existing: true, seed: existingText, want: existingText + appendedText},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), fileName)
			if tt.existing {
				writeFile(t, path, tt.seed)
			}

			appendText(t, path, appendedText)

			if diff := cmp.Diff(tt.want, readFile(t, path)); diff != "" {
				t.Errorf("contents of %q after Open and write mismatch (-want +got):\n%s", path, diff)
			}
		})
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), permOwnerOnly); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}

// appendText writes text through a handle from appendfile.Open.
func appendText(t *testing.T, path, text string) {
	t.Helper()
	f, err := appendfile.Open(path, permOwnerOnly)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	if _, err := f.WriteString(text); err != nil {
		t.Fatalf("WriteString to %q: %v", path, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close(%q): %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	return string(data)
}
