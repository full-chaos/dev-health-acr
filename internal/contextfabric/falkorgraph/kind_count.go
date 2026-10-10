package falkorgraph

import (
	"context"
	"fmt"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// CountKind counts the subject nodes of one kind the organization holds in its
// ACTIVE graph. It is the graph side of the projection run's source-vs-graph
// count check; an aggregate scalar, so it is not subject to the result-set cap.
func (a *Adapter) CountKind(ctx context.Context, orgID string, kind contextfabric.SubjectKind) (int64, error) {
	if strings.TrimSpace(orgID) == "" || strings.TrimSpace(string(kind)) == "" {
		return 0, fmt.Errorf("organization and kind are required")
	}
	key, err := a.resolveReadKey(ctx, orgID, contextfabric.GraphKeyRoleProjectionWrite)
	if err != nil {
		return 0, err
	}
	cypher := fmt.Sprintf("MATCH (n:%s {%s:$org, %s:$kind}) WHERE true%s RETURN count(n) AS total", labelSubject, propOrgID, propKind, activeTeamCypher("n"))
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
