package llmtest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// UpdateEnv names the environment variable that makes Golden rewrite golden
// files instead of comparing against them. Set it to any non-empty value.
const UpdateEnv = "LLMTEST_UPDATE"

const (
	goldenDir  = "testdata"
	goldenExt  = ".golden"
	permDir    = 0o750
	permFile   = 0o600
	jsonIndent = "  "
)

// Golden compares got, encoded as indented JSON, with testdata/<name>.golden.
// JSON encoding sorts map keys, so the output is stable across runs. With
// UpdateEnv set, Golden writes the file instead.
func Golden(t testing.TB, name string, got any) {
	t.Helper()
	data, err := json.MarshalIndent(got, "", jsonIndent)
	if err != nil {
		t.Fatalf("Golden(%q): encoding result: %v", name, err)
		return
	}
	data = append(data, '\n')
	file := name + goldenExt

	if os.Getenv(UpdateEnv) != "" {
		if err := writeGolden(file, data); err != nil {
			t.Fatalf("Golden(%q): %v", name, err)
		}
		return
	}

	want, err := readGolden(file)
	if err != nil {
		t.Fatalf("Golden(%q): %v (set %s=1 to create it)", name, err, UpdateEnv)
		return
	}
	if diff := cmp.Diff(want, string(data)); diff != "" {
		t.Errorf("Golden(%q) mismatch (-want +got):\n%s", name, diff)
	}
}

func readGolden(file string) (string, error) {
	root, err := os.OpenRoot(goldenDir)
	if err != nil {
		return "", fmt.Errorf("opening %s: %w", goldenDir, err)
	}
	defer root.Close()
	data, err := root.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("reading golden: %w", err)
	}
	// Git may check files out with CRLF line endings on Windows.
	return strings.ReplaceAll(string(data), "\r\n", "\n"), nil
}

func writeGolden(file string, data []byte) error {
	if err := os.MkdirAll(goldenDir, permDir); err != nil {
		return fmt.Errorf("creating %s: %w", goldenDir, err)
	}
	root, err := os.OpenRoot(goldenDir)
	if err != nil {
		return fmt.Errorf("opening %s: %w", goldenDir, err)
	}
	defer root.Close()
	if err := root.MkdirAll(filepath.Dir(file), permDir); err != nil {
		return fmt.Errorf("creating golden dir: %w", err)
	}
	if err := root.WriteFile(file, data, permFile); err != nil {
		return fmt.Errorf("writing golden: %w", err)
	}
	return nil
}
