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
	// vanished between (a) and (b)), 2 rows (LIMIT 2 caught a row landing
	// between the two statements -- the two-statement race), OR exactly
	// one row whose natural key does NOT equal the aggregate's own
	// min(<natural key>) WITNESS (sol review correction: a
	// count-preserving IDENTITY SWAP -- row W1 satisfying D at statement
	// (a) replaced by a DIFFERENT row W2 also satisfying D by the time
	// statement (b) runs, e.g. a mutable FK moving W1 out and W2 in
	// between the two reads -- previously passed the count==1-and-1-row
	// checks and committed W2 stamped with a receipt that was actually
	// read against W1's population. The witness closes that: it is not
	// just "did the COUNT agree", it is "did the SAME ROW agree". Brief
	// §1.3(2): "a race can only DEMOTE a decisive outcome to clarify,
	// never mint one" -- now true for an identity-preserving swap too, not
	// just a count-changing one.
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

	// witnessExpr is min(<natural key>) -- with count()==1 there is
	// exactly one row to take the min of, so this deterministically names
	// THAT row's own identity, computed in the SAME statement (and
	// therefore the SAME part-set snapshot) as the count itself (sol
	// review correction). SETTINGS empty_result_for_aggregation_by_empty_set=0
	// (sol review correction, setting pin): ClickHouse can otherwise
	// return ZERO aggregate rows for an aggregate query over an empty
	// input set, which would break the "protocol (a) always returns
	// exactly one row, unconditionally" guarantee the empty-census receipt
	// depends on (brief §1.3(2)-(3)) -- pinning this setting to 0 is what
	// makes that guarantee actually hold rather than merely being assumed.
	witnessExpr := fmt.Sprintf("min(%s)", entry.identityColumn)
	aggregateStatement := fmt.Sprintf(
		"SELECT count(), now64(), %s FROM %s AS %s FINAL WHERE %s = {census_org_id:String} AND %s SETTINGS empty_result_for_aggregation_by_empty_set = 0",
		witnessExpr, entry.table, entry.alias, entry.orgColumn, predicate.SQL,
	)
	count, readAt, witness, err := runCensusAggregate(ctx, client, aggregateStatement, bindings)
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
	// IDENTITY-WITNESS check (sol review correction): count==1 in BOTH
	// statements is not sufficient -- a count-preserving identity swap
	// (row W1 satisfied D at statement (a); a mutable FK moved W1 out and
	// a DIFFERENT row W2 in before statement (b) ran) would pass every
	// check above while committing the WRONG row under a receipt that was
	// never actually read against it. keys[0] must equal the aggregate's
	// own witness, taken from the SAME statement/snapshot as the count.
	if keys[0] != witness {
		result.ClosureMismatch = true
		return result, nil
	}
	result.SatisfierNaturalKey = keys[0]
	return result, nil
}

func runCensusAggregate(ctx context.Context, client contextpacket.ClickHouseQueryClient, statement string, bindings []contextpacket.ClickHouseBinding) (int, time.Time, string, error) {
	rows, err := client.Query(ctx, statement, bindings)
	if err != nil {
		return 0, time.Time{}, "", err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return 0, time.Time{}, "", err
		}
		// Protocol (a) always returns exactly one row (brief §1.3(2),
		// enforced by the empty_result_for_aggregation_by_empty_set=0
		// setting pin above) -- a client returning zero rows here is a
		// backend contract violation, not a valid empty census.
		return 0, time.Time{}, "", fmt.Errorf("devhealthsource: census aggregate statement returned no row")
	}
	var count uint64
	var readAt time.Time
	var witness string
	if err := rows.Scan(&count, &readAt, &witness); err != nil {
		return 0, time.Time{}, "", err
	}
	// Assert EXACTLY one aggregate row (sol review correction): a second
	// row here would mean the aggregate itself is not the single-scalar-row
	// statement the whole fail-closed protocol assumes.
	if rows.Next() {
		return 0, time.Time{}, "", fmt.Errorf("devhealthsource: census aggregate statement returned more than one row")
	}
	if err := rows.Err(); err != nil {
		return 0, time.Time{}, "", err
	}
	return int(count), readAt.UTC(), witness, nil
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
