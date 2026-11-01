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

// seedTeamNarrowingHangingKinds projects (CHAOS-12345):
//   - repository R (acme/svc) owned by team-b, S (acme/other) owned by team-c,
//     U (acme/ownerless) with no owner; team-a owns nothing;
//   - on R and on S: a pull request with a review, a CI run and a deployment;
//   - incidents: inc-r mapped to R; inc-both mapped to R and S; inc-s mapped only
//     to S, though correlated with R's deployment; inc-unmapped mapped to no
//     repository, also correlated with R's deployment;
//   - on U: a CI run; and a review with no pull request edge.
func seedTeamNarrowingHangingKinds(t *testing.T, ctx context.Context, adapter *Adapter) string {
	t.Helper()
	orgID := "live-12345-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	now := time.Now().UTC()
	created := now.Add(-48 * time.Hour)
	subject := func(kind contextfabric.SubjectKind, id string) contextfabric.SubjectRef {
		return contextfabric.SubjectRef{Kind: kind, CanonicalID: id, Label: id}
	}
	repoScope := func(slug string) contextfabric.AuthorizationScope {
		return contextfabric.AuthorizationScope{RepositorySlugs: []string{slug}}
	}
	base := contextfabric.ProjectionBatch{SchemaVersion: contextfabric.ProjectionBatchSchemaV1, OrgID: orgID, Source: "live-test", SourceVersion: "v1", GeneratedAt: now,
		Entities: []contextfabric.EntityProjection{}, Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
		Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{}}
	e, r := base, base
	e.BatchID, e.Cursor, e.NextCursor = "batch_12345_0001", "", "cursor-1"
	r.BatchID, r.Cursor, r.NextCursor = "batch_12345_0002", "cursor-1", "cursor-2"
	add := func(s contextfabric.SubjectRef, scope contextfabric.AuthorizationScope) contextfabric.SubjectRef {
		e.Entities = append(e.Entities, contextfabric.EntityProjection{Subject: s, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{}, Authorization: scope,
			EvidenceRefIDs: []string{"evidence_" + s.CanonicalID}, ObservedAt: now, ValidFrom: &created, SourceVersion: "v1"})
		return s
	}
	relate := func(id string, typ contractsv1.ContextFabricRelationshipType, from, to contextfabric.SubjectRef, scope contextfabric.AuthorizationScope) {
		r.Relationships = append(r.Relationships, contextfabric.RelationshipProjection{RelationshipID: "relationship_" + id, Type: typ, From: from, To: to,
			Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved, Authorization: scope,
			EvidenceRefIDs: []string{"evidence_" + id}, ObservedAt: now, ValidFrom: &created, SourceVersion: "v1"})
	}
	belongs := contractsv1.ContextFabricRelationshipBelongsToRepository
	for _, id := range []string{"team-a", "team-b", "team-c"} {
		add(subject(contextfabric.SubjectTeam, id), contextfabric.AuthorizationScope{TeamIDs: []string{id}})
	}
	repositories := map[string]contextfabric.SubjectRef{}
	for name, slug := range map[string]string{"r": "acme/svc", "s": "acme/other", "u": "acme/ownerless"} {
		repositories[name] = add(subject(contextfabric.SubjectRepository, "repository-"+name), repoScope(slug))
	}
	relate("r_owner", contractsv1.ContextFabricRelationshipOwnedByTeam, repositories["r"], subject(contextfabric.SubjectTeam, "team-b"),
		contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/svc"}, TeamIDs: []string{"team-b"}})
	relate("s_owner", contractsv1.ContextFabricRelationshipOwnedByTeam, repositories["s"], subject(contextfabric.SubjectTeam, "team-c"),
		contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/other"}, TeamIDs: []string{"team-c"}})
	for name, slug := range map[string]string{"r": "acme/svc", "s": "acme/other"} {
		scope := repoScope(slug)
		repo := repositories[name]
		pr := add(subject(contextfabric.SubjectPullRequest, "pull_request-"+name), scope)
		relate("pr_"+name, belongs, pr, repo, scope)
		relate("review_"+name, contractsv1.ContextFabricRelationshipBelongsToPullRequest, add(subject(contractsv1.ContextFabricSubjectPullRequestReview, "review-"+name), scope), pr, scope)
		relate("run_"+name, belongs, add(subject(contractsv1.ContextFabricSubjectCIRun, "ci-run-"+name), scope), repo, scope)
		relate("deployment_"+name, belongs, add(subject(contextfabric.SubjectDeployment, "deployment-"+name), scope), repo, scope)
	}
	svc, other := repoScope("acme/svc"), repoScope("acme/other")
	incR := add(subject(contextfabric.SubjectIncident, "incident-r"), svc)
	incBoth := add(subject(contextfabric.SubjectIncident, "incident-both"), svc)
	incS := add(subject(contextfabric.SubjectIncident, "incident-s"), other)
	incUnmapped := add(subject(contextfabric.SubjectIncident, "incident-unmapped"), svc)
	relate("inc_r", belongs, incR, repositories["r"], svc)
	relate("inc_both_r", belongs, incBoth, repositories["r"], svc)
	relate("inc_both_s", belongs, incBoth, repositories["s"], other)
	relate("inc_s", belongs, incS, repositories["s"], other)
	deploymentR := subject(contextfabric.SubjectDeployment, "deployment-r")
	relate("correlated_s", contractsv1.ContextFabricRelationshipCorrelatedWithIncident, deploymentR, incS, svc)
	relate("correlated_unmapped", contractsv1.ContextFabricRelationshipCorrelatedWithIncident, deploymentR, incUnmapped, svc)
	ownerless := repoScope("acme/ownerless")
	relate("run_u", belongs, add(subject(contractsv1.ContextFabricSubjectCIRun, "ci-run-u"), ownerless), repositories["u"], ownerless)
	add(subject(contractsv1.ContextFabricSubjectPullRequestReview, "review-orphan"), svc)
	for _, b := range []contextfabric.ProjectionBatch{e, r} {
		if _, err := adapter.ApplyProjectionBatch(ctx, b); err != nil {
			t.Fatalf("ApplyProjectionBatch(%s) error = %v", b.BatchID, err)
		}
	}
	return orgID
}

func hangingKindsRequest(t *testing.T, ctx context.Context, adapter *Adapter, principal storage.Principal, teams []string) contextfabric.GraphDiscoveryRequest {
	t.Helper()
	binding, err := adapter.ResolveInvestigationBinding(ctx, principal)
	if err != nil {
		t.Fatalf("ResolveInvestigationBinding() error = %v", err)
	}
	return contextfabric.GraphDiscoveryRequest{
		Request: contextfabric.InvestigationRequest{
			SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_12345", Question: "what is going on?",
			TimeContext:    contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			RequestedScope: contextfabric.RequestedScope{TeamIDs: teams},
			Options: contextfabric.InvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 100,
				MaxDrivers: 10, MaxEvidenceRefs: 200, MaxSerializedBytes: 262144},
			Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
		},
		Binding: binding,
	}
}

// TestLiveATeamNarrowingKeepsWhatHangsOffAnOwnedRepository (CHAOS-12345): a
// review reaches a team through its pull request; a CI run, a deployment and an
// incident through their own BELONGS_TO_REPOSITORY edge; an incident never
// through CORRELATED_WITH_INCIDENT, which is relevance, not ownership. Before
// the rule, a narrowing to the OWNING team lost all four kinds, not partial.
func TestLiveATeamNarrowingKeepsWhatHangsOffAnOwnedRepository(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	orgID := seedTeamNarrowingHangingKinds(t, ctx, adapter)
	principal := storage.Principal{OrgID: orgID}
	repository := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository-r", Label: "repository-r"}
	ofR := []string{"ci-run-r", "deployment-r", "incident-r", "pull_request-r", "review-r"}
	for _, tc := range []struct {
		name  string
		teams []string
		want  []string // every served subject must be one of these
		must  []string // and these must be served
		never []string
	}{
		{"the owning team keeps the repository's own", []string{"team-b"},
			[]string{"ci-run-r", "deployment-r", "incident-both", "incident-r", "pull_request-r", "repository-r", "review-r", "team-b"},
			[]string{"ci-run-r", "deployment-r", "incident-both", "incident-r", "pull_request-r", "repository-r", "review-r"},
			[]string{"incident-s", "incident-unmapped"}},
		{"another owner keeps the incident mapped to both, and none of R's own", []string{"team-c"}, nil,
			[]string{"incident-both"}, append([]string{"incident-r", "incident-unmapped"}, ofR...)},
		{"a team that owns nothing keeps nothing", []string{"team-a"}, []string{}, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := hangingKindsRequest(t, ctx, adapter, principal, tc.teams)
			request.Request.Question = "what is going on with repository-r?"
			request.Interpretation = contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status", SubjectTerms: []string{repository.Label},
				TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}}
			request.Resolution = contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{repository}, Candidates: []contextfabric.SubjectCandidate{{
				ReceiptID: "receipt_subject", Subject: repository, State: contextfabric.ResolutionCommitted,
				MatchedTerms: []string{repository.Label}, MatchReasons: []string{"matched"}, Confidence: 1, EvidenceRefIDs: []string{},
			}}}
			gc, err := adapter.DiscoverContext(ctx, principal, request)
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
			if tc.want != nil && !reflect.DeepEqual(served, tc.want) && len(tc.must) == 0 {
				t.Fatalf("served %v, want %v", served, tc.want)
			}
			for _, id := range served {
				if tc.want != nil && !slices.Contains(tc.want, id) {
					t.Errorf("served %s, outside %v", id, tc.want)
				}
				if slices.Contains(tc.never, id) {
					t.Errorf("served %s: it is outside the narrowing (all served: %v)", id, served)
				}
			}
			for _, id := range tc.must {
				if !slices.Contains(served, id) {
					t.Errorf("%s not served, want it in scope (all served: %v)", id, served)
				}
			}
		})
	}
}

// TestLiveATeamNarrowingDecidesTheMembersThatHangOffARepository (CHAOS-12345):
// deployments and incidents as a cohort (CI runs and reviews build no cohort,
// with or without a narrowing; the paths test pins them). An exclusion is scope;
// an incident with no repository edge has missing ownership, so the member set
// is a lower bound (lead ruling: it is undecided, not out of scope).
func TestLiveATeamNarrowingDecidesTheMembersThatHangOffARepository(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	orgID := seedTeamNarrowingHangingKinds(t, ctx, adapter)
	principal := storage.Principal{OrgID: orgID}
	for _, tc := range []struct {
		kind       contextfabric.SubjectKind
		teams      []string
		want       []string
		lowerBound bool
	}{
		{contextfabric.SubjectDeployment, []string{"team-b"}, []string{"deployment-r"}, false},
		{contextfabric.SubjectDeployment, []string{"team-a"}, []string{}, false},
		{contextfabric.SubjectIncident, []string{"team-b"}, []string{"incident-both", "incident-r"}, true},
		{contextfabric.SubjectIncident, []string{"team-c"}, []string{"incident-both", "incident-s"}, true},
	} {
		t.Run(string(tc.kind)+" "+tc.teams[0], func(t *testing.T) {
			request := hangingKindsRequest(t, ctx, adapter, principal, tc.teams)
			request.Request.Question = "which ones are there?"
			request.Interpretation = contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeDiscoveredCohort, TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}}
			request.Resolution = contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{}, Candidates: []contextfabric.SubjectCandidate{}}
			request.Frame = &contextfabric.QuestionFrame{
				Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
				SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionChildrenOfScope,
					Scoped: &contextfabric.ScopedSetExpression{AnchorTerms: []string{"Acme"}, MemberKind: tc.kind}},
				Temporal: contextfabric.TemporalIntentCurrent, Version: contextfabric.QuestionFrameVersion,
			}
			gc, err := adapter.DiscoverContext(ctx, principal, request)
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
			t.Logf("cohort nil=%v members=%v population=%d lowerBound=%v", gc.Cohort == nil, members, gc.CohortPopulation, gc.CohortPopulationLowerBound)
			if !reflect.DeepEqual(members, tc.want) {
				t.Fatalf("members %v, want %v", members, tc.want)
			}
			if gc.CohortPopulationLowerBound != tc.lowerBound {
				t.Fatalf("population lower bound %v, want %v", gc.CohortPopulationLowerBound, tc.lowerBound)
			}
		})
	}
}
