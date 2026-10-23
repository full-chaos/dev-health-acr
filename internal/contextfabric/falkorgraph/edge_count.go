package falkorgraph

import (
	"context"
	"fmt"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

var _ contextfabric.ProjectionGraphEdgeCounts = (*Adapter)(nil)

// CountEdges counts the distinct edges (by relationship_id) of one measured
// population that the organization holds in its ACTIVE graph. It is the graph
// side of the projection run's source-vs-graph edge count check. A closed
// interval edge counts: the producer asserts it and the graph keeps it. The
// arm of BELONGS_TO_PROJECT is the edge's validity start: the work-item column
// edge has none, an interval edge always has one.
func (a *Adapter) CountEdges(ctx context.Context, orgID string, key contextfabric.EdgeCountKey) (int64, error) {
	if strings.TrimSpace(orgID) == "" || strings.TrimSpace(string(key.Relation)) == "" {
		return 0, fmt.Errorf("organization and relation are required")
	}
	cypher, err := edgeCountCypher(key)
	if err != nil {
		return 0, err
	}
	graphKey, err := a.resolveReadKey(ctx, orgID, contextfabric.GraphKeyRoleProjectionWrite)
	if err != nil {
		return 0, err
	}
	rows, err := a.api.query(ctx, graphKey, cypher, map[string]interface{}{"org": orgID, "rel": graphrank.NormalizeRelation(string(key.Relation))}, true)
	if err != nil {
		return 0, safeDependencyError("count edges by relation", err)
	}
	if len(rows) != 1 {
		return 0, fmt.Errorf("edge count query returned %d rows, want exactly 1", len(rows))
	}
	total, ok := intFromCount(rows[0]["total"])
	if !ok {
		return 0, fmt.Errorf("edge count query returned a non-numeric total: %#v", rows[0]["total"])
	}
	return int64(total), nil
}

func edgeCountCypher(key contextfabric.EdgeCountKey) (string, error) {
	arm := ""
	switch {
	case key.Arm == "":
	case key.Relation == contractsv1.ContextFabricRelationshipBelongsToProject && key.Arm == contextfabric.EdgeArmWorkItemColumn:
		arm = fmt.Sprintf(" AND r.%s IS NULL", propValidFromNs)
	case key.Relation == contractsv1.ContextFabricRelationshipBelongsToProject && key.Arm == contextfabric.EdgeArmTransition:
		arm = fmt.Sprintf(" AND r.%s IS NOT NULL", propValidFromNs)
	default:
		return "", fmt.Errorf("relation %s has no arm %q", key.Relation, key.Arm)
	}
	return fmt.Sprintf("MATCH (a:%s {%s:$org})-[r:%s]->() WHERE r.%s = $rel%s RETURN count(DISTINCT r.%s) AS total",
		labelSubject, propOrgID, labelRelation, propRelationType, arm, propRelationshipID), nil
}
