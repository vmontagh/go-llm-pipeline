package promptregistry_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/vmontagh/go-llm-pipeline/pkg/promptregistry"
)

// goldenCases pairs every fixture template with deterministic render input. The
// golden helper fails if a registered template is missing from this set, so a
// new .tmpl file must be accompanied by a case and a golden file.
func goldenCases() []promptregistry.GoldenCase {
	return []promptregistry.GoldenCase{
		{
			Ref:  promptregistry.Ref{Name: "classify_asset", Version: "v1"},
			Data: map[string]any{"Name": "invoices-2024", "Kind": "dataset"},
		},
		{
			Ref:  promptregistry.Ref{Name: "classify_asset", Version: "v2"},
			Data: map[string]any{"Name": "invoices-2024", "Kind": "dataset", "Owner": "finance"},
		},
		{
			Ref:  promptregistry.Ref{Name: "summarize", Version: "v1"},
			Data: map[string]any{"MaxWords": 25, "Body": "The quarterly report covers revenue and costs."},
		},
	}
}

func TestGolden(t *testing.T) {
	t.Parallel()
	reg, err := promptregistry.New(os.DirFS("testdata/templates"))
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	promptregistry.AssertGolden(t, reg, "testdata/golden", goldenCases())
}

// TestGoldenUpdate exercises the mutating update branch: with WithUpdate(true)
// the helper writes each golden into a nested <name>/<version>.golden path with
// the rendered contents.
func TestGoldenUpdate(t *testing.T) {
	t.Parallel()
	reg, err := promptregistry.New(os.DirFS("testdata/templates"))
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	dir := t.TempDir()
	promptregistry.AssertGolden(t, reg, dir, goldenCases(), promptregistry.WithUpdate(true))

	got, err := os.ReadFile(filepath.Join(dir, "summarize", "v1.golden"))
	if err != nil {
		t.Fatalf("reading updated golden: %v", err)
	}
	want := "Summarize the text below in 25 words or fewer.\n\nThe quarterly report covers revenue and costs.\n"
	if diff := cmp.Diff(want, string(got)); diff != "" {
		t.Errorf("updated golden mismatch (-want +got):\n%s", diff)
	}
}
