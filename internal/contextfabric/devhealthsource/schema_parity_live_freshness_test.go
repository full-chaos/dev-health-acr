package devhealthsource_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
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
// github.com/full-chaos/dev-health-go/clickhouse's own integration suite uses
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
		"SELECT table, name, type, position, default_kind, default_expression FROM system.columns WHERE database = currentDatabase() AND table IN {tables:Array(String)}",
		[]contextpacket.ClickHouseBinding{{Name: "tables", Value: tables}})
	if err != nil {
		t.Fatalf("query live system.columns: %v", err)
	}
	defer rows.Close()

	liveTypes := map[string]map[string]string{}
	livePositions := map[string]map[string]uint64{}
	liveDefaults := map[string]map[string][2]string{}
	for rows.Next() {
		var table, name, chType, defaultKind, defaultExpression string
		var position uint64
		if err := rows.Scan(&table, &name, &chType, &position, &defaultKind, &defaultExpression); err != nil {
			t.Fatalf("scan system.columns row: %v", err)
		}
		if liveTypes[table] == nil {
			liveTypes[table] = map[string]string{}
			livePositions[table] = map[string]uint64{}
			liveDefaults[table] = map[string][2]string{}
		}
		liveDefaults[table][name] = [2]string{defaultKind, defaultExpression}
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
			// A type in transition is declared with its alternates
			// (Column.AlsoAccepts): a deployment carrying one of them is not
			// drift, only a type outside the declaration is.
			// system.columns.type never carries the DEFAULT/MATERIALIZED
			// clause: the declaration's type is compared without it, and
			// the clause is compared as its own field below.
			base, kind, expression := splitDeclaredType(column.Type)
			if !devhealthschema.AcceptsColumnType(table, column.Name, actual) && actual != base {
				t.Errorf("live %s.%s is %q but devhealthschema still says %q (also accepting %v) -- regenerate the snapshot, then check every Scan() destination for %s.%s against the new type", table, column.Name, actual, base, column.AlsoAccepts, table, column.Name)
			}
			// acr DDL DEFAULTs are test-seed only; acr never inserts in
			// prod, so a DEFAULT is not compared (devhealthschema.Column
			// comments name each as TEST DDL). A MATERIALIZED column is
			// part of the production contract (the version a FINAL read
			// picks), so it is compared. Type, order, engine and Enum
			// values stay hard failures.
			if kind == "MATERIALIZED" {
				got := liveDefaults[table][column.Name]
				if got[0] != kind || strings.ReplaceAll(got[1], " ", "") != strings.ReplaceAll(expression, " ", "") {
					t.Errorf("live %s.%s is %s %q but devhealthschema declares %s %q -- regenerate the snapshot", table, column.Name, got[0], got[1], kind, expression)
				}
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
	assertDeclaredProjectionsAreFresh(t, ctx, query)
}

// splitDeclaredType separates a declared type from its trailing DEFAULT or
// MATERIALIZED clause.
func splitDeclaredType(declared string) (base, kind, expression string) {
	for _, marker := range []string{" MATERIALIZED ", " DEFAULT "} {
		if i := strings.Index(declared, marker); i >= 0 {
			return declared[:i], strings.TrimSpace(marker), declared[i+len(marker):]
		}
	}
	return declared, "", ""
}

// assertDeclaredProjectionsAreFresh compares each projection devhealthschema
// declares with system.projections of the same server: a declared projection
// that is missing, or whose select list or group by differs, fails.
func assertDeclaredProjectionsAreFresh(t *testing.T, ctx context.Context, query *runtimeclickhouse.Client) {
	t.Helper()
	tables := make([]string, 0, len(devhealthschema.Projections))
	for table := range devhealthschema.Projections {
		tables = append(tables, table)
	}
	rows, err := query.Query(ctx,
		"SELECT table, name, query FROM system.projections WHERE database = currentDatabase() AND table IN {tables:Array(String)}",
		[]contextpacket.ClickHouseBinding{{Name: "tables", Value: tables}})
	if err != nil {
		t.Fatalf("query live system.projections: %v", err)
	}
	defer rows.Close()
	live := map[string]string{}
	for rows.Next() {
		var table, name, body string
		if err := rows.Scan(&table, &name, &body); err != nil {
			t.Fatalf("scan system.projections row: %v", err)
		}
		live[table+"."+name] = body
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate system.projections rows: %v", err)
	}
	for table, declared := range devhealthschema.Projections {
		for _, projection := range declared {
			body, ok := live[table+"."+projection.Name]
			if !ok {
				t.Errorf("live ClickHouse has no projection %s.%s that devhealthschema declares (%s)", table, projection.Name, projection.Source)
				continue
			}
			if !devhealthschema.ProjectionMatchesLive(projection, body) {
				t.Errorf("live projection %s.%s is %q but devhealthschema declares SELECT %s GROUP BY %s (%s)", table, projection.Name, body, projection.Select, projection.GroupBy, projection.Source)
			}
		}
	}
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
		"SELECT name, engine_full FROM system.tables WHERE database = currentDatabase() AND name IN {tables:Array(String)}",
		[]contextpacket.ClickHouseBinding{{Name: "tables", Value: tables}})
	if err != nil {
		t.Fatalf("query live system.tables: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var name, engineFull string
		if err := rows.Scan(&name, &engineFull); err != nil {
			t.Fatalf("scan system.tables row: %v", err)
		}
		// engine_full, not engine (round-4 R4-1): the class alone omits
		// the VERSION column, and a ReplacingMergeTree without its
		// version keeps an arbitrary row among those sharing a sort key
		// rather than the highest-versioned one. Comparing only the
		// class let a fixture disagree with production on exactly the
		// FINAL semantics several providers rely on. It also covers
		// PARTITION BY, ORDER BY and SETTINGS in the same string, so no
		// facet of the physical definition is left unverified.
		if gap, known := knownEngineGaps[name]; known && gap.matches(name, devhealthschema.EngineFull[name], engineFull) {
			t.Logf("known engine gap, not failed: %s -- %s", name, gap.ticket)
			continue
		}
		if declared, ok := devhealthschema.EngineFull[name]; ok && declared != engineFull {
			t.Errorf("live %s is %q but devhealthschema says %q -- regenerate from system.tables.engine_full rather than authoring any part of it", name, engineFull, declared)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate system.tables rows: %v", err)
	}
}
