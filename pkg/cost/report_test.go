package cost_test

import (
	"bytes"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/vmontagh/go-llm-pipeline/pkg/cost"
)

func TestReportWriteText(t *testing.T) {
	t.Parallel()
	r := cost.Report{
		Models: []cost.ModelTotals{
			{Model: "gpt-4o-mini", Totals: cost.Totals{Calls: 1, Usage: cost.Usage{Input: 1000, Output: 500}, Cost: 450_000_000}},
			{Model: "x", Totals: cost.Totals{Calls: 2, Usage: cost.Usage{Input: 3}, Unpriced: 2}},
		},
		Total: cost.Totals{Calls: 3, Usage: cost.Usage{Input: 1003, Output: 500}, Cost: 450_000_000, Unpriced: 2},
	}
	var out bytes.Buffer
	if err := r.WriteText(&out); err != nil {
		t.Fatalf("WriteText() = %v", err)
	}
	want := `MODEL        CALLS  INPUT  OUTPUT  COST       UNPRICED
gpt-4o-mini  1      1000   500     $0.000450  0
x            2      3      0       $0.000000  2
TOTAL        3      1003   500     $0.000450  2
`
	if diff := cmp.Diff(want, out.String()); diff != "" {
		t.Errorf("WriteText() (-want +got):\n%s", diff)
	}
}
