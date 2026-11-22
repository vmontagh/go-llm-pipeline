package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/pkg/cost"
)

const (
	pricesFile  = "testdata/prices.yaml"
	cassetteDir = "testdata/cassettes"
	// wantChatCost is 1000*0.15 + 500*0.60 micro-dollars, in picodollars.
	wantChatCost = cost.Amount(450_000_000)
)

func TestRunText(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := run([]string{"-cassettes", cassetteDir, "-prices", pricesFile}, &out, &bytes.Buffer{}); err != nil {
		t.Fatalf("run() = %v", err)
	}
	want := `MODEL        CALLS  INPUT  OUTPUT  COST       UNPRICED
gpt-4o-mini  1      1000   500     $0.000450  0
llama3.2     1      40     10      $0.000000  0
TOTAL        2      1040   510     $0.000450  0
`
	if diff := cmp.Diff(want, out.String()); diff != "" {
		t.Errorf("run() output (-want +got):\n%s", diff)
	}
}

func writeBaseline(t *testing.T, c cost.Amount) string {
	t.Helper()
	src, err := json.Marshal(cost.Report{Total: cost.Totals{Cost: c}})
	if err != nil {
		t.Fatalf("json.Marshal() = %v", err)
	}
	name := filepath.Join(t.TempDir(), "baseline.json")
	if err := os.WriteFile(name, src, 0o600); err != nil {
		t.Fatalf("os.WriteFile() = %v", err)
	}
	return name
}

func TestRunJSONRoundTrip(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := run([]string{"-cassettes", cassetteDir, "-prices", pricesFile, "-json"}, &out, &bytes.Buffer{}); err != nil {
		t.Fatalf("run(-json) = %v", err)
	}
	var got cost.Report
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decoding run(-json) output: %v", err)
	}
	if got.Total.Cost != wantChatCost {
		t.Errorf("run(-json) total cost = %d, want %d", got.Total.Cost, wantChatCost)
	}
}

func TestRunErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		args    []string
		wantErr error
	}{
		{name: "missing flags", wantErr: errUsage},
		{name: "unknown flag", args: []string{"-nope"}, wantErr: errUsage},
		{name: "extra argument", args: []string{"-cassettes", cassetteDir, "-prices", pricesFile, "x"}, wantErr: errUsage},
		{name: "negative increase", args: []string{"-cassettes", cassetteDir, "-prices", pricesFile, "-max-increase", "-1"}, wantErr: errUsage},
		{name: "missing prices", args: []string{"-cassettes", cassetteDir, "-prices", "testdata/none.yaml"}, wantErr: cost.ErrInvalidPrices},
		{name: "missing cassettes", args: []string{"-cassettes", "testdata/none", "-prices", pricesFile}, wantErr: os.ErrNotExist},
		{name: "unpriced", args: []string{"-cassettes", "testdata/unpriced", "-prices", pricesFile}, wantErr: cost.ErrUnpriced},
		{name: "regression", args: []string{"-cassettes", cassetteDir, "-prices", pricesFile, "-baseline", writeBaseline(t, wantChatCost-1)}, wantErr: cost.ErrCostRegression},
		{name: "missing baseline", args: []string{"-cassettes", cassetteDir, "-prices", pricesFile, "-baseline", "testdata/none.json"}, wantErr: os.ErrNotExist},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := run(tt.args, &bytes.Buffer{}, &bytes.Buffer{})
			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("run(%q) error (-want +got):\n%s", tt.args, diff)
			}
		})
	}
}

func TestRunAllowed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
	}{
		{name: "allow unpriced", args: []string{"-cassettes", "testdata/unpriced", "-prices", pricesFile, "-allow-unpriced"}},
		{name: "within increase", args: []string{"-cassettes", cassetteDir, "-prices", pricesFile, "-baseline", writeBaseline(t, wantChatCost-1), "-max-increase", "1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := run(tt.args, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Errorf("run(%q) = %v", tt.args, err)
			}
		})
	}
}
