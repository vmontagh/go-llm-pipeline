package cost

import (
	"math"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// TestTotalsAddCountOverflow is white-box because reaching math.MaxInt calls
// through Tracker.Record would take billions of calls.
func TestTotalsAddCountOverflow(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		totals Totals
		priced bool
	}{
		{name: "calls", totals: Totals{Calls: math.MaxInt}, priced: true},
		{name: "unpriced", totals: Totals{Unpriced: math.MaxInt}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := tt.totals.add(Usage{}, 0, tt.priced)
			if diff := cmp.Diff(ErrOverflow, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("%+v.add() error (-want +got):\n%s", tt.totals, diff)
			}
		})
	}
}
