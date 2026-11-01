package falkorgraph

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func teamCountsStatusRequest(teams []string, committed []contextfabric.SubjectRef, anchorTerm, status string, maxMembers int) contextfabric.GraphDiscoveryRequest {
	current := contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}
	resolution := contextfabric.SubjectResolution{Committed: committed, Candidates: []contextfabric.SubjectCandidate{}}
	bases := contextfabric.CommitBasisSet{}
	var anchorKind contextfabric.SubjectKind
	for _, c := range committed {
		anchorKind = c.Kind
		bases.Record(c, contextfabric.CommitBasisAuthoritativeIdentity)
		resolution.Candidates = append(resolution.Candidates, contextfabric.SubjectCandidate{ReceiptID: "receipt_anchor", Subject: c, State: contextfabric.ResolutionCommitted,
			MatchedTerms: []string{c.Label}, MatchReasons: []string{"matched"}, Confidence: 1, EvidenceRefIDs: []string{}})
	}
	return contextfabric.GraphDiscoveryRequest{
		Request: contextfabric.InvestigationRequest{SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_12357", Question: "which pull requests?", TimeContext: current,
			RequestedScope: contextfabric.RequestedScope{TeamIDs: teams},
			Options:        contextfabric.InvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: maxMembers, MaxRelationshipPaths: 50, MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144},
			Consumer:       contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"}},
		Interpretation: contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeDiscoveredCohort, TimeContext: current},
		Resolution:     resolution,
		Frame: &contextfabric.QuestionFrame{
			Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
			SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionChildrenOfScope,
				Scoped: &contextfabric.ScopedSetExpression{AnchorTerms: []string{anchorTerm}, MemberKind: contextfabric.SubjectPullRequest, MemberQualifier: contextfabric.MemberQualifierStatus, MemberQualifierValue: status}},
			Temporal: contextfabric.TemporalIntentCurrent, Version: contextfabric.QuestionFrameVersion,
		},
		ScopeAnchorKind: anchorKind, Bases: bases,
	}
}

// TestLiveATeamNarrowingStatesNoPoolForAProjectsLinkedPullRequests
// (CHAOS-12357): the pool cut's total is the requested team's only when the
// committed anchor being owned puts every member in scope. A project's own work
// items are in scope with it; a pull request reached through its issues follows
// its own repository's owner, so the project's total is not the team's. Project
// P is owned by team-t; a third of its 300 linked pull requests are in team-t's
// repository, the rest in team-c's.
func TestLiveATeamNarrowingStatesNoPoolForAProjectsLinkedPullRequests(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	orgID := "live-12357-pool-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	now := time.Now().UTC()
	created := now.Add(-48 * time.Hour)
	text := func(v string) contextfabric.ScalarValue { return contextfabric.ScalarValue{String: &v} }
	subject := func(kind contextfabric.SubjectKind, id string) contextfabric.SubjectRef {
		return contextfabric.SubjectRef{Kind: kind, CanonicalID: id, Label: id}
	}
	entity := func(s contextfabric.SubjectRef, scope contextfabric.AuthorizationScope, properties map[string]contextfabric.ScalarValue) contextfabric.EntityProjection {
		return contextfabric.EntityProjection{Subject: s, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{}, Properties: properties, Authorization: scope,
			EvidenceRefIDs: []string{"evidence_" + s.CanonicalID}, ObservedAt: now, ValidFrom: &created, SourceVersion: "v1"}
	}
	relationship := func(id string, typ contractsv1.ContextFabricRelationshipType, from, to contextfabric.SubjectRef, scope contextfabric.AuthorizationScope, properties map[string]contextfabric.ScalarValue) contextfabric.RelationshipProjection {
		return contextfabric.RelationshipProjection{RelationshipID: "relationship_" + id, Type: typ, From: from, To: to, Properties: properties, Derivation: contextfabric.DerivationCanonicalStructured,
			EpistemicStatus: contextfabric.EpistemicObserved, Authorization: scope, EvidenceRefIDs: []string{"evidence_" + id}, ObservedAt: now, ValidFrom: &created, SourceVersion: "v1"}
	}
	rank := int64(3)
	native := map[string]contextfabric.ScalarValue{"link_provenance": text("native"), "link_provenance_rank": {Integer: &rank}}
	teamT, teamC := subject(contextfabric.SubjectTeam, "team-t"), subject(contextfabric.SubjectTeam, "team-c")
	project := subject(contextfabric.SubjectProject, "project-p")
	owned, other := subject(contextfabric.SubjectRepository, "repository-owned"), subject(contextfabric.SubjectRepository, "repository-other")
	ownedScope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/owned"}}
	otherScope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/other"}}
	base := contextfabric.ProjectionBatch{SchemaVersion: contextfabric.ProjectionBatchSchemaV1, OrgID: orgID, Source: "live-test", SourceVersion: "v1", GeneratedAt: now,
		Entities: []contextfabric.EntityProjection{}, Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
		Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{}}
	e, r := base, base
	e.BatchID, e.Cursor, e.NextCursor = "batch_12357_0001", "", "cursor-1"
	r.BatchID, r.Cursor, r.NextCursor = "batch_12357_0002", "cursor-1", "cursor-2"
	e.Entities = append(e.Entities, entity(teamT, contextfabric.AuthorizationScope{TeamIDs: []string{"team-t"}}, nil), entity(teamC, contextfabric.AuthorizationScope{TeamIDs: []string{"team-c"}}, nil),
		entity(project, contextfabric.AuthorizationScope{ProjectIDs: []string{"p"}}, nil), entity(owned, ownedScope, nil), entity(other, otherScope, nil))
	r.Relationships = append(r.Relationships,
		relationship("p_owner", contractsv1.ContextFabricRelationshipOwnedByTeam, project, teamT, contextfabric.AuthorizationScope{ProjectIDs: []string{"p"}, TeamIDs: []string{"team-t"}}, nil),
		relationship("owned_owner", contractsv1.ContextFabricRelationshipOwnedByTeam, owned, teamT, contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/owned"}, TeamIDs: []string{"team-t"}}, nil),
		relationship("other_owner", contractsv1.ContextFabricRelationshipOwnedByTeam, other, teamC, contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/other"}, TeamIDs: []string{"team-c"}}, nil))
	for i := 1; i <= 300; i++ {
		repo, scope := other, otherScope
		if i%3 == 0 {
			repo, scope = owned, ownedScope
		}
		pull := subject(contextfabric.SubjectPullRequest, fmt.Sprintf("pull_request-%03d", i))
		issue := subject(contextfabric.SubjectWorkItem, fmt.Sprintf("work_item-%03d", i))
		e.Entities = append(e.Entities, entity(pull, scope, map[string]contextfabric.ScalarValue{"state": text("open")}), entity(issue, scope, map[string]contextfabric.ScalarValue{"type": text("issue")}))
		r.Relationships = append(r.Relationships,
			relationship(fmt.Sprintf("pr_%03d", i), contractsv1.ContextFabricRelationshipBelongsToRepository, pull, repo, scope, nil),
			relationship(fmt.Sprintf("issue_%03d", i), contractsv1.ContextFabricRelationshipBelongsToProject, issue, project, scope, nil),
			relationship(fmt.Sprintf("link_%03d", i), contractsv1.ContextFabricRelationshipLinksPullRequest, issue, pull, scope, native))
	}
	for _, b := range []contextfabric.ProjectionBatch{e, r} {
		if _, err := adapter.ApplyProjectionBatch(ctx, b); err != nil {
			t.Fatalf("ApplyProjectionBatch(%s) error = %v", b.BatchID, err)
		}
	}
	for _, tc := range []struct {
		name  string
		teams []string
		want  contextfabric.MemberStatusPoolCut
	}{
		{"no narrowing: the project's own total", nil, contextfabric.MemberStatusPoolCut{Kind: contextfabric.SubjectPullRequest, Read: 26, Pool: 300}},
		{"the project's owner: its read members, and no pool", []string{"team-t"}, contextfabric.MemberStatusPoolCut{Kind: contextfabric.SubjectPullRequest, Read: 8, Pool: 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gc, err := adapter.DiscoverContext(ctx, storage.Principal{OrgID: orgID}, teamCountsStatusRequest(tc.teams, []contextfabric.SubjectRef{project}, project.Label, "awaiting_review", 50))
			if err != nil {
				t.Fatalf("DiscoverContext() error = %v", err)
			}
			if gc.MemberStatusPoolCut != tc.want {
				t.Fatalf("pool cut %+v, want %+v", gc.MemberStatusPoolCut, tc.want)
			}
		})
	}
}

// TestLiveATeamNarrowingStatesTheKindCensusFloorInsideTheNarrowing
// (CHAOS-12357): the census detail's Declared is a floor of the members, so
// under a team narrowing it counts only the candidates inside the narrowing,
// never the organization's. 2050 open pull requests of team-c's repository sort
// first, so the cut census read holds none of team-t's.
func TestLiveATeamNarrowingStatesTheKindCensusFloorInsideTheNarrowing(t *testing.T) {
	t.Run("a repository the narrowing refuses", func(t *testing.T) {
		teamCensusFloorCase(t, contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/aaa"}})
	})
	// The team check keeps a node the requested scope refuses, for the
	// authorization check to refuse, so it stays in the census's nodes.
	t.Run("members the requested scope refuses", func(t *testing.T) {
		teamCensusFloorCase(t, contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/aaa"}, TeamIDs: []string{"team-c"}})
	})
}

func teamCensusFloorCase(t *testing.T, scope contextfabric.AuthorizationScope) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	orgID := "live-12357-census-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	now := time.Now().UTC()
	created := now.Add(-48 * time.Hour)
	open := "open"
	e, r := teamStatusFloorFixture(orgID, now, "", 1)
	entity := func(s contextfabric.SubjectRef, scope contextfabric.AuthorizationScope, properties map[string]contextfabric.ScalarValue) contextfabric.EntityProjection {
		return contextfabric.EntityProjection{Subject: s, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{}, Properties: properties, Authorization: scope,
			EvidenceRefIDs: []string{"evidence_" + s.Label}, ObservedAt: now, ValidFrom: &created, SourceVersion: "v1"}
	}
	relationship := func(id string, typ contractsv1.ContextFabricRelationshipType, from, to contextfabric.SubjectRef, scope contextfabric.AuthorizationScope) contextfabric.RelationshipProjection {
		return contextfabric.RelationshipProjection{RelationshipID: "relationship_" + id, Type: typ, From: from, To: to, Derivation: contextfabric.DerivationCanonicalStructured,
			EpistemicStatus: contextfabric.EpistemicObserved, Authorization: scope, EvidenceRefIDs: []string{"evidence_" + id}, ObservedAt: now, ValidFrom: &created, SourceVersion: "v1"}
	}
	teamC := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:team-c", Label: "team-c"}
	other := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository-aaa", Label: "repository-aaa"}
	e.Entities = append(e.Entities, entity(teamC, contextfabric.AuthorizationScope{TeamIDs: []string{"team-c"}}, nil), entity(other, scope, nil))
	r.Relationships = append(r.Relationships, relationship("aaa_owner", contractsv1.ContextFabricRelationshipOwnedByTeam, other, teamC, contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/aaa"}, TeamIDs: []string{"team-c"}}))
	batches := []contextfabric.ProjectionBatch{e}
	cursor := 1
	chunk := e
	chunk.Entities, chunk.Relationships = []contextfabric.EntityProjection{}, []contextfabric.RelationshipProjection{}
	flush := func() {
		chunk.BatchID, chunk.Cursor, chunk.NextCursor = fmt.Sprintf("batch_12357_census_%d", cursor), fmt.Sprintf("cursor-%d", cursor), fmt.Sprintf("cursor-%d", cursor+1)
		batches, cursor = append(batches, chunk), cursor+1
		chunk.Entities = []contextfabric.EntityProjection{}
	}
	for i := 1; i <= 2050; i++ {
		if len(chunk.Entities) == 900 {
			flush()
		}
		pull := contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: fmt.Sprintf("pull_request:aaa:%04d", i), Label: fmt.Sprintf("PR aaa %d", i)}
		chunk.Entities = append(chunk.Entities, entity(pull, scope, map[string]contextfabric.ScalarValue{"state": {String: &open}}))
		r.Relationships = append(r.Relationships, relationship(fmt.Sprintf("aaa_pr_%04d", i), contractsv1.ContextFabricRelationshipBelongsToRepository, pull, other, scope))
	}
	flush()
	r.BatchID, r.Cursor, r.NextCursor = "batch_12357_census_rels", fmt.Sprintf("cursor-%d", cursor), fmt.Sprintf("cursor-%d", cursor+1)
	batches = append(batches, r)
	for _, b := range batches {
		if _, err := adapter.ApplyProjectionBatch(ctx, b); err != nil {
			t.Fatalf("ApplyProjectionBatch(%s) error = %v", b.BatchID, err)
		}
	}
	declared := func(t *testing.T, teams []string) int {
		t.Helper()
		gc, err := adapter.DiscoverContext(ctx, storage.Principal{OrgID: orgID}, teamCountsStatusRequest(teams, []contextfabric.SubjectRef{}, "Acme", "open", 5))
		if err != nil {
			t.Fatalf("DiscoverContext() error = %v", err)
		}
		for _, d := range gc.Coverage.Details {
			if d.Code == contractsv1.ContextFabricCoverageDetailKindCensusTruncated && d.Declared != nil {
				return *d.Declared
			}
		}
		t.Fatalf("teams %v: no kind_census_truncated detail in %+v, want one (the census is cut)", teams, gc.Coverage.Details)
		return -1
	}
	if got := declared(t, nil); got < 5 {
		t.Fatalf("no narrowing: declared %d, want the census figure", got)
	}
	if got := declared(t, []string{"team-t"}); got != 0 {
		t.Fatalf("teams [team-t]: declared %d, want 0: no candidate the cut read holds is inside the narrowing", got)
	}
}
