package devhealthsource

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
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

	t.Run("paged", func(t *testing.T) {
		var seen []observedRead
		_, _, err := planWith(&seen).pagedBatch(context.Background(), "org", "", cursorState{}, false)
		check(t, "pagedBatch", seen, err)
	})
	t.Run("dimension", func(t *testing.T) {
		var seen []observedRead
		_, _, err := planWith(&seen).dimensionBatch(context.Background(), "org", "", dimensionPosition{Tables: []string{"teams"}})
		check(t, "dimensionBatch", seen, err)
	})
}

// A dimension page that does not fit beside what the batch holds is left for
// the next batch, but its statement ran: it is reported before the batch-bounds
// check, so the page read and left over is not silent.
func TestDimensionPageLeftForTheNextBatchIsStillReported(t *testing.T) {
	const perPage = 600
	at := time.Date(2026, 11, 9, 7, 0, 0, 0, time.UTC)
	wide := entityTable{name: "teams", dimension: true, query: func(_ context.Context, _ contextpacket.ClickHouseQueryClient, _ string, cursor cursorState, _ int) ([]candidate, bool, error) {
		start := 0
		if cursor.After != "" {
			start = perPage
		}
		rows := make([]candidate, 0, perPage)
		for n := start; n < start+perPage; n++ {
			rows = append(rows, candidate{sortKey: fmt.Sprintf("team-%05d", n), cursorAt: at, entity: &contractsv1.ContextFabricEntityProjection{}})
		}
		return rows, true, nil
	}}
	var seen []observedRead
	plan := sourcePlan{
		client: keysetRows{}, source: "table_read_site_test", version: "v1",
		tables: []entityTable{wide},
		logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		observeRead: func(table string, rows []candidate, more bool) {
			seen = append(seen, observedRead{table: table, rows: len(rows), more: more})
		},
	}
	_, _, _ = plan.dimensionBatch(context.Background(), "org", "", dimensionPosition{Tables: []string{"teams"}})
	want := []observedRead{{table: "teams", rows: perPage, more: true}, {table: "teams", rows: perPage, more: true}}
	if len(seen) != len(want) || seen[0] != want[0] || seen[1] != want[1] {
		t.Fatalf("dimensionBatch reported %v, want %v (the second page is read and left for the next batch)", seen, want)
	}
}
