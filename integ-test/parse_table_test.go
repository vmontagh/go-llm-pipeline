package integtest

import (
	"errors"
	"testing"

	"github.com/cucumber/godog"
	messages "github.com/cucumber/messages/go/v21"
	"github.com/google/go-cmp/cmp"
)

// tableFrom builds a godog data table from rows of cell values, where the first
// row is the header.
func tableFrom(rows [][]string) *godog.Table {
	t := &messages.PickleTable{Rows: make([]*messages.PickleTableRow, len(rows))}
	for i, row := range rows {
		cells := make([]*messages.PickleTableCell, len(row))
		for j, value := range row {
			cells[j] = &messages.PickleTableCell{Value: value}
		}
		t.Rows[i] = &messages.PickleTableRow{Cells: cells}
	}
	return t
}

func TestParseTextsTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		table     *godog.Table
		wantIDs   []string
		wantTexts []string
		wantErr   error
	}{
		{
			name:      "header order does not matter",
			table:     tableFrom([][]string{{"text", "id"}, {"the cat", "cat"}}),
			wantIDs:   []string{"cat"},
			wantTexts: []string{"the cat"},
		},
		{
			name:    "nil table",
			table:   nil,
			wantErr: errEmptyTable,
		},
		{
			name:    "header only",
			table:   tableFrom([][]string{{"id", "text"}}),
			wantErr: errEmptyTable,
		},
		{
			name:    "missing text column",
			table:   tableFrom([][]string{{"id"}, {"cat"}}),
			wantErr: errBadTableHeader,
		},
		{
			name:    "row shorter than header",
			table:   tableFrom([][]string{{"id", "text"}, {"cat"}}),
			wantErr: errShortTableRow,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ids, texts, err := parseTextsTable(tc.table)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("parseTextsTable() error = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil {
				return
			}
			if diff := cmp.Diff(tc.wantIDs, ids); diff != "" {
				t.Errorf("parseTextsTable() ids mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantTexts, texts); diff != "" {
				t.Errorf("parseTextsTable() texts mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
