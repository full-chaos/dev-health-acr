package falkorgraph

import (
	"context"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12327: a requested team narrowing on the general graph read
// (DiscoverContext) decides every served subject by ownership, the reach rule
// of CHAOS-12319 (team_narrowing_discover.go). Fixture of CHAOS-12319: R =
// acme/svc OWNED_BY_TEAM team-B, project P OWNED_BY_TEAM team-C, team-A owns
// nothing; github, gitlab, linear and jira issues each link a github and a
// gitlab pull request of R (native); the github and linear issues belong to P.
// Before the fix every team value served the same neighbourhood, complete.
func TestLiveATeamNarrowingDecidesTheGeneralGraphReadByOwnership(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	assertTeamNarrowingInTheGeneralRead(t, ctx, adapter)
}

const (
	teamDiscoverGithubPR = "pull_request:github:acme/svc:1"
	teamDiscoverGitlabPR = "pull_request:gitlab:acme/svc:2"
	teamDiscoverProject  = "project.v2:linear:proj-p"
)

// teamDiscoverRead is the served shape of one DiscoverContext call.
type teamDiscoverRead struct {
	nodes   map[contextfabric.SubjectKind][]string
	partial bool
	details []contractsv1.ContextFabricCoverageDetailCode
}

func discoverUnderTeams(t *testing.T, ctx context.Context, adapter *Adapter, orgID string, subject contextfabric.SubjectRef, grants, teams []string) teamDiscoverRead {
	t.Helper()
	principal := storage.Principal{OrgID: orgID, RepositoryScopes: grants}
	binding, err := adapter.ResolveInvestigationBinding(ctx, principal)
	if err != nil {
		t.Fatalf("ResolveInvestigationBinding() error = %v", err)
	}
	request := contextfabric.GraphDiscoveryRequest{
		Request: contextfabric.InvestigationRequest{
			SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_12327",
			Question: "what is going on with " + subject.Label + "?", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			RequestedScope: contextfabric.RequestedScope{TeamIDs: teams},
			Options: contextfabric.InvestigationOptions{
				MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 100,
				MaxDrivers: 10, MaxEvidenceRefs: 200, MaxSerializedBytes: 262144,
			},
			Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
		},
		Interpretation: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status", SubjectTerms: []string{subject.Label},
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		},
		Resolution: contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{subject}, Candidates: []contextfabric.SubjectCandidate{{
			ReceiptID: "receipt_subject", Subject: subject, State: contextfabric.ResolutionCommitted,
			MatchedTerms: []string{subject.Label}, MatchReasons: []string{"matched"}, Confidence: 1, EvidenceRefIDs: []string{},
		}}},
		Binding: binding,
	}
	gc, err := adapter.DiscoverContext(ctx, principal, request)
	if err != nil {
		t.Fatalf("%s grants %v teams %v: DiscoverContext() error = %v", subject.Label, grants, teams, err)
	}
	read := teamDiscoverRead{nodes: map[contextfabric.SubjectKind][]string{}, partial: gc.Coverage.Partial}
	seen := map[string]bool{}
	for _, p := range gc.Paths {
		for _, n := range p.Nodes {
			if !seen[n.CanonicalID] {
				seen[n.CanonicalID] = true
				read.nodes[n.Kind] = append(read.nodes[n.Kind], n.CanonicalID)
			}
		}
	}
	for kind := range read.nodes {
		sort.Strings(read.nodes[kind])
	}
	for _, d := range gc.Coverage.Details {
		read.details = append(read.details, d.Code)
	}
	return read
}

func assertTeamNarrowingInTheGeneralRead(t *testing.T, ctx context.Context, adapter *Adapter) {
	t.Helper()
	repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: memberAnchorID, Label: memberAnchorSlug}
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: teamDiscoverProject, Label: "proj-p"}
	allPRs := []string{teamDiscoverGithubPR, teamDiscoverGitlabPR, "pull_request:t:1", "pull_request:t:2", "pull_request:t:3"}
	sort.Strings(allPRs)
	// The issues of R: every issue with a link of an admitted tier to one of
	// R's pull requests ("work_item.v2:a:unknown" links only by an unknown tier).
	issuesOfR := []string{memberGithubIssue, memberGitlabIssue, memberLinearIssue, memberJiraIssue, "work_item.v2:a:both", "work_item.v2:a:text", "work_item.v2:a:text-less", "work_item.v2:a:weak"}
	sort.Strings(issuesOfR)
	issuesOfP := []string{memberGithubIssue, memberLinearIssue}
	sort.Strings(issuesOfP)
	orgID := seedTeamNarrowingFixture(t, ctx, adapter, teamNarrowingFixture{})
	type want map[contextfabric.SubjectKind][]string
	k := func(kind contextfabric.SubjectKind) contextfabric.SubjectKind { return kind }
	check := func(t *testing.T, subject contextfabric.SubjectRef, teams []string, w want) {
		t.Helper()
		got := discoverUnderTeams(t, ctx, adapter, orgID, subject, nil, teams)
		for kind := range got.nodes {
			if _, ok := w[kind]; !ok {
				w[kind] = nil
			}
		}
		for kind, ids := range w {
			if !slices.Equal(got.nodes[kind], ids) {
				t.Errorf("%s teams %v: served %s = %v, want %v", subject.Label, teams, kind, got.nodes[kind], ids)
			}
		}
		// An exclusion is scope: never partial, never an authorization detail.
		if got.partial {
			t.Errorf("%s teams %v: partial, want complete (an exclusion is scope; every ownership was present)", subject.Label, teams)
		}
		for _, code := range got.details {
			if code == contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization {
				t.Errorf("%s teams %v: %s, want no authorization detail for a scope exclusion", subject.Label, teams, code)
			}
		}
	}
	t.Run("no narrowing serves the whole neighbourhood", func(t *testing.T) {
		got := discoverUnderTeams(t, ctx, adapter, orgID, repo, nil, nil)
		if len(got.nodes[contextfabric.SubjectPullRequest]) != len(allPRs) || len(got.nodes[contextfabric.SubjectTeam]) != 1 {
			t.Fatalf("no narrowing: served %v, want R's 5 pull requests and team-B", got.nodes)
		}
	})
	t.Run("R under a team that owns nothing serves no neighbour", func(t *testing.T) {
		check(t, repo, []string{"team-A"}, want{})
	})
	t.Run("R under its owning team serves its pull requests and its issues, every provider", func(t *testing.T) {
		check(t, repo, []string{"team-B"}, want{k(contextfabric.SubjectRepository): {memberAnchorID}, k(contextfabric.SubjectPullRequest): allPRs,
			k(contextfabric.SubjectTeam): {"team:team-B"}, k(contextfabric.SubjectWorkItem): issuesOfR})
	})
	t.Run("R under the team that owns only P serves no neighbour within two hops", func(t *testing.T) {
		check(t, repo, []string{"team-C"}, want{})
	})
	t.Run("P under a team that owns nothing serves no neighbour", func(t *testing.T) {
		check(t, project, []string{"team-A"}, want{})
	})
	t.Run("P under R's owning team serves P's issues of R and their pull requests (rule i)", func(t *testing.T) {
		check(t, project, []string{"team-B"}, want{k(contextfabric.SubjectProject): {teamDiscoverProject},
			k(contextfabric.SubjectWorkItem): issuesOfP, k(contextfabric.SubjectPullRequest): {teamDiscoverGithubPR, teamDiscoverGitlabPR}})
	})
	t.Run("P under its owning team serves P's issues, not R's pull requests (rule ii)", func(t *testing.T) {
		check(t, project, []string{"team-C"}, want{k(contextfabric.SubjectProject): {teamDiscoverProject},
			k(contextfabric.SubjectTeam): {"team:team-C"}, k(contextfabric.SubjectWorkItem): issuesOfP})
	})
	t.Run("a requested star narrows nothing", func(t *testing.T) {
		got := discoverUnderTeams(t, ctx, adapter, orgID, repo, nil, []string{"*"})
		if len(got.nodes[contextfabric.SubjectPullRequest]) != len(allPRs) {
			t.Fatalf("teams [*]: served %v, want the whole neighbourhood", got.nodes)
		}
	})
	t.Run("a kind with no ownership path is out of scope (lead ruling Q1)", func(t *testing.T) {
		org := seedTeamNarrowingFixture(t, ctx, adapter, teamNarrowingFixture{})
		seedTeamDiscoverReview(t, ctx, adapter, org)
		if got := discoverUnderTeams(t, ctx, adapter, org, repo, nil, nil); !slices.Equal(got.nodes[contractsv1.ContextFabricSubjectPullRequestReview], []string{teamDiscoverReview}) {
			t.Fatalf("no narrowing: served reviews %v, want %v (the fixture must reach it)", got.nodes[contractsv1.ContextFabricSubjectPullRequestReview], []string{teamDiscoverReview})
		}
		got := discoverUnderTeams(t, ctx, adapter, org, repo, nil, []string{"team-B"})
		if len(got.nodes[contractsv1.ContextFabricSubjectPullRequestReview]) != 0 || len(got.nodes[contextfabric.SubjectPullRequest]) != len(allPRs) {
			t.Fatalf("teams [team-B]: served reviews %v, pull requests %v; want no review and R's 5 pull requests", got.nodes[contractsv1.ContextFabricSubjectPullRequestReview], got.nodes[contextfabric.SubjectPullRequest])
		}
		if got.partial {
			t.Fatalf("teams [team-B]: partial, want complete (an out-of-scope kind is scope, not missing data)")
		}
	})
	t.Run("a granted caller under a team that owns nothing sees no neighbour", func(t *testing.T) {
		got := discoverUnderTeams(t, ctx, adapter, orgID, repo, []string{memberAnchorSlug}, []string{"team-A"})
		for kind, ids := range got.nodes {
			if kind != contextfabric.SubjectRepository && len(ids) > 0 {
				t.Errorf("grant acme/svc teams [team-A]: served %s = %v, want none", kind, ids)
			}
		}
	})
}

// TestLiveATeamNarrowingFloorsACohortOnlyWhereOwnershipIsMissing: the anchored
// member set "R's pull requests" under a team narrowing. A pull request outside
// the requested team is scope (no member, a complete set); the read is floored
// only where the ownership needed to decide it is absent (lead ruling Q2: the
// population floor). An empty member set has no cohort to mark, so the floor
// is the read's population lower bound (GraphContext.CohortPopulationLowerBound,
// what the count step reads), as for any other cut pool.
func TestLiveATeamNarrowingFloorsACohortOnlyWhereOwnershipIsMissing(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: memberAnchorID, Label: memberAnchorSlug}
	type want struct {
		members             int
		complete, truncated bool
		lowerBound          bool
	}
	read := func(t *testing.T, orgID string, teams []string) want {
		t.Helper()
		principal := storage.Principal{OrgID: orgID}
		binding, err := adapter.ResolveInvestigationBinding(ctx, principal)
		if err != nil {
			t.Fatalf("ResolveInvestigationBinding() error = %v", err)
		}
		scoped := &contextfabric.ScopedSetExpression{AnchorTerms: []string{repo.Label}, MemberKind: contextfabric.SubjectPullRequest}
		bases := contextfabric.CommitBasisSet{}
		bases.Record(repo, contextfabric.CommitBasisAuthoritativeIdentity)
		request := contextfabric.GraphDiscoveryRequest{
			Request: contextfabric.InvestigationRequest{
				SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_12327_cohort",
				Question: "which pull requests does acme/svc have?", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
				RequestedScope: contextfabric.RequestedScope{TeamIDs: teams},
				Options: contextfabric.InvestigationOptions{
					MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 100,
					MaxDrivers: 10, MaxEvidenceRefs: 200, MaxSerializedBytes: 262144,
				},
				Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
			},
			Interpretation: contextfabric.InterpretedQuestion{
				Shape: contextfabric.ShapeDiscoveredCohort, TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			},
			Resolution: contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{repo}, Candidates: []contextfabric.SubjectCandidate{{
				ReceiptID: "receipt_anchor", Subject: repo, State: contextfabric.ResolutionCommitted,
				MatchedTerms: []string{repo.Label}, MatchReasons: []string{"matched"}, Confidence: 1, EvidenceRefIDs: []string{},
			}}},
			Frame: &contextfabric.QuestionFrame{
				Goals:             []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
				SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionChildrenOfScope, Scoped: scoped},
				Temporal:          contextfabric.TemporalIntentCurrent,
				Version:           contextfabric.QuestionFrameVersion,
			},
			ScopeAnchorKind: repo.Kind,
			Bases:           bases,
			Binding:         binding,
		}
		gc, err := adapter.DiscoverContext(ctx, principal, request)
		if err != nil {
			t.Fatalf("teams %v: DiscoverContext() error = %v", teams, err)
		}
		if gc.Cohort == nil {
			return want{complete: true, lowerBound: gc.CohortPopulationLowerBound}
		}
		n := 0
		for _, m := range gc.Cohort.Members {
			if m.Subject.Kind == contextfabric.SubjectPullRequest {
				n++
			}
		}
		for _, d := range gc.Coverage.Details {
			if d.Code == contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization {
				t.Errorf("teams %v: %s, want no authorization detail for a scope exclusion", teams, d.Code)
			}
		}
		return want{members: n, complete: gc.Cohort.Complete, truncated: gc.Cohort.Truncated, lowerBound: gc.CohortPopulationLowerBound}
	}
	owned := seedTeamNarrowingFixture(t, ctx, adapter, teamNarrowingFixture{})
	ended := seedTeamNarrowingFixture(t, ctx, adapter, teamNarrowingFixture{anchorOwnerEnded: true})
	for _, tc := range []struct {
		name  string
		orgID string
		teams []string
		want  want
	}{
		{"no narrowing", owned, nil, want{members: 5, complete: true}},
		{"the owning team", owned, []string{"team-B"}, want{members: 5, complete: true}},
		{"a team that owns nothing: scope, complete", owned, []string{"team-A"}, want{members: 0, complete: true}},
		{"no current owner, no narrowing: complete", ended, nil, want{members: 5, complete: true}},
		{"no current owner under a team: floored", ended, []string{"team-A"}, want{members: 0, complete: true, lowerBound: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := read(t, tc.orgID, tc.teams)
			if got != tc.want {
				t.Fatalf("teams %v: got %+v, want %+v", tc.teams, got, tc.want)
			}
		})
	}
}

const teamDiscoverReview = "pull_request_review:github:acme/svc:1:r1"

// seedTeamDiscoverReview adds one pull-request review of R's github pull
// request: a kind with no ownership path to a team.
func seedTeamDiscoverReview(t *testing.T, ctx context.Context, adapter *Adapter, orgID string) {
	t.Helper()
	review := contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectPullRequestReview, CanonicalID: teamDiscoverReview, Label: "review"}
	pr := contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: teamDiscoverGithubPR, Label: "pull request"}
	scope := contextfabric.AuthorizationScope{RepositorySlugs: []string{memberAnchorSlug}}
	entities, relationships := newLiveMemberFixture(orgID, time.Now().UTC(), func(add func(contextfabric.SubjectRef, contextfabric.AuthorizationScope, string), relate func(string, contractsv1.ContextFabricRelationshipType, contextfabric.SubjectRef, contextfabric.SubjectRef, contextfabric.AuthorizationScope, string)) {
		add(review, scope, "")
		relate("review_pr", contractsv1.ContextFabricRelationshipBelongsToPullRequest, review, pr, scope, "")
	}, nil)
	entities.BatchID, entities.Cursor, entities.NextCursor = "batch_12327_review_0001", "cursor-2", "cursor-3"
	relationships.BatchID, relationships.Cursor, relationships.NextCursor = "batch_12327_review_0002", "cursor-3", "cursor-4"
	for _, batch := range []contextfabric.ProjectionBatch{entities, relationships} {
		if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
			t.Fatalf("ApplyProjectionBatch(%s) error = %v", batch.BatchID, err)
		}
	}
}
