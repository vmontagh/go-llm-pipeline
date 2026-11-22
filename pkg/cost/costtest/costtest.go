// Package costtest prices the LLM calls recorded in go-vcr cassettes from a
// test, so CI catches unpriced models and cost regressions without contacting
// a provider:
//
//	func TestPromptCost(t *testing.T) {
//		prices, err := cost.LoadPrices(os.DirFS("testdata"), "prices.yaml")
//		if err != nil {
//			t.Fatal(err)
//		}
//		costtest.Check(t, os.DirFS("testdata/cassettes"), prices, costtest.Options{
//			Baseline:           &baseline, // a cost.Report saved as JSON
//			MaxIncreasePercent: 10,
//		})
//	}
package costtest

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/vmontagh/go-llm-pipeline/pkg/cost"
)

// cassetteFile is the part of a go-vcr cassette that Report reads.
type cassetteFile struct {
	Interactions []struct {
		Response struct {
			Body string `yaml:"body"`
		} `yaml:"response"`
	} `yaml:"interactions"`
}

// Report prices every recorded response in the .yaml and .yml cassettes
// under fsys. Responses that report no usage are skipped; calls on models
// without a price are counted as unpriced.
func Report(fsys fs.FS, prices *cost.PriceTable) (cost.Report, error) {
	t := cost.NewTracker(prices)
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
		return cost.Report{}, fmt.Errorf("reading cassettes: %w", err)
	}
	return t.Report(), nil
}

func recordCassette(fsys fs.FS, name string, t *cost.Tracker) error {
	src, err := fs.ReadFile(fsys, name)
	if err != nil {
		return fmt.Errorf("reading %q: %w", name, err)
	}
	var c cassetteFile
	if err := yaml.NewDecoder(bytes.NewReader(src)).Decode(&c); err != nil {
		return fmt.Errorf("decoding %q: %w", name, err)
	}
	for _, in := range c.Interactions {
		model, u, err := cost.ParseUsage([]byte(in.Response.Body))
		if errors.Is(err, cost.ErrNoUsage) {
			continue
		}
		if err != nil {
			return fmt.Errorf("parsing usage in %q: %w", name, err)
		}
		if _, err := t.Record(name, model, u); err != nil && !errors.Is(err, cost.ErrUnknownModel) {
			return fmt.Errorf("pricing %q: %w", name, err)
		}
	}
	return nil
}

// Options configures Check.
type Options struct {
	// Baseline, if set, is an earlier report, for example one saved as JSON,
	// that the cost must not exceed by more than MaxIncreasePercent.
	Baseline *cost.Report
	// MaxIncreasePercent is the allowed total cost increase over Baseline.
	MaxIncreasePercent int
	// AllowUnpriced accepts calls on models missing from the price table.
	AllowUnpriced bool
}

// Check prices the cassettes under fsys, logs the report as a table, and
// fails t on unpriced models or a cost regression over opts.Baseline. It
// returns the report, for example to save as the next baseline.
func Check(t testing.TB, fsys fs.FS, prices *cost.PriceTable, opts Options) cost.Report {
	t.Helper()
	r, err := Report(fsys, prices)
	if err != nil {
		t.Errorf("costtest: %v", err)
		return cost.Report{}
	}
	var table strings.Builder
	if err := r.WriteText(&table); err != nil {
		t.Errorf("costtest: %v", err)
	}
	t.Log("cassette cost:\n" + table.String())
	if !opts.AllowUnpriced {
		if err := r.CheckPriced(); err != nil {
			t.Errorf("costtest: %v", err)
		}
	}
	if opts.Baseline != nil {
		if err := cost.Compare(*opts.Baseline, r, opts.MaxIncreasePercent); err != nil {
			t.Errorf("costtest: %v", err)
		}
	}
	return r
}
