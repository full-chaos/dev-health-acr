package devhealthsource

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

type observedRead struct {
	table string
	rows  int
	more  bool
}

// A statement that says rows lie beyond its limit but returned none is refused
// by the page bound. It still ran: the paged and dimension reads report it
// before the bound refuses it, so its more value keeps the read from looking
// drained.
func TestBoundRefusedStatementIsStillReported(t *testing.T) {
	refused := entityTable{name: "teams", dimension: true, query: func(context.Context, contextpacket.ClickHouseQueryClient, string, cursorState, int) ([]candidate, bool, error) {
		return nil, true, nil
	}}
	planWith := func(seen *[]observedRead) sourcePlan {
		return sourcePlan{
			client: keysetRows{}, source: "table_read_site_test", version: "v1",
			tables: []entityTable{refused},
			logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
			observeRead: func(table string, rows []candidate, more bool) {
				*seen = append(*seen, observedRead{table: table, rows: len(rows), more: more})
			},
		}
	}
	want := []observedRead{{table: "teams", rows: 0, more: true}}
	check := func(t *testing.T, path string, seen []observedRead, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: the bound did not refuse the read", path)
		}
		if len(seen) != 1 || seen[0] != want[0] {
			t.Fatalf("%s reported %v, want %v", path, seen, want)
		}
	}

	var paged []observedRead
	_, _, err := planWith(&paged).pagedBatch(context.Background(), "org", "", cursorState{}, false)
	check(t, "pagedBatch", paged, err)

	var dimension []observedRead
	_, _, err = planWith(&dimension).dimensionBatch(context.Background(), "org", "", dimensionPosition{Tables: []string{"teams"}})
	check(t, "dimensionBatch", dimension, err)
}
