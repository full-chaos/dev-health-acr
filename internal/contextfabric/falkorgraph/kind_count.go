package falkorgraph

import (
	"context"
	"fmt"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// CountKind counts the subject nodes of one kind the organization holds in its
// ACTIVE graph that the owned writer wrote (propProjectionOwned, CHAOS-12649): a
// stub a relationship created is not a projected node. It is the graph side of
// the projection run's source-vs-graph count check; an aggregate scalar, so it is
// not subject to the result-set cap.
func (a *Adapter) CountKind(ctx context.Context, orgID string, kind contextfabric.SubjectKind) (int64, error) {
	if strings.TrimSpace(orgID) == "" || strings.TrimSpace(string(kind)) == "" {
		return 0, fmt.Errorf("organization and kind are required")
	}
	key, err := a.resolveReadKey(ctx, orgID, contextfabric.GraphKeyRoleProjectionWrite)
	if err != nil {
		return 0, err
	}
	cypher := fmt.Sprintf("MATCH (n:%s {%s:$org, %s:$kind}) WHERE n.%s = true%s RETURN count(n) AS total", labelSubject, propOrgID, propKind, propProjectionOwned, activeTeamCypher("n"))
	rows, err := a.api.query(ctx, key, cypher, map[string]interface{}{"org": orgID, "kind": string(kind)}, true)
	if err != nil {
		return 0, safeDependencyError("count subject nodes by kind", err)
	}
	if len(rows) != 1 {
		return 0, fmt.Errorf("kind count query returned %d rows, want exactly 1", len(rows))
	}
	total, ok := intFromCount(rows[0]["total"])
	if !ok {
		return 0, fmt.Errorf("kind count query returned a non-numeric total: %#v", rows[0]["total"])
	}
	return int64(total), nil
}

// OwnedCountsKnown reports whether the source's owned node counts can be compared
// with the source (CHAOS-12649): its watermark carries watermarkOwnedMarkedWalk,
// which the first batch of a walk from zero writes. A graph walked before the
// marker existed, or never walked, holds owned nodes without the marker, and its
// owned counts would read as a gap that is not there.
func (a *Adapter) OwnedCountsKnown(ctx context.Context, orgID, source string) (bool, error) {
	if strings.TrimSpace(orgID) == "" || strings.TrimSpace(source) == "" {
		return false, fmt.Errorf("organization and source are required")
	}
	key, err := a.resolveReadKey(ctx, orgID, contextfabric.GraphKeyRoleProjectionWrite)
	if err != nil {
		return false, err
	}
	cypher := fmt.Sprintf("MATCH (w:%s {%s:$org, source:$source}) WHERE w.%s = true RETURN count(w) AS total", labelWatermark, propOrgID, watermarkOwnedMarkedWalk)
	rows, err := a.api.query(ctx, key, cypher, map[string]interface{}{"org": orgID, "source": source}, true)
	if err != nil {
		return false, safeDependencyError("read the owned marker of a projection watermark", err)
	}
	if len(rows) != 1 {
		return false, fmt.Errorf("owned marker query returned %d rows, want exactly 1", len(rows))
	}
	total, ok := intFromCount(rows[0]["total"])
	if !ok {
		return false, fmt.Errorf("owned marker query returned a non-numeric total: %#v", rows[0]["total"])
	}
	return total > 0, nil
}

// nanosPerDay is one UTC day in nanoseconds: the day bucket of a sync stamp.
const nanosPerDay int64 = 24 * 60 * 60 * 1_000_000_000

// CountKindByDay counts the owned nodes of one kind in the organization's ACTIVE
// graph per UTC day (days since the Unix epoch) of their sync stamp
// (contextfabric.ProjectionSyncedAtProperty, CHAOS-12649). A node without the
// stamp is not counted: only a producer that pages on its rows' own last_synced
// stamps its entities. One row per day, far below the server's result cap; a
// reply at the cap is refused rather than read as complete.
func (a *Adapter) CountKindByDay(ctx context.Context, orgID string, kind contextfabric.SubjectKind) (map[int64]int64, error) {
	if strings.TrimSpace(orgID) == "" || strings.TrimSpace(string(kind)) == "" {
		return nil, fmt.Errorf("organization and kind are required")
	}
	key, err := a.resolveReadKey(ctx, orgID, contextfabric.GraphKeyRoleProjectionWrite)
	if err != nil {
		return nil, err
	}
	stamp := "n." + propPropertyPrefix + safeName(contextfabric.ProjectionSyncedAtProperty)
	cypher := fmt.Sprintf("MATCH (n:%s {%s:$org, %s:$kind}) WHERE n.%s = true AND %s IS NOT NULL%s RETURN toInteger(%s / $day) AS day, count(n) AS total",
		labelSubject, propOrgID, propKind, propProjectionOwned, stamp, activeTeamCypher("n"), stamp)
	rows, err := a.api.query(ctx, key, cypher, map[string]interface{}{"org": orgID, "kind": string(kind), "day": nanosPerDay}, true)
	if err != nil {
		return nil, safeDependencyError("count subject nodes by kind and day", err)
	}
	if len(rows) >= falkorResultSetSize {
		return nil, fmt.Errorf("kind day count returned %d rows, the result cap", len(rows))
	}
	days := make(map[int64]int64, len(rows))
	for _, row := range rows {
		day, ok := intFromCount(row["day"])
		if !ok {
			return nil, fmt.Errorf("kind day count returned a non-numeric day: %#v", row["day"])
		}
		total, ok := intFromCount(row["total"])
		if !ok {
			return nil, fmt.Errorf("kind day count returned a non-numeric total: %#v", row["total"])
		}
		days[int64(day)] = int64(total)
	}
	return days, nil
}
