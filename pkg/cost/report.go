package cost

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"path"
	"text/tabwriter"

	"gopkg.in/yaml.v3"
)

// cassetteFile is the part of a go-vcr cassette the report reads.
type cassetteFile struct {
	Interactions []struct {
		Response struct {
			Body string `yaml:"body"`
		} `yaml:"response"`
	} `yaml:"interactions"`
}

// ReportFromCassettes prices every recorded response in the .yaml and .yml
// cassettes under fsys, without contacting a provider. Responses that report
// no usage are skipped.
func ReportFromCassettes(fsys fs.FS, prices *PriceTable) (Report, error) {
	t := NewTracker(prices)
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || (path.Ext(name) != ".yaml" && path.Ext(name) != ".yml") {
			return nil
		}
		return recordCassette(fsys, name, t)
	})
	if err != nil {
		return Report{}, fmt.Errorf("reading cassettes: %w", err)
	}
	return t.Report(), nil
}

func recordCassette(fsys fs.FS, name string, t *Tracker) error {
	src, err := fs.ReadFile(fsys, name)
	if err != nil {
		return fmt.Errorf("reading %q: %w", name, err)
	}
	var c cassetteFile
	if err := yaml.NewDecoder(bytes.NewReader(src)).Decode(&c); err != nil {
		return fmt.Errorf("decoding %q: %w", name, err)
	}
	for _, in := range c.Interactions {
		model, u, err := ParseUsage([]byte(in.Response.Body))
		if errors.Is(err, ErrNoUsage) {
			continue
		}
		if err != nil {
			return fmt.Errorf("parsing usage in %q: %w", name, err)
		}
		if _, err := t.Record(name, model, u); err != nil && !errors.Is(err, ErrUnknownModel) {
			return fmt.Errorf("pricing %q: %w", name, err)
		}
	}
	return nil
}

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

// CheckOptions configures CheckCassettes.
type CheckOptions struct {
	// Baseline, if set, is an earlier report, for example one saved as JSON,
	// that the cost must not exceed by more than MaxIncreasePercent.
	Baseline *Report
	// MaxIncreasePercent is the allowed total cost increase over Baseline.
	MaxIncreasePercent int
	// AllowUnpriced accepts calls on models missing from the price table.
	AllowUnpriced bool
}

// CheckCassettes prices the cassettes under fsys and checks the result, so a
// test can gate CI on cost without contacting a provider. It returns the
// report together with ErrUnpriced, ErrCostRegression, or both joined.
func CheckCassettes(fsys fs.FS, prices *PriceTable, opts CheckOptions) (Report, error) {
	r, err := ReportFromCassettes(fsys, prices)
	if err != nil {
		return Report{}, err
	}
	var errs []error
	if !opts.AllowUnpriced {
		errs = append(errs, r.CheckPriced())
	}
	if opts.Baseline != nil {
		errs = append(errs, Compare(*opts.Baseline, r, opts.MaxIncreasePercent))
	}
	return r, errors.Join(errs...)
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
