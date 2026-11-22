package cost_test

import (
	"fmt"

	"github.com/vmontagh/go-llm-pipeline/pkg/cost"
	"github.com/vmontagh/go-llm-pipeline/pkg/observ"
)

func Example() {
	prices, err := cost.NewPriceTable(map[string]cost.Rate{
		"gpt-4o-mini*": {Input: 150_000, Output: 600_000}, // $0.15 and $0.60 per million tokens
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	metrics := observ.NewMetrics()
	tracker := cost.NewTracker(prices, cost.WithMetrics(metrics))

	body := []byte(`{"model":"gpt-4o-mini-2024-07-18","usage":{"prompt_tokens":1200,"completion_tokens":300}}`)
	if model, usage, ok := cost.ParseUsage(body); ok {
		if _, err := tracker.Record("asset-1", model, usage); err != nil {
			fmt.Println(err)
			return
		}
	}
	estimated, err := cost.Estimate(cost.Heuristic{}, "gpt-4o-mini", "a prompt of 32 bytes, roughly 8.", "ok")
	if err != nil {
		fmt.Println(err)
		return
	}
	if _, err := tracker.Record("asset-2", "gpt-4o-mini", estimated); err != nil {
		fmt.Println(err)
		return
	}

	total := tracker.Report().Total
	fmt.Println(total.Calls, total.Usage.Input, total.Usage.Output, total.Cost, total.Usage.Estimated)
	fmt.Println(metrics.Value(cost.MetricCost))
	// Output:
	// 2 1208 301 $0.000362 true
	// 361800000
}
