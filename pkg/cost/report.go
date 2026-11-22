package cost

import (
	"fmt"
	"io"
	"math/big"
	"text/tabwriter"
)

// percent is the denominator of a percentage.
const percent = 100

// Compare returns ErrCostRegression when current costs more than baseline by
// over maxIncreasePercent percent. A zero baseline allows no cost at all
// unless maxIncreasePercent is negative, which disables the check.
func Compare(baseline, current Report, maxIncreasePercent int) error {
	if maxIncreasePercent < 0 {
		return nil
	}
	// cur > base*(100+p)/100, multiplied out in big integers so neither
	// side can overflow.
	limit := new(big.Int).Mul(
		big.NewInt(int64(baseline.Total.Cost)),
		new(big.Int).Add(big.NewInt(percent), big.NewInt(int64(maxIncreasePercent))),
	)
	scaled := new(big.Int).Mul(big.NewInt(int64(current.Total.Cost)), big.NewInt(percent))
	if scaled.Cmp(limit) > 0 {
		return fmt.Errorf("%w: %s to %s, over %d%%", ErrCostRegression, baseline.Total.Cost, current.Total.Cost, maxIncreasePercent)
	}
	return nil
}

// tabPadding is the space between WriteText columns.
const tabPadding = 2

// WriteText writes r as an aligned table with one row per model and a total.
// Use encoding/json for a machine-readable form.
func (r Report) WriteText(w io.Writer) error {
	tw := tabwriter.NewWriter(w, 0, 0, tabPadding, ' ', 0)
	fmt.Fprintln(tw, "MODEL\tCALLS\tINPUT\tOUTPUT\tCOST\tUNPRICED")
	for _, m := range r.Models {
		writeRow(tw, m.Model, m.Totals)
	}
	writeRow(tw, "TOTAL", r.Total)
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("writing report: %w", err)
	}
	return nil
}

func writeRow(w io.Writer, name string, t Totals) {
	fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%s\t%d\n", name, t.Calls, t.Usage.Input, t.Usage.Output, t.Cost, t.Unpriced)
}
