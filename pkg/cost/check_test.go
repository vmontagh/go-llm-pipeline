package cost_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/pkg/cost"
)

// wantChatCost is 1000*0.15 + 500*0.60 micro-dollars, in picodollars, for
// testdata/cassettes.
const wantChatCost = cost.Amount(450_000_000)

func loadTestPrices(t *testing.T) *cost.PriceTable {
	t.Helper()
	prices, err := cost.LoadPrices(os.DirFS("testdata"), "prices.yaml")
	if err != nil {
		t.Fatalf("LoadPrices() = %v", err)
	}
	return prices
}

func TestCheckCassettes(t *testing.T) {
	t.Parallel()
	prices := loadTestPrices(t)
	baseline := func(c cost.Amount) *cost.Report { return &cost.Report{Total: cost.Totals{Cost: c}} }
	tests := []struct {
		name    string
		dir     string
		opts    cost.CheckOptions
		wantErr []error
	}{
		{name: "priced", dir: "testdata/cassettes"},
		{name: "unpriced", dir: "testdata/unpriced", wantErr: []error{cost.ErrUnpriced}},
		{name: "allow unpriced", dir: "testdata/unpriced", opts: cost.CheckOptions{AllowUnpriced: true}},
		{name: "regression", dir: "testdata/cassettes", opts: cost.CheckOptions{Baseline: baseline(wantChatCost - 1)}, wantErr: []error{cost.ErrCostRegression}},
		{name: "within increase", dir: "testdata/cassettes", opts: cost.CheckOptions{Baseline: baseline(wantChatCost - 1), MaxIncreasePercent: 1}},
		{name: "both", dir: "testdata", opts: cost.CheckOptions{Baseline: baseline(0)}, wantErr: []error{cost.ErrUnpriced, cost.ErrCostRegression}},
		{name: "missing directory", dir: "testdata/none", wantErr: []error{os.ErrNotExist}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := cost.CheckCassettes(os.DirFS(tt.dir), prices, tt.opts)
			if len(tt.wantErr) == 0 && err != nil {
				t.Errorf("CheckCassettes(%q) = %v, want nil", tt.dir, err)
			}
			for _, want := range tt.wantErr {
				if !errors.Is(err, want) {
					t.Errorf("CheckCassettes(%q) = %v, want %v", tt.dir, err, want)
				}
			}
		})
	}
}

func TestReportWriteText(t *testing.T) {
	t.Parallel()
	r, err := cost.CheckCassettes(os.DirFS("testdata/cassettes"), loadTestPrices(t), cost.CheckOptions{})
	if err != nil {
		t.Fatalf("CheckCassettes() = %v", err)
	}
	var out bytes.Buffer
	if err := r.WriteText(&out); err != nil {
		t.Fatalf("WriteText() = %v", err)
	}
	want := `MODEL        CALLS  INPUT  OUTPUT  COST       UNPRICED
gpt-4o-mini  1      1000   500     $0.000450  0
llama3.2     1      40     10      $0.000000  0
TOTAL        2      1040   510     $0.000450  0
`
	if diff := cmp.Diff(want, out.String()); diff != "" {
		t.Errorf("WriteText() (-want +got):\n%s", diff)
	}
}

func TestReportJSONRoundTrip(t *testing.T) {
	t.Parallel()
	want, err := cost.ReportFromCassettes(os.DirFS("testdata/cassettes"), loadTestPrices(t))
	if err != nil {
		t.Fatalf("ReportFromCassettes() = %v", err)
	}
	src, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("json.Marshal() = %v", err)
	}
	var got cost.Report
	if err := json.Unmarshal(src, &got); err != nil {
		t.Fatalf("json.Unmarshal() = %v", err)
	}
	if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("JSON round trip (-want +got):\n%s", diff)
	}
	if got.Total.Cost != wantChatCost {
		t.Errorf("Total.Cost = %d, want %d", got.Total.Cost, wantChatCost)
	}
}
