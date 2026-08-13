package devhealthsource_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	runtimeclickhouse "github.com/full-chaos/dev-health-acr/internal/runtime/clickhouse"
)

// TestProductionSchemaSnapshotStaysFreshAgainstLiveClickHouse is CHAOS-3789
// codex round-1 F1: productionColumns (schema_parity_integration_test.go)
// is a snapshot dated 2026-08-13. TestLiveSchemaParityAcrossEveryProducer
// only proves the Go code matches that snapshot -- if production changes a
// scanned column's type after that date and nobody updates the snapshot,
// that test keeps using the stale type and false-passes while a live
// Scan() would fail. This test is the snapshot's own freshness guard: given
// a real ClickHouse DSN, it re-reads system.columns from THAT server and
// fails -- naming the exact table/column and telling the maintainer to
// regenerate productionColumns -- the moment the two disagree.
//
// Gated behind ACR_CLICKHOUSE_INTEGRATION_DSN, the same env var
// internal/runtime/clickhouse's own integration suite uses
// (integration_support_test.go), rather than testcontainers: freshness can
// only be checked against a database that already carries production's
// real schema, which a fresh empty container never does. Unset, this test
// skips (or fails if ACR_CLICKHOUSE_INTEGRATION_REQUIRED=1) --
// TestLiveSchemaParityAcrossEveryProducer's testcontainer-only guarantee
// still runs everywhere.
func TestProductionSchemaSnapshotStaysFreshAgainstLiveClickHouse(t *testing.T) {
	dsn := os.Getenv("ACR_CLICKHOUSE_INTEGRATION_DSN")
	if dsn == "" {
		if os.Getenv("ACR_CLICKHOUSE_INTEGRATION_REQUIRED") == "1" {
			t.Fatal("ACR_CLICKHOUSE_INTEGRATION_DSN is required when native ClickHouse integration is mandatory")
		}
		t.Skip("ACR_CLICKHOUSE_INTEGRATION_DSN is required to check the live-schema-parity snapshot for drift")
	}

	ctx := context.Background()
	query, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{DSN: dsn, DialTimeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("open live ClickHouse query client: %v", err)
	}
	t.Cleanup(func() { _ = query.Close() })
	if err := query.Ping(ctx); err != nil {
		t.Fatalf("ping live ClickHouse: %v", err)
	}

	tables := make([]string, 0, len(devhealthschema.ProductionColumns))
	for table := range devhealthschema.ProductionColumns {
		tables = append(tables, table)
	}

	rows, err := query.Query(ctx,
		"SELECT table, name, type, position FROM system.columns WHERE database = currentDatabase() AND table IN {tables:Array(String)}",
		[]contextpacket.ClickHouseBinding{{Name: "tables", Value: tables}})
	if err != nil {
		t.Fatalf("query live system.columns: %v", err)
	}
	defer rows.Close()

	liveTypes := map[string]map[string]string{}
	livePositions := map[string]map[string]uint64{}
	for rows.Next() {
		var table, name, chType string
		var position uint64
		if err := rows.Scan(&table, &name, &chType, &position); err != nil {
			t.Fatalf("scan system.columns row: %v", err)
		}
		if liveTypes[table] == nil {
			liveTypes[table] = map[string]string{}
			livePositions[table] = map[string]uint64{}
		}
		liveTypes[table][name] = chType
		livePositions[table][name] = position
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate system.columns rows: %v", err)
	}

	for table, columns := range devhealthschema.ProductionColumns {
		live, ok := liveTypes[table]
		if !ok {
			t.Errorf("live ClickHouse has no table %q that productionColumns (schema_parity_integration_test.go) expects -- regenerate the snapshot against the current schema", table)
			continue
		}
		for _, column := range columns {
			actual, ok := live[column.Name]
			if !ok {
				t.Errorf("live %s.%s no longer exists -- regenerate productionColumns (schema_parity_integration_test.go); the devhealthsource producer reading it will fail on real data", table, column.Name)
				continue
			}
			if actual != column.Type {
				t.Errorf("live %s.%s is %q but devhealthschema still says %q -- regenerate the snapshot, then check every Scan() destination for %s.%s against the new type", table, column.Name, actual, column.Type, table, column.Name)
			}
		}
		// CHAOS-3781 round-3 F3: POSITION as well as type. The
		// declaration is stored in production position order so a
		// rendered fixture is a positional replica; a column reordered
		// upstream would silently make every positional seed land in the
		// wrong column while every type still matched.
		assertDeclaredOrderMatchesLive(t, table, columns, livePositions[table])
	}

	assertEngineAndSortingKeyAreFresh(t, ctx, query)
}

// assertDeclaredOrderMatchesLive checks the declaration lists a table's
// columns in the same relative order production does.
//
// Relative, not absolute: the declaration deliberately carries only the
// columns the readers use, so its positions are a subsequence of live's,
// never equal to them.
func assertDeclaredOrderMatchesLive(t *testing.T, table string, declared []devhealthschema.Column, livePositions map[string]uint64) {
	t.Helper()
	previous := uint64(0)
	previousName := ""
	for _, column := range declared {
		position, ok := livePositions[column.Name]
		if !ok {
			continue // absence is already reported by the caller
		}
		if previousName != "" && position < previous {
			t.Errorf("devhealthschema lists %s.%s before %s.%s, but live has them the other way round -- regenerate the snapshot in production position order, or every positional seed rendered from it lands in the wrong column",
				table, previousName, table, column.Name)
		}
		previous, previousName = position, column.Name
	}
}

// assertEngineAndSortingKeyAreFresh verifies the metadata that is NOT a
// column type (round-3 F3).
//
// This exists because that metadata was originally hand-authored beside
// probed types, and two entries were simply wrong -- disagreeing with live
// and with in-repo comments that had it right. Guessing metadata beside
// probed types reintroduces the drift the probed types exist to prevent,
// so it is now verified by the same check.
func assertEngineAndSortingKeyAreFresh(t *testing.T, ctx context.Context, query *runtimeclickhouse.Client) {
	t.Helper()
	tables := make([]string, 0, len(devhealthschema.ProductionColumns))
	for table := range devhealthschema.ProductionColumns {
		tables = append(tables, table)
	}
	rows, err := query.Query(ctx,
		"SELECT name, engine, sorting_key FROM system.tables WHERE database = currentDatabase() AND name IN {tables:Array(String)}",
		[]contextpacket.ClickHouseBinding{{Name: "tables", Value: tables}})
	if err != nil {
		t.Fatalf("query live system.tables: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var name, engine, sortingKey string
		if err := rows.Scan(&name, &engine, &sortingKey); err != nil {
			t.Fatalf("scan system.tables row: %v", err)
		}
		if declared, ok := devhealthschema.Engines[name]; ok && declared != engine {
			t.Errorf("live %s is ENGINE %q but devhealthschema says %q -- a fixture with the wrong engine either rejects FINAL or dedups rows a test seeded", name, engine, declared)
		}
		if declared, ok := devhealthschema.OrderBy[name]; ok {
			if want := "(" + sortingKey + ")"; declared != want {
				t.Errorf("live %s sorts on %s but devhealthschema says %s -- regenerate from system.tables rather than authoring it", name, want, declared)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate system.tables rows: %v", err)
	}
}
