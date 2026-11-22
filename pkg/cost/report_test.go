package cost_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

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

var errWrite = errors.New("write failed")

// failWriter fails every write and counts the attempts.
type failWriter struct{ writes int }

func (w *failWriter) Write([]byte) (int, error) {
	w.writes++
	return 0, errWrite
}

func TestReportWriteTextError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		models []string
	}{
		// A form feed makes tabwriter flush during Write, not only in Flush.
		{name: "first row flush", models: []string{"a\fb", "c"}},
		{name: "later row flush", models: []string{"a", "c\fd"}},
		{name: "final flush", models: []string{"a", "c"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var r cost.Report
			for _, m := range tt.models {
				r.Models = append(r.Models, cost.ModelTotals{Model: m})
			}
			w := &failWriter{}
			err := r.WriteText(w)
			if diff := cmp.Diff(errWrite, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("WriteText(%q) error (-want +got):\n%s", tt.models, diff)
			}
			if w.writes != 1 {
				t.Errorf("WriteText(%q) wrote %d times, want 1 (stop at first failure)", tt.models, w.writes)
			}
		})
	}
}
