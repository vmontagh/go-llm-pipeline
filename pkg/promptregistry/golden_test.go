package promptregistry_test

import (
	"os"
	"testing"

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
