package devhealthsource

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

type c11165PanicRows struct{ closed *int }

func (r c11165PanicRows) Next() bool        { return true }
func (r c11165PanicRows) Scan(...any) error { panic("scan panic") }
func (r c11165PanicRows) Err() error        { return nil }
func (r c11165PanicRows) Close() error      { *r.closed++; return nil }

type c11165Client struct{ closed *int }

func (c c11165Client) Query(context.Context, string, []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	return c11165PanicRows{closed: c.closed}, nil
}

func c11165ExpectPanicAndClose(t *testing.T, closed *int, call func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("the scan panic did not reach the test")
		}
		if *closed != 1 {
			t.Fatalf("rows closed %d times after a panic in Scan, want 1", *closed)
		}
	}()
	call()
}

func TestCHAOS11165RowsCloseWhenScanPanics(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("countKinds", func(t *testing.T) {
		closed := 0
		c11165ExpectPanicAndClose(t, &closed, func() {
			_, _ = countKinds(ctx, c11165Client{&closed}, "org", []kindCountStatement{{kind: contractsv1.ContextFabricSubjectRepository, sql: "q"}})
		})
	})
	t.Run("runEdgePopulations", func(t *testing.T) {
		closed := 0
		c11165ExpectPanicAndClose(t, &closed, func() {
			_, _ = runEdgePopulations(ctx, c11165Client{&closed}, "org", []edgePopulation{{relation: contractsv1.ContextFabricRelationshipBelongsToPullRequest, sql: "q"}})
		})
	})
	t.Run("readIncidentDescriptions", func(t *testing.T) {
		closed := 0
		entity := &contractsv1.ContextFabricEntityProjection{Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectIncident, CanonicalID: "incident:1"}}
		page := []candidate{{sortKey: "1", entity: entity}}
		c11165ExpectPanicAndClose(t, &closed, func() {
			_ = readIncidentDescriptions(ctx, c11165Client{&closed}, "org", page, map[string]time.Time{"1": time.Unix(1, 0).UTC()})
		})
	})
}
