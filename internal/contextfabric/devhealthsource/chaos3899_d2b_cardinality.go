package devhealthsource

import (
	"context"
	"fmt"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// windowColumn is the SAME base-table time column each producer in
// tables.go already uses for its own sincePredicate/orderBy pagination
// (queryPullRequests: p.last_synced; queryWorkItems: w.updated_at;
// queryCIRuns: coalesce(c.finished_at, c.started_at); queryPullRequestReviews:
// r.submitted_at) -- reused here rather than inventing a new "window"
// notion, per the SAME devhealthschema:not-a-production-replica discipline
// the rest of this package's registries follow (no new column, no new
// meaning, just a WHERE range over an already-load-bearing column).
// Deliberately a SEPARATE map from censusKindRegistryEntries -- D2(b)
// measurement is NOT part of the Slice-A decisive registry (D2(a) is what
// ships), so its own window notion lives apart from that registry's own
// fields.
//
// devhealthschema:not-a-production-replica this map pairs each closed census kind with the SAME
// time column name tables.go's own producers already select and order by -- it mirrors no
// column type, engine or sort key of its own.
var windowColumn = map[graphrank.CensusKind]string{
	contextfabric.SubjectPullRequest:                  "p.last_synced",
	contextfabric.SubjectWorkItem:                     "w.updated_at",
	contractsv1.ContextFabricSubjectCIRun:             "coalesce(c.finished_at, c.started_at)",
	contractsv1.ContextFabricSubjectPullRequestReview: "r.submitted_at",
}

// CardinalityWindow is the D2(b) cardinality-measurement predicate --
// window+kind ONLY (design brief v5 D2(b): "allow window+kind-only"), no
// handle, no anchor. Deliberately a SEPARATE, narrower type from
// CensusPredicate (chaos3899_census_registry.go): D2(b) measurement is not
// part of the Slice-A decisive registry (D2(a) is what ships), so its own
// predicate builder lives apart from BuildCensusDiscriminator, which
// requires >=1 KEYED class and must never be tempted to accept a
// window-only D for a real decisive outcome.
type CardinalityWindow struct {
	SQL      string // "" means NO window bound at all -- the fallback/open-window case
	Bindings []contextpacket.ClickHouseBinding
	// Bound is true iff at least one of Start/End/AsOf was present on the
	// INTERPRETED question -- the "did the interpretation layer extract a
	// real window, or did this fall back to unbounded" signal chris named
	// as the actual lever this measurement is checking.
	Bound bool
}

// BuildCardinalityWindow composes the window-only WHERE fragment for kind
// from an interpreted TimeContext's own Start/End/AsOf (never the raw
// request's -- same "interpreted is authoritative" rule CHAOS-3899's
// resolve.go wiring already established). start<=col<=end when both
// present; a lone bound applies as a one-sided range; AsOf (only
// consulted when neither Start nor End is set) applies as an upper bound
// ("as of this instant"). No bound at all -- CardinalityWindow.Bound=false
// -- means an ORG-WIDE, ALL-TIME count for this kind, which is the
// expected/diagnostic reading, not an error.
func BuildCardinalityWindow(kind graphrank.CensusKind, start, end, asOf *time.Time) (CardinalityWindow, error) {
	if _, ok := censusKindRegistryEntries[kind]; !ok {
		return CardinalityWindow{}, fmt.Errorf("devhealthsource: %s is not a registered census kind", kind)
	}
	column, ok := windowColumn[kind]
	if !ok {
		return CardinalityWindow{}, fmt.Errorf("devhealthsource: %s has no registered window column", kind)
	}
	var fragments []string
	var bindings []contextpacket.ClickHouseBinding
	switch {
	case start != nil && end != nil:
		fragments = append(fragments, fmt.Sprintf("%s >= {census_window_start:DateTime64(3,'UTC')}", column))
		bindings = append(bindings, contextpacket.ClickHouseBinding{Name: "census_window_start", Value: start.UTC()})
		fragments = append(fragments, fmt.Sprintf("%s <= {census_window_end:DateTime64(3,'UTC')}", column))
		bindings = append(bindings, contextpacket.ClickHouseBinding{Name: "census_window_end", Value: end.UTC()})
	case start != nil:
		fragments = append(fragments, fmt.Sprintf("%s >= {census_window_start:DateTime64(3,'UTC')}", column))
		bindings = append(bindings, contextpacket.ClickHouseBinding{Name: "census_window_start", Value: start.UTC()})
	case end != nil:
		fragments = append(fragments, fmt.Sprintf("%s <= {census_window_end:DateTime64(3,'UTC')}", column))
		bindings = append(bindings, contextpacket.ClickHouseBinding{Name: "census_window_end", Value: end.UTC()})
	case asOf != nil:
		fragments = append(fragments, fmt.Sprintf("%s <= {census_window_asof:DateTime64(3,'UTC')}", column))
		bindings = append(bindings, contextpacket.ClickHouseBinding{Name: "census_window_asof", Value: asOf.UTC()})
	}
	if len(fragments) == 0 {
		return CardinalityWindow{Bound: false}, nil
	}
	sql := fragments[0]
	for _, f := range fragments[1:] {
		sql += " AND " + f
	}
	return CardinalityWindow{SQL: sql, Bindings: bindings, Bound: true}, nil
}

// CardinalityResult is ONE (kind) cardinality read: the aggregate-only
// count design brief D2(b) would see for window+kind alone -- no witness,
// no row fetch (chris's own framing: "measure what's actually there",
// cheaply -- a distribution question, not a decisive-outcome question).
type CardinalityResult struct {
	Kind   graphrank.CensusKind
	Count  int
	ReadAt time.Time
}

// RunCardinalityCensus executes ONE bare aggregate statement (count(),
// now64() only -- SETTINGS empty_result_for_aggregation_by_empty_set=0,
// the SAME setting pin RunCensus uses, for the SAME reason: the
// unconditional-one-row guarantee) against kind's base table, org-scoped,
// with window.SQL ANDed in when window.Bound. No row statement is ever
// issued -- this measurement never needs a natural key, only a count.
func RunCardinalityCensus(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, kind graphrank.CensusKind, window CardinalityWindow) (CardinalityResult, error) {
	entry, ok := censusKindRegistryEntries[kind]
	if !ok {
		return CardinalityResult{}, fmt.Errorf("devhealthsource: %s is not a registered census kind", kind)
	}
	if client == nil {
		return CardinalityResult{}, fmt.Errorf("devhealthsource: cardinality census requires a ClickHouseQueryClient")
	}
	whereExtra := ""
	bindings := []contextpacket.ClickHouseBinding{{Name: "census_org_id", Value: orgID}}
	if window.Bound {
		whereExtra = " AND " + window.SQL
		bindings = append(bindings, window.Bindings...)
	}
	statement := fmt.Sprintf(
		"SELECT count(), now64() FROM %s AS %s FINAL WHERE %s = {census_org_id:String}%s SETTINGS empty_result_for_aggregation_by_empty_set = 0",
		entry.table, entry.alias, entry.orgColumn, whereExtra,
	)
	rows, err := client.Query(ctx, statement, bindings)
	if err != nil {
		return CardinalityResult{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return CardinalityResult{}, err
		}
		return CardinalityResult{}, fmt.Errorf("devhealthsource: cardinality aggregate statement returned no row")
	}
	var count uint64
	var readAt time.Time
	if err := rows.Scan(&count, &readAt); err != nil {
		return CardinalityResult{}, err
	}
	if rows.Next() {
		return CardinalityResult{}, fmt.Errorf("devhealthsource: cardinality aggregate statement returned more than one row")
	}
	if err := rows.Err(); err != nil {
		return CardinalityResult{}, err
	}
	return CardinalityResult{Kind: kind, Count: int(count), ReadAt: readAt.UTC()}, nil
}
