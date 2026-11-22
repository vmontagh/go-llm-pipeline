package costtest_test

import (
	"errors"
	"os"
	"testing"
	"testing/fstest"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/pkg/cost"
	"github.com/vmontagh/go-llm-pipeline/pkg/cost/costtest"
)

// wantChatCost is 1000*0.15 + 500*0.60 micro-dollars, in picodollars, for
// testdata/cassettes.
const wantChatCost = cost.Amount(450_000_000)

func newTable(t *testing.T, rates map[string]cost.Rate) *cost.PriceTable {
	t.Helper()
	table, err := cost.NewPriceTable(rates)
	if err != nil {
		t.Fatalf("NewPriceTable() = %v", err)
	}
	return table
}

func TestReport(t *testing.T) {
	t.Parallel()
	const cassette = `version: 2
interactions:
  - response:
      body: '{"model":"m","usage":{"prompt_tokens":10,"completion_tokens":2}}'
  - response:
      body: '{"status":"ok"}'
  - response:
      body: '{"model":"x","prompt_eval_count":3,"eval_count":1}'
`
	fsys := fstest.MapFS{
		"a.yaml":    {Data: []byte(cassette)},
		"sub/b.yml": {Data: []byte(cassette)},
		"notes.txt": {Data: []byte("ignored")},
	}
	got, err := costtest.Report(fsys, newTable(t, map[string]cost.Rate{"m": {Input: 1, Output: 5}}))
	if err != nil {
		t.Fatalf("Report() = %v", err)
	}
	want := cost.Report{
		Models: []cost.ModelTotals{
			{Model: "m", Totals: cost.Totals{Calls: 2, Usage: cost.Usage{Input: 20, Output: 4}, Cost: 40}},
			{Model: "x", Totals: cost.Totals{Calls: 2, Usage: cost.Usage{Input: 6, Output: 2}, Unpriced: 2}},
		},
		Total: cost.Totals{Calls: 4, Usage: cost.Usage{Input: 26, Output: 6}, Cost: 40, Unpriced: 2},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Report() (-want +got):\n%s", diff)
	}
}

func TestReportErrors(t *testing.T) {
	t.Parallel()
	table := newTable(t, map[string]cost.Rate{"m": {Input: 1 << 62}})
	tests := []struct {
		name    string
		fsys    fstest.MapFS
		wantErr error
	}{
		{name: "bad yaml", fsys: fstest.MapFS{"a.yaml": {Data: []byte("interactions: [")}}},
		{name: "negative usage", fsys: fstest.MapFS{"a.yaml": {Data: []byte(`interactions: [{response: {body: '{"model":"m","prompt_eval_count":-1}'}}]`)}}, wantErr: cost.ErrInvalidUsage},
		{name: "overflow", fsys: fstest.MapFS{"a.yaml": {Data: []byte(`interactions: [{response: {body: '{"model":"m","prompt_eval_count":2}'}}]`)}}, wantErr: cost.ErrOverflow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := costtest.Report(tt.fsys, table)
			if err == nil {
				t.Fatal("Report() = nil, want error")
			}
			if tt.wantErr != nil {
				if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
					t.Errorf("Report() error (-want +got):\n%s", diff)
				}
			}
		})
	}
}

// fakeTB records the errors a check reports instead of failing the test.
type fakeTB struct {
	testing.TB
	errs []error
	logs int
}

func (f *fakeTB) Helper() {}

func (f *fakeTB) Log(...any) { f.logs++ }

func (f *fakeTB) Errorf(_ string, args ...any) {
	for _, a := range args {
		if err, ok := a.(error); ok {
			f.errs = append(f.errs, err)
		}
	}
}

func loadTestPrices(t *testing.T) *cost.PriceTable {
	t.Helper()
	prices, err := cost.LoadPrices(os.DirFS("testdata"), "prices.yaml")
	if err != nil {
		t.Fatalf("LoadPrices() = %v", err)
	}
	return prices
}

func TestCheck(t *testing.T) {
	t.Parallel()
	prices := loadTestPrices(t)
	baseline := func(c cost.Amount) *cost.Report { return &cost.Report{Total: cost.Totals{Cost: c}} }
	tests := []struct {
		name     string
		dir      string
		opts     costtest.Options
		wantErrs []error
		wantCost cost.Amount
	}{
		{name: "priced", dir: "testdata/cassettes", wantCost: wantChatCost},
		{name: "unpriced", dir: "testdata/unpriced", wantErrs: []error{cost.ErrUnpriced}},
		{name: "allow unpriced", dir: "testdata/unpriced", opts: costtest.Options{AllowUnpriced: true}},
		{name: "regression", dir: "testdata/cassettes", opts: costtest.Options{Baseline: baseline(wantChatCost - 1)}, wantErrs: []error{cost.ErrCostRegression}, wantCost: wantChatCost},
		{name: "within increase", dir: "testdata/cassettes", opts: costtest.Options{Baseline: baseline(wantChatCost - 1), MaxIncreasePercent: 1}, wantCost: wantChatCost},
		{name: "both", dir: "testdata", opts: costtest.Options{Baseline: baseline(0)}, wantErrs: []error{cost.ErrUnpriced, cost.ErrCostRegression}, wantCost: wantChatCost},
		{name: "missing directory", dir: "testdata/none", wantErrs: []error{os.ErrNotExist}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tb := &fakeTB{}
			got := costtest.Check(tb, os.DirFS(tt.dir), prices, tt.opts)
			if len(tb.errs) != len(tt.wantErrs) {
				t.Fatalf("Check(%q) reported %v, want %v", tt.dir, tb.errs, tt.wantErrs)
			}
			for i, want := range tt.wantErrs {
				if !errors.Is(tb.errs[i], want) {
					t.Errorf("Check(%q) error %d = %v, want %v", tt.dir, i, tb.errs[i], want)
				}
			}
			if got.Total.Cost != tt.wantCost {
				t.Errorf("Check(%q) cost = %d, want %d", tt.dir, got.Total.Cost, tt.wantCost)
			}
			if tb.logs == 0 && len(tt.wantErrs) == 0 {
				t.Errorf("Check(%q) logged no report", tt.dir)
			}
		})
	}
}
