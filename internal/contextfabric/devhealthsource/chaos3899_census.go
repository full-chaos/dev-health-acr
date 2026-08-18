package devhealthsource

import (
	"context"
	"fmt"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// CensusBudget is design brief v5 D3's B=999 per-kind row-budget
// interlock: B+1=1,000 sits exactly at the runtime max_result_rows default
// (runtime/clickhouse/options.go). It bounds the NON-DECISIVE enrichment
// row fetch only (satisfier-set prompt-ordering when 2<=count<=B) --
// RunCensus's own DECISIVE row statement below never uses it: that
// statement is only ever issued at Count==1, LIMIT 2 (brief's own
// fail-closed race pin), which can never approach 999.
const CensusBudget = 999

// CensusResult is the per-kind aggregate-first census outcome (design
// brief v5 §1.3(2)).
type CensusResult struct {
	Kind graphrank.CensusKind
	// Count is the aggregate statement's exact satisfier count -- present
	// even for an empty census (protocol (a) always returns its one row,
	// unconditionally).
	Count int
	// CensusReadAt is the aggregate statement's OWN now64() -- never zero,
	// even for Count==0 (brief §1.3(3): "the receipt... exist
	// UNCONDITIONALLY").
	CensusReadAt time.Time
	// SatisfierNaturalKey is the count==1 row statement's own identity
	// column value. Empty unless Count==1 AND the row statement agreed
	// (exactly one row, matching the aggregate's own population).
	SatisfierNaturalKey string
	// ClosureMismatch is true when the row statement (issued only at
	// Count==1) disagreed with the aggregate: 0 rows (the satisfier
	// vanished between (a) and (b)) or 2 rows (LIMIT 2 caught a row
	// landing between the two statements -- the two-statement race). Brief
	// §1.3(2): "a race can only DEMOTE a decisive outcome to clarify,
	// never mint one."
	ClosureMismatch bool
	// StatementCount/RowsRead are the cost-contract gate's own counters
	// (brief §6 Slice A cost-contract gate). StatementCount is 1 for an
	// empty or multi-satisfier census (aggregate only) and 2 once the row
	// statement also runs (Count==1). RowsRead is the row statement's own
	// returned-row count (0, 1, or 2 under its LIMIT 2) -- 0 for a
	// Count!=1 census, since the row statement never runs there.
	//
	// BytesRead is deliberately NOT a field here: contextpacket's
	// ClickHouseQueryClient boundary (source_executor.go) exposes no
	// per-query bytes-read accounting -- only the driver-level
	// max_bytes_to_read SETTING (runtime/clickhouse/options.go) backstops
	// it, loudly, on the ClickHouse side. Recording an actual byte count
	// here would need a wider client interface than every existing
	// producer in this package already uses; this is a noted Slice-A gap,
	// not a faked placeholder number.
	StatementCount int
	RowsRead       int
}

// RunCensus executes design brief v5 §1.3(2)'s aggregate-first, fail-closed
// two-part protocol for ONE kind, against orgID and an already-built
// discriminator predicate (BuildCensusDiscriminator). No join, ever (brief
// §1.3(1)) -- predicate.SQL is ANDed directly onto the base table's own
// org_id equality; nothing else is consulted.
func RunCensus(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, kind graphrank.CensusKind, predicate CensusPredicate) (CensusResult, error) {
	entry, ok := censusKindRegistryEntries[kind]
	if !ok {
		return CensusResult{}, fmt.Errorf("devhealthsource: %s is not a registered census kind", kind)
	}
	if client == nil {
		return CensusResult{}, fmt.Errorf("devhealthsource: census requires a ClickHouseQueryClient")
	}
	if predicate.SQL == "" {
		return CensusResult{}, fmt.Errorf("devhealthsource: census requires a non-empty discriminator predicate")
	}

	bindings := append([]contextpacket.ClickHouseBinding{{Name: "census_org_id", Value: orgID}}, predicate.Bindings...)

	aggregateStatement := fmt.Sprintf(
		"SELECT count(), now64() FROM %s AS %s FINAL WHERE %s = {census_org_id:String} AND %s",
		entry.table, entry.alias, entry.orgColumn, predicate.SQL,
	)
	count, readAt, err := runCensusAggregate(ctx, client, aggregateStatement, bindings)
	if err != nil {
		return CensusResult{}, err
	}
	result := CensusResult{Kind: kind, Count: count, CensusReadAt: readAt, StatementCount: 1}
	if count != 1 {
		return result, nil
	}

	rowStatement := fmt.Sprintf(
		"SELECT %s FROM %s AS %s FINAL WHERE %s = {census_org_id:String} AND %s LIMIT 2",
		entry.identityColumn, entry.table, entry.alias, entry.orgColumn, predicate.SQL,
	)
	keys, err := runCensusRow(ctx, client, rowStatement, bindings)
	if err != nil {
		return CensusResult{}, err
	}
	result.StatementCount = 2
	result.RowsRead = len(keys)
	if len(keys) != 1 {
		result.ClosureMismatch = true
		return result, nil
	}
	result.SatisfierNaturalKey = keys[0]
	return result, nil
}

func runCensusAggregate(ctx context.Context, client contextpacket.ClickHouseQueryClient, statement string, bindings []contextpacket.ClickHouseBinding) (int, time.Time, error) {
	rows, err := client.Query(ctx, statement, bindings)
	if err != nil {
		return 0, time.Time{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return 0, time.Time{}, err
		}
		// Protocol (a) always returns exactly one row (brief §1.3(2)) -- a
		// client returning zero rows here is a backend contract
		// violation, not a valid empty census.
		return 0, time.Time{}, fmt.Errorf("devhealthsource: census aggregate statement returned no row")
	}
	var count uint64
	var readAt time.Time
	if err := rows.Scan(&count, &readAt); err != nil {
		return 0, time.Time{}, err
	}
	if err := rows.Err(); err != nil {
		return 0, time.Time{}, err
	}
	return int(count), readAt.UTC(), nil
}

func runCensusRow(ctx context.Context, client contextpacket.ClickHouseQueryClient, statement string, bindings []contextpacket.ClickHouseBinding) ([]string, error) {
	rows, err := client.Query(ctx, statement, bindings)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return keys, nil
}
