package falkorgraph

import (
	"context"
	"reflect"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	projectOnlyNarrowingP = "project.v2:github:P-1"
	projectOnlyNarrowingQ = "project.v2:github:P-2"
)

// seedProjectOnlyTeamNarrowing projects two projects a project-only caller is
// granted: P owned by team-c, with two member work items, and Q owned by
// team-d. team-a owns nothing. The BELONGS_TO_PROJECT edges carry "*" for their
// project list, so AuthorizedEdge defers to their endpoints.
func seedProjectOnlyTeamNarrowing(t *testing.T, ctx context.Context, adapter *Adapter) string {
	t.Helper()
	orgID := "live-12396-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	now := time.Now().UTC()
	created := now.Add(-48 * time.Hour)
	entity := func(s contextfabric.SubjectRef, scope contextfabric.AuthorizationScope) contextfabric.EntityProjection {
		return contextfabric.EntityProjection{Subject: s, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{}, Authorization: scope,
			EvidenceRefIDs: []string{"evidence_" + s.Label}, ObservedAt: now, ValidFrom: &created, SourceVersion: "v1"}
	}
	relationship := func(id string, typ contractsv1.ContextFabricRelationshipType, from, to contextfabric.SubjectRef, scope contextfabric.AuthorizationScope) contextfabric.RelationshipProjection {
		return contextfabric.RelationshipProjection{RelationshipID: id, Type: typ, From: from, To: to, Derivation: contextfabric.DerivationCanonicalStructured,
			EpistemicStatus: contextfabric.EpistemicObserved, Authorization: scope, EvidenceRefIDs: []string{"evidence_" + id}, ObservedAt: now, ValidFrom: &created, SourceVersion: "v1"}
	}
	team := func(id string) contextfabric.SubjectRef {
		return contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:" + id, Label: id}
	}
	teamA, teamC, teamD := team("team-a"), team("team-c"), team("team-d")
	p := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: projectOnlyNarrowingP, Label: "ProjectP"}
	q := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: projectOnlyNarrowingQ, Label: "ProjectQ"}
	base := contextfabric.ProjectionBatch{SchemaVersion: contextfabric.ProjectionBatchSchemaV1, OrgID: orgID, Source: "live-test", SourceVersion: "v1", GeneratedAt: now,
		Entities: []contextfabric.EntityProjection{}, Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
		Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{}}
	e, r := base, base
	e.BatchID, e.Cursor, e.NextCursor = "batch_12396_0001", "", "cursor-1"
	r.BatchID, r.Cursor, r.NextCursor = "batch_12396_0002", "cursor-1", "cursor-2"
	for _, s := range []struct {
		team contextfabric.SubjectRef
		id   string
	}{{teamA, "team-a"}, {teamC, "team-c"}, {teamD, "team-d"}} {
		e.Entities = append(e.Entities, entity(s.team, contextfabric.AuthorizationScope{TeamIDs: []string{s.id}}))
	}
	e.Entities = append(e.Entities, entity(p, contextfabric.AuthorizationScope{ProjectIDs: []string{"p-1"}}), entity(q, contextfabric.AuthorizationScope{ProjectIDs: []string{"p-2"}}))
	r.Relationships = append(r.Relationships,
		relationship("relationship_p_owner", contractsv1.ContextFabricRelationshipOwnedByTeam, p, teamC, contextfabric.AuthorizationScope{ProjectIDs: []string{"p-1"}, TeamIDs: []string{"team-c"}}),
		relationship("relationship_q_owner", contractsv1.ContextFabricRelationshipOwnedByTeam, q, teamD, contextfabric.AuthorizationScope{ProjectIDs: []string{"p-2"}, TeamIDs: []string{"team-d"}}))
	for _, id := range []string{"w1", "w2"} {
		w := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_item:" + id, Label: id}
		item := entity(w, contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/x"}})
		identities := []string{p.CanonicalID}
		item.ProjectIdentities = &identities
		e.Entities = append(e.Entities, item)
		r.Relationships = append(r.Relationships, relationship("relationship_"+id+"_project", contractsv1.ContextFabricRelationshipBelongsToProject, w, p, contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/x"}}))
	}
	for _, b := range []contextfabric.ProjectionBatch{e, r} {
		if _, err := adapter.ApplyProjectionBatch(ctx, b); err != nil {
			t.Fatalf("ApplyProjectionBatch(%s) error = %v", b.BatchID, err)
		}
	}
	return orgID
}

func projectOnlyNarrowingRequest(t *testing.T, ctx context.Context, adapter *Adapter, principal storage.Principal, teams []string) contextfabric.GraphDiscoveryRequest {
	t.Helper()
	binding, err := adapter.ResolveInvestigationBinding(ctx, principal)
	if err != nil {
		t.Fatalf("ResolveInvestigationBinding() error = %v", err)
	}
	current := contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}
	return contextfabric.GraphDiscoveryRequest{
		Request: contextfabric.InvestigationRequest{
			SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_12396", Question: "what is going on?", TimeContext: current,
			RequestedScope: contextfabric.RequestedScope{TeamIDs: teams},
			Options: contextfabric.InvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 100,
				MaxDrivers: 10, MaxEvidenceRefs: 200, MaxSerializedBytes: 262144},
			Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
		},
		Binding: binding,
	}
}

// TestLiveATeamNarrowingDecidesAProjectOnlyCallersPaths (CHAOS-12396): the
// general read's team narrowing decides a project-only caller's path edges. An
// edge's own attributes are decided by AuthorizedEdge for this caller;
// AuthorizedAttributes refuses every attribute map with no subject kind, which
// left every edge undecided and kept, so a narrowing to a team that owns nothing
// served the whole neighbourhood, not partial.
func TestLiveATeamNarrowingDecidesAProjectOnlyCallersPaths(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	orgID := seedProjectOnlyTeamNarrowing(t, ctx, adapter)
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: projectOnlyNarrowingP, Label: "ProjectP"}
	projectOnly := storage.Principal{OrgID: orgID, ProjectScopes: []string{projectOnlyNarrowingP, projectOnlyNarrowingQ}}
	unrestricted := storage.Principal{OrgID: orgID}
	neighbourhood := []string{projectOnlyNarrowingP, "work_item:w1", "work_item:w2"}
	withOwner := []string{projectOnlyNarrowingP, "team:team-c", "work_item:w1", "work_item:w2"}
	for _, tc := range []struct {
		name      string
		principal storage.Principal
		teams     []string
		want      []string
	}{
		{"project-only, no narrowing", projectOnly, nil, neighbourhood},
		{"project-only, a team that owns nothing", projectOnly, []string{"team-a"}, []string{}},
		{"project-only, the owning team", projectOnly, []string{"team-c"}, neighbourhood},
		{"project-only, another team's owner", projectOnly, []string{"team-d"}, []string{}},
		{"unrestricted, no narrowing", unrestricted, nil, withOwner},
		{"unrestricted, a team that owns nothing", unrestricted, []string{"team-a"}, []string{}},
		{"unrestricted, the owning team", unrestricted, []string{"team-c"}, withOwner},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := projectOnlyNarrowingRequest(t, ctx, adapter, tc.principal, tc.teams)
			request.Request.Question = "what is going on with ProjectP?"
			request.Interpretation = contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status", SubjectTerms: []string{project.Label},
				TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}}
			request.Resolution = contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{project}, Candidates: []contextfabric.SubjectCandidate{{
				ReceiptID: "receipt_subject", Subject: project, State: contextfabric.ResolutionCommitted,
				MatchedTerms: []string{project.Label}, MatchReasons: []string{"matched"}, Confidence: 1, EvidenceRefIDs: []string{},
			}}}
			gc, err := adapter.DiscoverContext(ctx, tc.principal, request)
			if err != nil {
				t.Fatalf("DiscoverContext() error = %v", err)
			}
			served := []string{}
			for _, p := range gc.Paths {
				for _, n := range p.Nodes {
					if !slices.Contains(served, n.CanonicalID) {
						served = append(served, n.CanonicalID)
					}
				}
			}
			sort.Strings(served)
			if !reflect.DeepEqual(served, tc.want) {
				t.Fatalf("served %v, want %v", served, tc.want)
			}
			if gc.Coverage.Partial {
				t.Fatalf("partial: an exclusion is scope, and no ownership is missing here")
			}
		})
	}
}

// TestLiveATeamNarrowingDecidesAProjectOnlyCallersCohort (CHAOS-12396): the
// cohort candidates of a project-only caller are real nodes, so the node rule
// decides them and the team narrowing keeps only the projects the requested team
// owns. Both projects are granted.
func TestLiveATeamNarrowingDecidesAProjectOnlyCallersCohort(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	orgID := seedProjectOnlyTeamNarrowing(t, ctx, adapter)
	projectOnly := storage.Principal{OrgID: orgID, ProjectScopes: []string{projectOnlyNarrowingP, projectOnlyNarrowingQ}}
	both := []string{projectOnlyNarrowingP, projectOnlyNarrowingQ}
	for _, tc := range []struct {
		name  string
		teams []string
		want  []string
	}{
		{"no narrowing", nil, both},
		{"the owner of P", []string{"team-c"}, []string{projectOnlyNarrowingP}},
		{"the owner of Q", []string{"team-d"}, []string{projectOnlyNarrowingQ}},
		{"a team that owns nothing", []string{"team-a"}, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := projectOnlyNarrowingRequest(t, ctx, adapter, projectOnly, tc.teams)
			request.Request.Question = "which projects are there?"
			request.Interpretation = contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeDiscoveredCohort, TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}}
			request.Resolution = contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{}, Candidates: []contextfabric.SubjectCandidate{}}
			request.Frame = &contextfabric.QuestionFrame{
				Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
				SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionChildrenOfScope,
					Scoped: &contextfabric.ScopedSetExpression{AnchorTerms: []string{"Acme"}, MemberKind: contextfabric.SubjectProject}},
				Temporal: contextfabric.TemporalIntentCurrent, Version: contextfabric.QuestionFrameVersion,
			}
			gc, err := adapter.DiscoverContext(ctx, projectOnly, request)
			if err != nil {
				t.Fatalf("DiscoverContext() error = %v", err)
			}
			members := []string{}
			if gc.Cohort != nil {
				for _, m := range gc.Cohort.Members {
					members = append(members, m.Subject.CanonicalID)
				}
			}
			sort.Strings(members)
			t.Logf("cohort nil=%v members=%v population=%d", gc.Cohort == nil, members, gc.CohortPopulation)
			if !reflect.DeepEqual(members, tc.want) {
				t.Fatalf("members %v, want %v", members, tc.want)
			}
		})
	}
}
