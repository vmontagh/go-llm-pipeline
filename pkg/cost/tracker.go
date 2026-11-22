package cost

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
)

// Totals aggregates calls, usage, and cost.
type Totals struct {
	Calls int    `json:"calls"`
	Usage Usage  `json:"usage"`
	Cost  Amount `json:"cost_picodollars"`
	// Unpriced counts calls whose model had no price; their tokens are in
	// Usage but not in Cost.
	Unpriced int `json:"unpriced,omitempty"`
}

// add returns t plus one call, or ErrOverflow if any total does not fit.
func (t Totals) add(u Usage, c Amount, priced bool) (Totals, error) {
	usage, err := t.Usage.Add(u)
	if err != nil {
		return Totals{}, err
	}
	cost, ok := checkedAdd(t.Cost, c)
	if !ok {
		return Totals{}, fmt.Errorf("%w: cost", ErrOverflow)
	}
	t.Calls++
	t.Usage, t.Cost = usage, cost
	if !priced {
		t.Unpriced++
	}
	return t, nil
}

// ModelTotals is the Totals of one model.
type ModelTotals struct {
	Model string `json:"model"`
	Totals
}

// Report is the usage and cost of a batch, per model and in total.
type Report struct {
	Models []ModelTotals `json:"models"`
	Total  Totals        `json:"total"`
}

// CheckPriced returns ErrUnpriced naming the models that had no price.
func (r Report) CheckPriced() error {
	var models []string
	for _, m := range r.Models {
		if m.Unpriced > 0 {
			models = append(models, m.Model)
		}
	}
	if len(models) > 0 {
		return fmt.Errorf("%w: models %q", ErrUnpriced, models)
	}
	return nil
}

// Adder receives counter increments; *observ.Metrics implements it.
type Adder interface {
	Add(name string, delta int64)
}

// Metric names a Tracker adds to its Adder.
const (
	MetricCalls        = "llm_calls"
	MetricInputTokens  = "llm_input_tokens"  //nolint:gosec // G101: a metric name, not a credential.
	MetricOutputTokens = "llm_output_tokens" //nolint:gosec // G101: a metric name, not a credential.
	MetricCost         = "llm_cost_picodollars"
	MetricUnpriced     = "llm_unpriced_calls"
)

// TrackerOption configures a Tracker.
type TrackerOption func(*Tracker)

// WithMetrics makes the Tracker add every recorded call to m, for example an
// *observ.Metrics served on the observability endpoint.
func WithMetrics(m Adder) TrackerOption {
	return func(t *Tracker) { t.metrics = m }
}

// Tracker aggregates the usage and cost of a batch per asset and per model.
// Use one Tracker per batch. It is safe for concurrent use.
type Tracker struct {
	prices  *PriceTable
	metrics Adder

	mu      sync.Mutex
	byAsset map[string]Totals
	byModel map[string]Totals
	total   Totals
}

// NewTracker returns a Tracker that prices calls with prices.
func NewTracker(prices *PriceTable, opts ...TrackerOption) *Tracker {
	t := &Tracker{prices: prices, byAsset: make(map[string]Totals), byModel: make(map[string]Totals)}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// Record adds one call on model with usage u for asset, an opaque
// caller-chosen ID that must not contain personal data. It returns the call's
// cost. A model without a price is still recorded, counted as unpriced, and
// reported with ErrUnknownModel; other errors leave the totals unchanged.
func (t *Tracker) Record(asset, model string, u Usage) (Amount, error) {
	c, err := t.prices.Cost(model, u)
	if err != nil && !errors.Is(err, ErrUnknownModel) {
		return 0, err
	}
	priced := err == nil
	t.mu.Lock()
	defer t.mu.Unlock()
	// Compute every total before committing any, so an overflow leaves the
	// tracker unchanged.
	next := [...]Totals{t.byAsset[asset], t.byModel[model], t.total}
	for i := range next {
		var addErr error
		if next[i], addErr = next[i].add(u, c, priced); addErr != nil {
			return 0, fmt.Errorf("recording %q: %w", model, addErr)
		}
	}
	t.byAsset[asset], t.byModel[model], t.total = next[0], next[1], next[2]
	if t.metrics != nil {
		t.metrics.Add(MetricCalls, 1)
		t.metrics.Add(MetricInputTokens, int64(u.Input))
		t.metrics.Add(MetricOutputTokens, int64(u.Output))
		t.metrics.Add(MetricCost, int64(c))
		if !priced {
			t.metrics.Add(MetricUnpriced, 1)
		}
	}
	return c, err
}

// Asset returns the totals recorded for asset, or false if none.
func (t *Tracker) Asset(asset string) (Totals, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	totals, ok := t.byAsset[asset]
	return totals, ok
}

// Report returns the per-model and total usage and cost so far, with models
// in name order.
func (t *Tracker) Report() Report {
	t.mu.Lock()
	defer t.mu.Unlock()
	r := Report{Total: t.total}
	for _, m := range slices.Sorted(maps.Keys(t.byModel)) {
		r.Models = append(r.Models, ModelTotals{Model: m, Totals: t.byModel[m]})
	}
	return r
}
