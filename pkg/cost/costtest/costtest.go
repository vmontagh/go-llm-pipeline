// Package costtest catches LLM cost regressions in ordinary Go tests, offline
// and for free.
//
// # How it works
//
// It never calls a provider. It reads the go-vcr cassettes that pkg/llmvcr
// (or go-vcr) already recorded: each recorded response body carries the
// provider's token counts, for example
//
//	response:
//	  body: '{"model":"gpt-4o-mini","usage":{"prompt_tokens":1000,"completion_tokens":500}}'
//
// [Check] parses those counts with [cost.ParseUsage], multiplies them by
// your price table, sums them per model, and compares the total with a
// baseline report you saved earlier. The cost changes only when the cassettes
// are re-recorded, for example after a prompt change, so the check flags the
// change in the same pull request that caused it.
//
// # Workflow
//
// Step 1: record cassettes once against the real provider (llmvcr record
// mode) and commit them, as for any replay test.
//
// Step 2: commit a price table in USD per million tokens (see
// [cost.LoadPrices]), for example testdata/prices.yaml:
//
//	models:
//	  gpt-4o-mini: {input: 0.15, output: 0.60}
//	  "llama*": {input: 0, output: 0}
//
// Step 3: add a test that checks the cassettes against a committed baseline
// and rewrites the baseline on request:
//
//	var updateCost = flag.Bool("update-cost", false, "rewrite testdata/cost-baseline.json")
//
//	func TestPromptCost(t *testing.T) {
//		prices, err := cost.LoadPrices(os.DirFS("testdata"), "prices.yaml")
//		if err != nil {
//			t.Fatal(err)
//		}
//		opts := costtest.Options{MaxIncreasePercent: 10}
//		if !*updateCost {
//			src, err := os.ReadFile("testdata/cost-baseline.json")
//			if err != nil {
//				t.Fatalf("reading baseline (run with -update-cost to create it): %v", err)
//			}
//			var base cost.Report
//			if err := json.Unmarshal(src, &base); err != nil {
//				t.Fatal(err)
//			}
//			opts.Baseline = &base
//		}
//		report := costtest.Check(t, os.DirFS("testdata/cassettes"), prices, opts)
//		// Never overwrite a good baseline with a failed or partial report.
//		if *updateCost && !t.Failed() {
//			src, err := json.MarshalIndent(report, "", "  ")
//			if err != nil {
//				t.Fatal(err)
//			}
//			if err := os.WriteFile("testdata/cost-baseline.json", src, 0o644); err != nil {
//				t.Fatal(err)
//			}
//		}
//	}
//
// Step 4: run the test once with the flag and commit the baseline. Name only
// the package that defines the flag (here ./myapp); with ./... every other
// test binary rejects the unknown flag:
//
//	go test ./myapp -run TestPromptCost -update-cost
//
// Responses without provider-reported usage (for example a stream the
// caller stopped reading early) are skipped and add nothing to the total, so compare the CALLS
// column with the number of recorded calls if a model looks under-counted.
//
// From then on, the test fails when re-recorded cassettes cost more than
// MaxIncreasePercent above the baseline, or when a call uses a model missing
// from the price table (set AllowUnpriced to accept that). Run it with -v to
// see the per-model table. When an increase is intended, rerun with
// -update-cost and commit the new baseline with the change, so reviewers see
// the cost difference in the diff.
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
