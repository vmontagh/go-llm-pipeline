// Command costreport prices the LLM calls recorded in go-vcr cassettes, so CI
// can see what a test suite's prompts cost and fail on cost regressions
// without calling a provider.
//
// Usage:
//
//	costreport -cassettes testdata/cassettes -prices prices.yaml \
//	    [-baseline baseline.json -max-increase 10] [-json] [-allow-unpriced]
//
// Prices are caller-supplied; see cost.LoadPrices for the format. Save a
// -json report as the baseline for later runs. Calls on models missing from
// the price table fail the run unless -allow-unpriced is set.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/vmontagh/go-llm-pipeline/pkg/cost"
)

// errUsage is returned for missing or invalid flags.
var errUsage = errors.New("usage")

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "costreport:", err)
		os.Exit(1)
	}
}

// config holds the parsed flags.
type config struct {
	cassettes     string
	prices        string
	baseline      string
	maxIncrease   int
	json          bool
	allowUnpriced bool
}

func parseFlags(args []string, stderr io.Writer) (config, error) {
	var c config
	fs := flag.NewFlagSet("costreport", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&c.cassettes, "cassettes", "", "directory of go-vcr cassettes to price (required)")
	fs.StringVar(&c.prices, "prices", "", "YAML price table in USD per million tokens (required)")
	fs.StringVar(&c.baseline, "baseline", "", "JSON report from an earlier -json run to compare against")
	fs.IntVar(&c.maxIncrease, "max-increase", 0, "allowed total cost increase over -baseline, in percent")
	fs.BoolVar(&c.json, "json", false, "print the report as JSON")
	fs.BoolVar(&c.allowUnpriced, "allow-unpriced", false, "do not fail on calls to models without a price")
	if err := fs.Parse(args); err != nil {
		return config{}, fmt.Errorf("%w: %w", errUsage, err)
	}
	if c.cassettes == "" || c.prices == "" || fs.NArg() > 0 {
		return config{}, fmt.Errorf("%w: -cassettes and -prices are required and no arguments are accepted", errUsage)
	}
	if c.maxIncrease < 0 {
		return config{}, fmt.Errorf("%w: -max-increase must not be negative", errUsage)
	}
	return c, nil
}

func run(args []string, stdout, stderr io.Writer) error {
	c, err := parseFlags(args, stderr)
	if err != nil {
		return err
	}
	prices, err := cost.LoadPrices(os.DirFS(filepath.Dir(c.prices)), filepath.Base(c.prices))
	if err != nil {
		return fmt.Errorf("loading prices: %w", err)
	}
	report, err := cost.ReportFromCassettes(os.DirFS(c.cassettes), prices)
	if err != nil {
		return fmt.Errorf("pricing cassettes: %w", err)
	}
	if err := write(stdout, report, c.json); err != nil {
		return fmt.Errorf("writing report: %w", err)
	}
	var errs []error
	if !c.allowUnpriced {
		errs = append(errs, report.CheckPriced())
	}
	if c.baseline != "" {
		errs = append(errs, compare(c.baseline, report, c.maxIncrease))
	}
	return errors.Join(errs...)
}

func compare(name string, report cost.Report, maxIncrease int) error {
	src, err := os.ReadFile(name)
	if err != nil {
		return fmt.Errorf("reading baseline: %w", err)
	}
	var base cost.Report
	if err := json.Unmarshal(src, &base); err != nil {
		return fmt.Errorf("decoding baseline %q: %w", name, err)
	}
	if err := cost.Compare(base, report, maxIncrease); err != nil {
		return fmt.Errorf("comparing with baseline: %w", err)
	}
	return nil
}

func write(w io.Writer, r cost.Report, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "MODEL\tCALLS\tINPUT\tOUTPUT\tCOST\tUNPRICED")
	for _, m := range r.Models {
		writeRow(tw, m.Model, m.Totals)
	}
	writeRow(tw, "TOTAL", r.Total)
	return tw.Flush()
}

func writeRow(w io.Writer, name string, t cost.Totals) {
	fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%s\t%d\n", name, t.Calls, t.Usage.Input, t.Usage.Output, t.Cost, t.Unpriced)
}
