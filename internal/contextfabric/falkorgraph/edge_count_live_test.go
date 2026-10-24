package falkorgraph

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestLiveCountEdgesCountsDistinctEdgesPerRelationAndArm runs CountEdges on a
// real graph store: the interval arm counts closed and open intervals, the
// column arm counts edges with no validity start, a re-applied batch does not
// double a count, and another organization's edges are not counted.
func TestLiveCountEdgesCountsDistinctEdgesPerRelationAndArm(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	stamp := time.Now().UTC().Format("20060102T150405.000000000")
	orgID, otherOrg := "live-edges-"+stamp, "live-edges-other-"+stamp
	t.Cleanup(func() {
		_ = adapter.PurgeOrganization(context.Background(), orgID)
		_ = adapter.PurgeOrganization(context.Background(), otherOrg)
	})
	observed := time.Now().UTC().Truncate(time.Second)
	closed := observed.Add(time.Hour)
	scope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/api"}}
	workItem := func(id string) contextfabric.SubjectRef {
		return contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: id, Label: id}
	}
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:p1", Label: "p1"}
	relationship := func(id string, rel contractsv1.ContextFabricRelationshipType, from, to contextfabric.SubjectRef, validFrom, validTo *time.Time) contextfabric.RelationshipProjection {
		return contextfabric.RelationshipProjection{
			RelationshipID: id, Type: rel, From: from, To: to,
			Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
			Authorization: scope, EvidenceRefIDs: []string{"evidence_" + id}, ObservedAt: observed, ValidFrom: validFrom, ValidTo: validTo, SourceVersion: "v1",
		}
	}
	batch := func(org, batchID string, rels ...contextfabric.RelationshipProjection) contextfabric.ProjectionBatch {
		return contextfabric.ProjectionBatch{
			SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: batchID, OrgID: org, Source: "live-test", SourceVersion: "v1",
			Cursor: "", NextCursor: "c1", GeneratedAt: observed, Relationships: rels, Entities: []contextfabric.EntityProjection{},
			Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
		}
	}
	rels := []contextfabric.RelationshipProjection{
		relationship("relationship_e1", contractsv1.ContextFabricRelationshipBelongsToProject, workItem("work:1"), project, &observed, &closed),
		relationship("relationship_e2", contractsv1.ContextFabricRelationshipBelongsToProject, workItem("work:1"), project, &closed, nil),
		relationship("relationship_e3", contractsv1.ContextFabricRelationshipBelongsToProject, workItem("work:2"), project, nil, nil),
		relationship("relationship_e4", contractsv1.ContextFabricRelationshipBelongsToPullRequest,
			contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectPullRequestReview, CanonicalID: "review:1", Label: "r1"},
			contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pull_request:1", Label: "pr1"}, nil, nil),
	}
	repoNode := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:r1", Label: "acme/api"}
	tier := func(id, name string) contextfabric.RelationshipProjection {
		r := relationship(id, contractsv1.ContextFabricRelationshipLinksPullRequest, workItem("work:"+id), contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pull_request:" + id, Label: id}, nil, nil)
		r.Properties = map[string]contractsv1.ContextFabricScalarValue{"link_provenance": {String: &name}}
		return r
	}
	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:t1", Label: "t1"}
	rels = append(rels,
		relationship("relationship_t1", contractsv1.ContextFabricRelationshipOwnedByTeam, workItem("work:1"), team, nil, nil),
		relationship("relationship_t2", contractsv1.ContextFabricRelationshipOwnedByTeam, project, team, nil, nil),
		relationship("relationship_t3", contractsv1.ContextFabricRelationshipOwnedByTeam, repoNode, team, nil, nil),
		relationship("relationship_t4", contractsv1.ContextFabricRelationshipOwnedByTeam, contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:r2", Label: "r2"}, team, nil, nil),
		relationship("relationship_r1", contractsv1.ContextFabricRelationshipBelongsToRepository, contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pull_request:1", Label: "pr1"}, repoNode, nil, nil),
		relationship("relationship_r2", contractsv1.ContextFabricRelationshipBelongsToRepository, workItem("work:1"), repoNode, nil, nil),
		relationship("relationship_r3", contractsv1.ContextFabricRelationshipBelongsToRepository, contextfabric.SubjectRef{Kind: contextfabric.SubjectIncident, CanonicalID: "incident:1", Label: "i1"}, repoNode, nil, nil),
		relationship("relationship_r4", contractsv1.ContextFabricRelationshipBelongsToRepository, contextfabric.SubjectRef{Kind: contextfabric.SubjectIncident, CanonicalID: "incident:2", Label: "i2"}, repoNode, nil, nil),
		tier("relationship_l1", contextfabric.EdgeArmLinkNative),
		tier("relationship_l2", contextfabric.EdgeArmLinkHeuristic),
		tier("relationship_l3", contextfabric.EdgeArmLinkHeuristic),
	)
	for i := 0; i < 2; i++ {
		if _, err := adapter.ApplyProjectionBatch(ctx, batch(orgID, "batch_edges_000000"+string(rune('1'+i)), rels...)); err != nil {
			t.Fatalf("ApplyProjectionBatch() error = %v", err)
		}
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, batch(otherOrg, "batch_edges_other_01", relationship("relationship_o1", contractsv1.ContextFabricRelationshipBelongsToProject, workItem("work:9"), project, nil, nil))); err != nil {
		t.Fatalf("other org ApplyProjectionBatch() error = %v", err)
	}
	for key, want := range map[contextfabric.EdgeCountKey]int64{
		{Relation: contractsv1.ContextFabricRelationshipBelongsToProject, Arm: contextfabric.EdgeArmTransition}:       2,
		{Relation: contractsv1.ContextFabricRelationshipBelongsToProject, Arm: contextfabric.EdgeArmWorkItemColumn}:   1,
		{Relation: contractsv1.ContextFabricRelationshipBelongsToProject}:                                             3,
		{Relation: contractsv1.ContextFabricRelationshipBelongsToPullRequest}:                                         1,
		{Relation: contractsv1.ContextFabricRelationshipOwnedByTeam}:                                                  4,
		{Relation: contractsv1.ContextFabricRelationshipOwnedByTeam, Arm: "work_item"}:                                1,
		{Relation: contractsv1.ContextFabricRelationshipOwnedByTeam, Arm: "project"}:                                  1,
		{Relation: contractsv1.ContextFabricRelationshipOwnedByTeam, Arm: "repository"}:                               2,
		{Relation: contractsv1.ContextFabricRelationshipBelongsToRepository}:                                          4,
		{Relation: contractsv1.ContextFabricRelationshipBelongsToRepository, Arm: "incident"}:                         2,
		{Relation: contractsv1.ContextFabricRelationshipBelongsToRepository, Arm: "pull_request"}:                     1,
		{Relation: contractsv1.ContextFabricRelationshipBelongsToRepository, Arm: "work_item"}:                        1,
		{Relation: contractsv1.ContextFabricRelationshipBelongsToRepository, Arm: "deployment"}:                       0,
		{Relation: contractsv1.ContextFabricRelationshipLinksPullRequest, Arm: contextfabric.EdgeArmLinkNative}:       1,
		{Relation: contractsv1.ContextFabricRelationshipLinksPullRequest, Arm: contextfabric.EdgeArmLinkHeuristic}:    2,
		{Relation: contractsv1.ContextFabricRelationshipLinksPullRequest, Arm: contextfabric.EdgeArmLinkExplicitText}: 0,
	} {
		got, err := adapter.CountEdges(ctx, orgID, key)
		if err != nil || got != want {
			t.Fatalf("CountEdges(%+v) = %d, %v; want %d", key, got, err, want)
		}
	}
}
