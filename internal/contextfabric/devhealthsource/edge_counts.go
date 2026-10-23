package devhealthsource

import (
	"context"
	"fmt"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// membershipEdgeKeys is every population membershipEdgeCountSQL reports. The
// source returns each one, zero included.
var membershipEdgeKeys = []contextfabric.EdgeCountKey{
	{Relation: contractsv1.ContextFabricRelationshipBelongsToProject, Arm: contextfabric.EdgeArmTransition},
	{Relation: contractsv1.ContextFabricRelationshipBelongsToProject, Arm: contextfabric.EdgeArmWorkItemColumn},
}

// membershipEdgeCountSQL counts the BELONGS_TO_PROJECT edges the producer
// asserts, per arm: one per distinct relationship identity (subject, resolved
// project, and for a transition interval its event). It reads the producer's own
// row source (membershipRows) and keeps the rows querySubjectProjectMemberships
// turns into an edge: not malformed, not a duplicate add, a project that
// resolves to exactly one row, and not a column-superseded retraction.
func membershipEdgeCountSQL(ingest bool) string {
	return `SELECT source, uniqExact(subject_kind, repo_id_str, subject_id, provider, resolved_project_id, event_id) AS edge_count
FROM (` + membershipRows(cursorState{}, ingest, false) + `)
WHERE is_malformed = 0 AND is_duplicate_add = 0 AND key_resolution_count = 1 AND source != '` + columnSupersededSource + `'
GROUP BY source`
}

// ProjectionSourceEdgeCounts implements contextfabric.ProjectionSourceEdgeCounts.
// A disabled source projects nothing, so it reports no edge populations.
func (s *TeamsProjectsSource) ProjectionSourceEdgeCounts(ctx context.Context, orgID string) (map[contextfabric.EdgeCountKey]int64, error) {
	if !s.enabled {
		return map[contextfabric.EdgeCountKey]int64{}, nil
	}
	if strings.TrimSpace(orgID) == "" {
		return nil, fmt.Errorf("devhealthsource: organization is required")
	}
	ingest, _ := s.ingest.availableAt(ctx, s.client, s.logger, sourcePlan{now: s.now}.clock())
	rows, err := s.client.Query(ctx, membershipEdgeCountSQL(ingest), []contextpacket.ClickHouseBinding{{Name: "org_id", Value: orgID}})
	if err != nil {
		return nil, fmt.Errorf("devhealthsource: count %s edges: %w", contractsv1.ContextFabricRelationshipBelongsToProject, err)
	}
	defer rows.Close()
	counts := make(map[contextfabric.EdgeCountKey]int64, len(membershipEdgeKeys))
	for _, key := range membershipEdgeKeys {
		counts[key] = 0
	}
	for rows.Next() {
		var arm string
		var total uint64
		if err := rows.Scan(&arm, &total); err != nil {
			return nil, fmt.Errorf("devhealthsource: scan %s edge count: %w", contractsv1.ContextFabricRelationshipBelongsToProject, err)
		}
		key := contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipBelongsToProject, Arm: arm}
		if _, known := counts[key]; !known {
			return nil, fmt.Errorf("devhealthsource: %s edge count returned unknown arm %q", contractsv1.ContextFabricRelationshipBelongsToProject, arm)
		}
		counts[key] = int64(total)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("devhealthsource: count %s edges: %w", contractsv1.ContextFabricRelationshipBelongsToProject, err)
	}
	return counts, nil
}

// DeferredEdgeRelations implements contextfabric.ProjectionSourceEdgeCounts:
// the relation types this source produces and does not measure yet.
func (s *TeamsProjectsSource) DeferredEdgeRelations() []contextfabric.RelationshipType {
	if !s.enabled {
		return nil
	}
	return []contextfabric.RelationshipType{contractsv1.ContextFabricRelationshipOwnedByTeam}
}

// ProjectionSourceEdgeCounts implements contextfabric.ProjectionSourceEdgeCounts.
// Each review is one BELONGS_TO_PULL_REQUEST edge (queryPullRequestReviews), so
// the edge count is the review count over the producer's own joins.
func (s *ClickHouseProjectionSource) ProjectionSourceEdgeCounts(ctx context.Context, orgID string) (map[contextfabric.EdgeCountKey]int64, error) {
	counts, err := countKinds(ctx, s.client, orgID, []kindCountStatement{{kind: contractsv1.ContextFabricSubjectPullRequestReview, sql: pullRequestReviewCountSQL}})
	if err != nil {
		return nil, err
	}
	return map[contextfabric.EdgeCountKey]int64{
		{Relation: contractsv1.ContextFabricRelationshipBelongsToPullRequest}: counts[contractsv1.ContextFabricSubjectPullRequestReview],
	}, nil
}

// DeferredEdgeRelations implements contextfabric.ProjectionSourceEdgeCounts.
func (s *ClickHouseProjectionSource) DeferredEdgeRelations() []contextfabric.RelationshipType {
	var deferred []contextfabric.RelationshipType
	for _, relation := range ProducedRelationshipTypes() {
		if relation != contractsv1.ContextFabricRelationshipBelongsToPullRequest {
			deferred = append(deferred, relation)
		}
	}
	return deferred
}
