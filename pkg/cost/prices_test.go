package cost_test

import (
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/pkg/cost"
)

func TestAmountString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   cost.Amount
		want string
	}{
		{in: 0, want: "$0.000000"},
		{in: 1_250_000_000, want: "$0.001250"},
		{in: 499_999, want: "$0.000000"},
		{in: 500_000, want: "$0.000001"},
		{in: 12_345_678_000_000, want: "$12.345678"},
		{in: -1_000_000, want: "-$0.000001"},
	}
	for _, tt := range tests {
		if got := tt.in.String(); got != tt.want {
			t.Errorf("Amount(%d).String() = %q, want %q", int64(tt.in), got, tt.want)
		}
	}
}

func TestNewPriceTableErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		rates map[string]cost.Rate
	}{
		{name: "empty model", rates: map[string]cost.Rate{"": {}}},
		{name: "bare wildcard", rates: map[string]cost.Rate{"*": {}}},
		{name: "inner wildcard", rates: map[string]cost.Rate{"gpt*4": {}}},
		{name: "negative input", rates: map[string]cost.Rate{"m": {Input: -1}}},
		{name: "negative output", rates: map[string]cost.Rate{"m": {Output: -1}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := cost.NewPriceTable(tt.rates)
			if diff := cmp.Diff(cost.ErrInvalidPrices, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("NewPriceTable(%v) error (-want +got):\n%s", tt.rates, diff)
			}
		})
	}
}

func TestPriceTableCost(t *testing.T) {
	t.Parallel()
	table, err := cost.NewPriceTable(map[string]cost.Rate{
		"gpt-4o":       {Input: 2_500_000, Output: 10_000_000},
		"gpt-4o*":      {Input: 1, Output: 1},
		"gpt-4o-mini*": {Input: 150_000, Output: 600_000},
		"big":          {Input: 1 << 62, Output: 1 << 62},
	})
	if err != nil {
		t.Fatalf("NewPriceTable() = %v", err)
	}
	tests := []struct {
		name    string
		model   string
		usage   cost.Usage
		want    cost.Amount
		wantErr error
	}{
		{name: "exact beats prefix", model: "gpt-4o", usage: cost.Usage{Input: 1000, Output: 100}, want: 3_500_000_000},
		{name: "longest prefix", model: "gpt-4o-mini-2024", usage: cost.Usage{Input: 1000, Output: 100}, want: 210_000_000},
		{name: "shorter prefix", model: "gpt-4o-2024", usage: cost.Usage{Input: 3, Output: 4}, want: 7},
		{name: "unknown", model: "claude", usage: cost.Usage{Input: 1}, wantErr: cost.ErrUnknownModel},
		{name: "negative", model: "gpt-4o", usage: cost.Usage{Output: -1}, wantErr: cost.ErrInvalidUsage},
		{name: "multiply overflow", model: "big", usage: cost.Usage{Input: 2}, wantErr: cost.ErrOverflow},
		{name: "sum overflow", model: "big", usage: cost.Usage{Input: 1, Output: 1}, wantErr: cost.ErrOverflow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := table.Cost(tt.model, tt.usage)
			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("Cost(%q, %+v) error (-want +got):\n%s", tt.model, tt.usage, diff)
			}
			if got != tt.want {
				t.Errorf("Cost(%q, %+v) = %d, want %d", tt.model, tt.usage, got, tt.want)
			}
		})
	}
}

func TestLoadPrices(t *testing.T) {
	t.Parallel()
	const src = `models:
  gpt-4o-mini: {input: 0.15, output: "0.60"}
  "llama*": {input: 0, output: 0}
  exact: {input: 1.000001, output: 12}
`
	table, err := cost.LoadPrices(fstest.MapFS{"p.yaml": {Data: []byte(src)}}, "p.yaml")
	if err != nil {
		t.Fatalf("LoadPrices() = %v", err)
	}
	tests := []struct {
		model string
		want  cost.Rate
	}{
		{model: "gpt-4o-mini", want: cost.Rate{Input: 150_000, Output: 600_000}},
		{model: "llama3.2", want: cost.Rate{}},
		{model: "exact", want: cost.Rate{Input: 1_000_001, Output: 12_000_000}},
	}
	for _, tt := range tests {
		got, ok := table.Rate(tt.model)
		if !ok {
			t.Errorf("Rate(%q) not found", tt.model)
			continue
		}
		if diff := cmp.Diff(tt.want, got); diff != "" {
			t.Errorf("Rate(%q) (-want +got):\n%s", tt.model, diff)
		}
	}
}

func TestLoadPricesErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		src  string
	}{
		{name: "unknown field", src: "models: {m: {input: 1, output: 1, cache: 1}}"},
		{name: "two documents", src: "models: {}\n---\nmodels: {}"},
		{name: "too many decimals", src: "models: {m: {input: 0.0000001, output: 1}}"},
		{name: "negative", src: "models: {m: {input: -1, output: 1}}"},
		{name: "plus sign", src: `models: {m: {input: "+1", output: 1}}`},
		{name: "signed fraction", src: `models: {m: {input: "1.+5", output: 1}}`},
		{name: "missing whole", src: `models: {m: {input: ".5", output: 1}}`},
		{name: "missing rate", src: "models: {m: {input: 1}}"},
		{name: "exponent", src: "models: {m: {input: 1e3, output: 1}}"},
		{name: "too large", src: "models: {m: {input: 9223372036855, output: 1}}"},
		{name: "bad wildcard", src: `models: {"a*b": {input: 1, output: 1}}`},
		{name: "not yaml", src: "models: ["},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := cost.LoadPrices(fstest.MapFS{"p.yaml": {Data: []byte(tt.src)}}, "p.yaml")
			if diff := cmp.Diff(cost.ErrInvalidPrices, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("LoadPrices(%q) error (-want +got):\n%s", tt.src, diff)
			}
		})
	}
}

func TestLoadPricesFilesystemErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		fsys fs.FS
	}{
		{name: "nil filesystem"},
		{name: "missing file", fsys: fstest.MapFS{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := cost.LoadPrices(tt.fsys, "p.yaml")
			if diff := cmp.Diff(cost.ErrInvalidPrices, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("LoadPrices() error (-want +got):\n%s", diff)
			}
		})
	}
}
