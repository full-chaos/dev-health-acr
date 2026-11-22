package falkorgraph

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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
// edge has none, an interval edge always has one. The arm of BELONGS_TO_REPOSITORY
// and OWNED_BY_TEAM is the kind of the edge's source node; the arm of LINKS_PULL_REQUEST is the
// provenance tier stored on the edge.
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
	params := map[string]interface{}{"org": orgID, "rel": graphrank.NormalizeRelation(string(key.Relation))}
	if strings.Contains(cypher, "$arm") {
		params["arm"] = key.Arm
	}
	timeout, ok := edgeCountServerTimeout(ctx, a.now())
	if !ok {
		return 0, fmt.Errorf("count edges by relation: %w", contextfabric.ErrQueryBudgetExceeded)
	}
	rows, err := a.api.query(withServerTimeout(ctx, timeout), graphKey, cypher, params, true)
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		// The server's own TIMEOUT stopped the read inside the caller's budget:
		// the count did not fit, which is not a cancellation.
		return 0, fmt.Errorf("count edges by relation: %w", contextfabric.ErrQueryBudgetExceeded)
	}
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

// An edge count walks every edge of the organization for a relation without a
// subject kind arm, past the image's 1 s server TIMEOUT on a production graph.
// Each count read gets its own server TIMEOUT: what is left of the caller's
// deadline less edgeCountTimeoutMargin, so the server answers before the
// context ends, and at most edgeCountTimeoutCap, about 2.5 times the estimated
// production walk, which also bounds how long a projector write waits behind it.
const (
	edgeCountTimeoutCap    = 5 * time.Second
	edgeCountTimeoutMargin = 250 * time.Millisecond
)

func edgeCountServerTimeout(ctx context.Context, now time.Time) (time.Duration, bool) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return edgeCountTimeoutCap, true
	}
	remaining := deadline.Sub(now) - edgeCountTimeoutMargin
	if remaining <= 0 {
		return 0, false
	}
	return min(remaining, edgeCountTimeoutCap), true
}

var repositoryEdgeArms = map[string]bool{
	string(contractsv1.ContextFabricSubjectPullRequest): true,
	string(contractsv1.ContextFabricSubjectWorkItem):    true,
	string(contractsv1.ContextFabricSubjectDeployment):  true,
	string(contractsv1.ContextFabricSubjectCIRun):       true,
	string(contractsv1.ContextFabricSubjectIncident):    true,
}

var ownedByTeamEdgeArms = map[string]bool{
	string(contractsv1.ContextFabricSubjectWorkItem):   true,
	string(contractsv1.ContextFabricSubjectProject):    true,
	string(contractsv1.ContextFabricSubjectRepository): true,
}

var linkEdgeArms = map[string]bool{
	contextfabric.EdgeArmLinkNative:       true,
	contextfabric.EdgeArmLinkExplicitText: true,
	contextfabric.EdgeArmLinkHeuristic:    true,
}

func edgeCountCypher(key contextfabric.EdgeCountKey) (string, error) {
	arm := ""
	switch {
	case key.Arm == "":
	case key.Relation == contractsv1.ContextFabricRelationshipBelongsToProject && key.Arm == contextfabric.EdgeArmWorkItemColumn:
		arm = fmt.Sprintf(" AND r.%s IS NULL", propValidFromNs)
	case key.Relation == contractsv1.ContextFabricRelationshipBelongsToProject && key.Arm == contextfabric.EdgeArmTransition:
		arm = fmt.Sprintf(" AND r.%s IS NOT NULL", propValidFromNs)
	case key.Relation == contractsv1.ContextFabricRelationshipBelongsToRepository && repositoryEdgeArms[key.Arm],
		key.Relation == contractsv1.ContextFabricRelationshipOwnedByTeam && ownedByTeamEdgeArms[key.Arm]:
		arm = fmt.Sprintf(" AND a.%s = $arm", propKind)
	case key.Relation == contractsv1.ContextFabricRelationshipLinksPullRequest && linkEdgeArms[key.Arm]:
		arm = fmt.Sprintf(" AND r.%s%s = $arm", propPropertyPrefix, safeName(linkTierProperty))
	default:
		return "", fmt.Errorf("relation %s has no arm %q", key.Relation, key.Arm)
	}
	return fmt.Sprintf("MATCH (a:%s {%s:$org})-[r:%s]->() WHERE r.%s = $rel%s RETURN count(DISTINCT r.%s) AS total",
		labelSubject, propOrgID, labelRelation, propRelationType, arm, propRelationshipID), nil
}
