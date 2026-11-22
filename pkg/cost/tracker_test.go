package cost_test

import (
	"math"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/pkg/cost"
)

// fakeMetrics records counter increments.
type fakeMetrics struct {
	mu     sync.Mutex
	counts map[string]int64
}

func (m *fakeMetrics) Add(name string, delta int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.counts == nil {
		m.counts = make(map[string]int64)
	}
	m.counts[name] += delta
}

func newTable(t *testing.T, rates map[string]cost.Rate) *cost.PriceTable {
	t.Helper()
	table, err := cost.NewPriceTable(rates)
	if err != nil {
		t.Fatalf("NewPriceTable() = %v", err)
	}
	return table
}

func TestTracker(t *testing.T) {
	t.Parallel()
	metrics := &fakeMetrics{}
	tr := cost.NewTracker(newTable(t, map[string]cost.Rate{"m": {Input: 2, Output: 3}}), cost.WithMetrics(metrics))
	calls := []struct {
		asset, model string
		usage        cost.Usage
		wantErr      error
	}{
		{asset: "a", model: "m", usage: cost.Usage{Input: 10, Output: 1}},
		{asset: "a", model: "x", usage: cost.Usage{Input: 5}, wantErr: cost.ErrUnknownModel},
		{asset: "b", model: "m", usage: cost.Usage{Input: 1, Output: 1, Estimated: true}},
		{asset: "b", model: "m", usage: cost.Usage{Input: -1}, wantErr: cost.ErrInvalidUsage},
	}
	for _, c := range calls {
		if _, err := tr.Record(c.asset, c.model, c.usage); !cmp.Equal(c.wantErr, err, cmpopts.EquateErrors()) {
			t.Fatalf("Record(%q, %q, %+v) = %v, want %v", c.asset, c.model, c.usage, err, c.wantErr)
		}
	}

	wantReport := cost.Report{
		Models: []cost.ModelTotals{
			{Model: "m", Totals: cost.Totals{Calls: 2, Usage: cost.Usage{Input: 11, Output: 2, Estimated: true}, Cost: 28}},
			{Model: "x", Totals: cost.Totals{Calls: 1, Usage: cost.Usage{Input: 5}, Unpriced: 1}},
		},
		Total: cost.Totals{Calls: 3, Usage: cost.Usage{Input: 16, Output: 2, Estimated: true}, Cost: 28, Unpriced: 1},
	}
	if diff := cmp.Diff(wantReport, tr.Report()); diff != "" {
		t.Errorf("Report() (-want +got):\n%s", diff)
	}
	gotAsset, ok := tr.Asset("a")
	wantAsset := cost.Totals{Calls: 2, Usage: cost.Usage{Input: 15, Output: 1}, Cost: 23, Unpriced: 1}
	if diff := cmp.Diff(wantAsset, gotAsset); !ok || diff != "" {
		t.Errorf("Asset(%q) = %t (-want +got):\n%s", "a", ok, diff)
	}
	if _, ok := tr.Asset("missing"); ok {
		t.Errorf("Asset(%q) found, want none", "missing")
	}
	wantMetrics := map[string]int64{
		cost.MetricCalls: 3, cost.MetricInputTokens: 16, cost.MetricOutputTokens: 2,
		cost.MetricCost: 28, cost.MetricUnpriced: 1,
	}
	if diff := cmp.Diff(wantMetrics, metrics.counts); diff != "" {
		t.Errorf("metrics (-want +got):\n%s", diff)
	}
}

func TestTrackerOverflow(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		rate        cost.Rate
		first, next cost.Usage
	}{
		{name: "cost", rate: cost.Rate{Input: 1 << 62}, first: cost.Usage{Input: 1}, next: cost.Usage{Input: 1}},
		{name: "free input tokens", first: cost.Usage{Input: math.MaxInt}, next: cost.Usage{Input: 1}},
		{name: "free output tokens", first: cost.Usage{Output: math.MaxInt}, next: cost.Usage{Output: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tr := cost.NewTracker(newTable(t, map[string]cost.Rate{"m": tt.rate}))
			if _, err := tr.Record("a", "m", tt.first); err != nil {
				t.Fatalf("first Record() = %v", err)
			}
			want := tr.Report()
			_, err := tr.Record("b", "m", tt.next)
			if diff := cmp.Diff(cost.ErrOverflow, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("second Record() error (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(want, tr.Report()); diff != "" {
				t.Errorf("Report() changed after overflow (-want +got):\n%s", diff)
			}
			if _, ok := tr.Asset("b"); ok {
				t.Errorf("Asset(%q) recorded after overflow", "b")
			}
		})
	}
}

func TestTrackerConcurrent(t *testing.T) {
	t.Parallel()
	const goroutines, perGoroutine = 8, 50
	tr := cost.NewTracker(newTable(t, map[string]cost.Rate{"m": {Input: 1}}))
	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() {
			for range perGoroutine {
				if _, err := tr.Record("a", "m", cost.Usage{Input: 1}); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	if got, want := tr.Report().Total.Cost, cost.Amount(goroutines*perGoroutine); got != want {
		t.Errorf("Report().Total.Cost = %d, want %d", got, want)
	}
}

// reentrantMetrics reads the Tracker from inside Add.
type reentrantMetrics struct {
	tracker *cost.Tracker
	calls   []int
}

func (m *reentrantMetrics) Add(name string, _ int64) {
	if name == cost.MetricCalls {
		m.calls = append(m.calls, m.tracker.Report().Total.Calls)
	}
}

func TestTrackerMetricsReadTracker(t *testing.T) {
	t.Parallel()
	metrics := &reentrantMetrics{}
	metrics.tracker = cost.NewTracker(newTable(t, map[string]cost.Rate{"m": {Input: 1}}), cost.WithMetrics(metrics))

	if _, err := metrics.tracker.Record("a", "m", cost.Usage{Input: 1}); err != nil {
		t.Fatalf("Record() = %v", err)
	}

	if diff := cmp.Diff([]int{1}, metrics.calls); diff != "" {
		t.Errorf("Report().Total.Calls seen by Adder (-want +got):\n%s", diff)
	}
}

func TestCheckPriced(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		report  cost.Report
		wantErr error
	}{
		{name: "all priced", report: cost.Report{Models: []cost.ModelTotals{{Model: "m"}}}},
		{name: "unpriced", report: cost.Report{Models: []cost.ModelTotals{{Model: "m", Totals: cost.Totals{Unpriced: 1}}}}, wantErr: cost.ErrUnpriced},
	}
	for _, tt := range tests {
		err := tt.report.CheckPriced()
		if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
			t.Errorf("%s: CheckPriced() (-want +got):\n%s", tt.name, diff)
		}
	}
}

func TestCompare(t *testing.T) {
	t.Parallel()
	report := func(c cost.Amount) cost.Report { return cost.Report{Total: cost.Totals{Cost: c}} }
	tests := []struct {
		name        string
		base, cur   cost.Amount
		maxIncrease int
		wantErr     error
	}{
		{name: "equal", base: 100, cur: 100},
		{name: "within", base: 100, cur: 110, maxIncrease: 10},
		{name: "over", base: 100, cur: 111, maxIncrease: 10, wantErr: cost.ErrCostRegression},
		{name: "cheaper", base: 100, cur: 1},
		{name: "zero baseline", cur: 1, wantErr: cost.ErrCostRegression},
		{name: "disabled", base: 1, cur: 1000, maxIncrease: -1},
		{name: "huge current", base: 1, cur: cost.Amount(1 << 62), wantErr: cost.ErrCostRegression},
		{name: "huge limit", base: cost.Amount(1 << 62), cur: cost.Amount(1 << 62), maxIncrease: 10},
		{name: "large baseline regressed", base: 100_000_000_000_000_000, cur: 101_000_000_000_000_000, wantErr: cost.ErrCostRegression},
		{name: "large baseline within", base: 100_000_000_000_000_000, cur: 101_000_000_000_000_000, maxIncrease: 1},
		{name: "max percent", base: 100, cur: cost.Amount(math.MaxInt), maxIncrease: math.MaxInt},
		{name: "max costs", base: math.MaxInt64, cur: math.MaxInt64},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := cost.Compare(report(tt.base), report(tt.cur), tt.maxIncrease)
			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Compare(%d, %d, %d) (-want +got):\n%s", tt.base, tt.cur, tt.maxIncrease, diff)
			}
		})
	}
}
