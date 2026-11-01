package falkorgraph

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
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
		// floor: the team-narrowing log line's floor ("" = no line, for no
		// narrowing); armed: the cohort basis line names the
		// team_narrowing_undecided arm.
		floor contextfabric.TeamNarrowingReadFloor
		armed bool
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
		recorder := &recordingTelemetry{}
		previous := adapter.config.Telemetry
		adapter.config.Telemetry = recorder
		gc, err := adapter.DiscoverContext(ctx, principal, request)
		adapter.config.Telemetry = previous
		if err != nil {
			t.Fatalf("teams %v: DiscoverContext() error = %v", teams, err)
		}
		var floor contextfabric.TeamNarrowingReadFloor
		switch len(recorder.teamNarrowingReads) {
		case 0:
		case 1:
			line := recorder.teamNarrowingReads[0]
			floor = line.floor
			if line.requestedTeams != len(teams) || line.excluded > line.decided {
				t.Errorf("teams %v: team narrowing line %+v, want requested_teams=%d and excluded <= decided", teams, line, len(teams))
			}
		default:
			t.Errorf("teams %v: %d team narrowing lines, want at most one per call", teams, len(recorder.teamNarrowingReads))
		}
		armed := false
		for _, basis := range recorder.cohortKindBases {
			for _, arm := range basis.poolTruncationArms {
				armed = armed || arm == CohortPoolTruncationArmTeamNarrowingUndecided
			}
		}
		if gc.Cohort == nil {
			return want{complete: true, lowerBound: gc.CohortPopulationLowerBound, floor: floor, armed: armed}
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
		return want{members: n, complete: gc.Cohort.Complete, truncated: gc.Cohort.Truncated, lowerBound: gc.CohortPopulationLowerBound, floor: floor, armed: armed}
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
		{"the owning team", owned, []string{"team-B"}, want{members: 5, complete: true, floor: contextfabric.TeamNarrowingReadFloorNone}},
		{"a team that owns nothing: scope, complete", owned, []string{"team-A"}, want{members: 0, complete: true, floor: contextfabric.TeamNarrowingReadFloorNone}},
		{"no current owner, no narrowing: complete", ended, nil, want{members: 5, complete: true}},
		{"no current owner under a team: floored", ended, []string{"team-A"}, want{members: 0, complete: true, lowerBound: true, floor: contextfabric.TeamNarrowingReadFloorUndecided, armed: true}},
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

// TestLiveATeamNarrowingKeepsTheStatusPopulationALowerBoundWhereOwnershipIsMissing
// (CHAOS-12327, review r1 and r2): the organization's open pull requests under a
// team narrowing, when the status census is cut past the member read. team-t
// owns acme/owned and its 30 open pull requests; acme/orphan has no owner and
// one open pull request. The read serves 25. The status count decides all 31
// matches, so the population is the team's 30 -- a lower bound, because one
// match's ownership was missing, even though the count itself was complete.
// The orphan is first by id (inside the 25 read) or last (only in the status
// count, past the read): either way the floor is logged and its pool arm named.
// With no orphan pull request, the ownerless repository is a candidate of
// another kind: it loses no member, so the read is not floored.
func TestLiveATeamNarrowingKeepsTheStatusPopulationALowerBoundWhereOwnershipIsMissing(t *testing.T) {
	for _, tc := range []struct {
		name     string
		orphanID string
		owned    int
		floored  bool
	}{
		{"orphan pull request first by id", teamStatusFloorOrphanPR, 30, true},
		{"orphan pull request past the read", teamStatusFloorLateOrphanPR, 30, true},
		{"ownerless repository with no pull request", "", 10, false},
	} {
		t.Run(tc.name, func(t *testing.T) { assertTeamStatusFloor(t, tc.orphanID, tc.owned, tc.floored) })
	}
}

func assertTeamStatusFloor(t *testing.T, orphanID string, owned int, floored bool) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	orgID := "live-12327-status-floor-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	entities, edges := teamStatusFloorFixture(orgID, time.Now().UTC(), orphanID, owned)
	for _, b := range []contextfabric.ProjectionBatch{entities, edges} {
		if _, err := adapter.ApplyProjectionBatch(ctx, b); err != nil {
			t.Fatalf("ApplyProjectionBatch(%s) error = %v", b.BatchID, err)
		}
	}
	scoped := &contextfabric.ScopedSetExpression{AnchorTerms: []string{"Acme"}, MemberKind: contextfabric.SubjectPullRequest,
		MemberQualifier: contextfabric.MemberQualifierStatus, MemberQualifierValue: "open"}
	request := contextfabric.GraphDiscoveryRequest{
		Request: contextfabric.InvestigationRequest{
			SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_12327_status_floor",
			Question: "which pull requests are open?", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			RequestedScope: contextfabric.RequestedScope{TeamIDs: []string{"team-t"}},
			Options: contextfabric.InvestigationOptions{
				MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
				MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144,
			},
			Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
		},
		Interpretation: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeDiscoveredCohort, TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		},
		Resolution: contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{}, Candidates: []contextfabric.SubjectCandidate{}},
		Frame: &contextfabric.QuestionFrame{
			Goals:             []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
			SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionChildrenOfScope, Scoped: scoped},
			Temporal:          contextfabric.TemporalIntentCurrent,
			Version:           contextfabric.QuestionFrameVersion,
		},
	}
	recorder := &recordingTelemetry{}
	adapter.config.Telemetry = recorder
	gc, err := adapter.DiscoverContext(ctx, storage.Principal{OrgID: orgID}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	wantFloor := contextfabric.TeamNarrowingReadFloorNone
	if floored {
		wantFloor = contextfabric.TeamNarrowingReadFloorUndecided
	}
	if len(recorder.teamNarrowingReads) != 1 || recorder.teamNarrowingReads[0].floor != wantFloor {
		t.Fatalf("team narrowing lines %+v, want one with floor=%s", recorder.teamNarrowingReads, wantFloor)
	}
	armed := false
	for _, basis := range recorder.cohortKindBases {
		armed = armed || slices.Contains(basis.poolTruncationArms, CohortPoolTruncationArmTeamNarrowingUndecided)
	}
	if armed != floored {
		t.Fatalf("cohort kind basis lines %+v: pool arm %s named=%v, want %v (named exactly when the floor is logged)", recorder.cohortKindBases, CohortPoolTruncationArmTeamNarrowingUndecided, armed, floored)
	}
	if gc.Cohort == nil {
		t.Fatal("no member set served, want the team's open pull requests")
	}
	for _, m := range gc.Cohort.Members {
		if m.Subject.CanonicalID == orphanID {
			t.Fatalf("served %s, whose repository has no owner: out of the narrowing", orphanID)
		}
	}
	if gc.CohortPopulation != owned || gc.CohortPopulationLowerBound != floored {
		t.Fatalf("population=%d lower bound=%v (members %d), want %d and lower bound %v",
			gc.CohortPopulation, gc.CohortPopulationLowerBound, len(gc.Cohort.Members), owned, floored)
	}
}

const (
	teamStatusFloorOrphanPR     = "pull_request:aaa-orphan:001"
	teamStatusFloorLateOrphanPR = "pull_request:zzz-orphan:001"
)

// teamStatusFloorFixture: team-t owns acme/owned (owned open pull requests);
// acme/orphan has no owner (one open pull request orphanID, or none for "").
func teamStatusFloorFixture(orgID string, observed time.Time, orphanID string, ownedPRs int) (contextfabric.ProjectionBatch, contextfabric.ProjectionBatch) {
	created := observed.Add(-48 * time.Hour)
	text := func(value string) contextfabric.ScalarValue { return contextfabric.ScalarValue{String: &value} }
	entity := func(subject contextfabric.SubjectRef, scope contextfabric.AuthorizationScope, properties map[string]contextfabric.ScalarValue) contextfabric.EntityProjection {
		return contextfabric.EntityProjection{
			Subject: subject, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{},
			Properties: properties, Authorization: scope, EvidenceRefIDs: []string{"evidence_" + subject.CanonicalID},
			ObservedAt: observed, ValidFrom: &created, SourceVersion: "v1",
		}
	}
	relationship := func(id string, typ contractsv1.ContextFabricRelationshipType, from, to contextfabric.SubjectRef, scope contextfabric.AuthorizationScope) contextfabric.RelationshipProjection {
		return contextfabric.RelationshipProjection{
			RelationshipID: id, Type: typ, From: from, To: to,
			Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
			Authorization: scope, EvidenceRefIDs: []string{"evidence_" + id}, ObservedAt: observed, ValidFrom: &created, SourceVersion: "v1",
		}
	}
	owned := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/owned"}}
	orphanScope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/orphan"}}
	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:team-t", Label: "Team T"}
	repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acme-owned", Label: "acme/owned"}
	orphan := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acme-orphan", Label: "acme/orphan"}
	entities := []contextfabric.EntityProjection{
		entity(team, contextfabric.AuthorizationScope{TeamIDs: []string{"team-t"}}, nil),
		entity(repo, owned, nil), entity(orphan, orphanScope, nil),
	}
	relationships := []contextfabric.RelationshipProjection{
		relationship("relationship_owned_team", contractsv1.ContextFabricRelationshipOwnedByTeam, repo, team,
			contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/owned"}, TeamIDs: []string{"team-t"}}),
	}
	open := map[string]contextfabric.ScalarValue{"state": text("open")}
	if orphanID != "" {
		pr := contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: orphanID, Label: "PR orphan"}
		entities = append(entities, entity(pr, orphanScope, open))
		relationships = append(relationships, relationship("relationship_orphan_pr_repository", contractsv1.ContextFabricRelationshipBelongsToRepository, pr, orphan, orphanScope))
	}
	for i := 1; i <= ownedPRs; i++ {
		pr := contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: fmt.Sprintf("pull_request:owned:%03d", i), Label: fmt.Sprintf("PR #%d", i)}
		entities = append(entities, entity(pr, owned, open))
		relationships = append(relationships, relationship(fmt.Sprintf("relationship_owned_pr_%03d_repository", i), contractsv1.ContextFabricRelationshipBelongsToRepository, pr, repo, owned))
	}
	base := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, OrgID: orgID, Source: "live-test", SourceVersion: "v1", GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{}, Relationships: []contextfabric.RelationshipProjection{},
		Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	e, r := base, base
	e.BatchID, e.Cursor, e.NextCursor, e.Entities = "batch_12327_status_floor_0001", "", "cursor-1", entities
	r.BatchID, r.Cursor, r.NextCursor, r.Relationships = "batch_12327_status_floor_0002", "cursor-1", "cursor-2", relationships
	return e, r
}

// TestLiveTheTeamNarrowingLineCertifiesFromTheRealProducer: the general read's
// team-narrowing line, emitted by the real DiscoverContext into a real
// slog.JSONHandler, certifies against eventspec.TeamNarrowingGeneralRead.
func TestLiveTheTeamNarrowingLineCertifiesFromTheRealProducer(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	orgID := seedTeamNarrowingFixture(t, ctx, adapter, teamNarrowingFixture{anchorOwnerEnded: true})
	var buf bytes.Buffer
	adapter.config.Telemetry = SlogTelemetry{Logger: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))}
	repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: memberAnchorID, Label: memberAnchorSlug}
	discoverUnderTeams(t, ctx, adapter, orgID, repo, nil, []string{"team-A"})
	log, err := certify.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse() error = %v", err)
	}
	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.TeamNarrowingGeneralRead,
		Want:  map[string]any{"org_id": orgID, "requested_teams": 1, "floor": string(contextfabric.TeamNarrowingReadFloorUndecided)},
	}); err != nil {
		t.Fatalf("certify.Certify() error = %v", err)
	}
}

// TestLiveATeamNarrowingDecidesOwnershipAtTheCurrentTimeOnAHistoricalAxis
// (CHAOS-12327, review of CHAOS-12352 r2): the reach rule is CURRENT
// ownership on every axis, as the repository walk's narrowing reads it.
// acme/owned and project Ledger were owned by team-t until 12 hours ago and
// by team-u since; a question as of 24 hours ago narrowed to team-t gets none
// of the repository's pull requests nor the project, and one narrowed to
// team-u gets them all.
func TestLiveATeamNarrowingDecidesOwnershipAtTheCurrentTimeOnAHistoricalAxis(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	orgID := "live-12327-historical-owner-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	now := time.Now().UTC()
	entities, edges := teamStatusFloorFixture(orgID, now, "", 3)
	switched := now.Add(-12 * time.Hour)
	found := false
	for i := range edges.Relationships {
		if edges.Relationships[i].RelationshipID == "relationship_owned_team" {
			edges.Relationships[i].ValidTo, found = &switched, true
		}
	}
	if !found {
		t.Fatal("the fixture has no team-t ownership edge to end")
	}
	teamU := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:team-u", Label: "Team U"}
	repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acme-owned", Label: "acme/owned"}
	created := now.Add(-48 * time.Hour)
	entities.Entities = append(entities.Entities, contextfabric.EntityProjection{Subject: teamU, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{},
		Authorization: contextfabric.AuthorizationScope{TeamIDs: []string{"team-u"}}, EvidenceRefIDs: []string{"evidence_team_u"}, ObservedAt: now, ValidFrom: &created, SourceVersion: "v1"})
	edges.Relationships = append(edges.Relationships, contextfabric.RelationshipProjection{RelationshipID: "relationship_owned_team_u", Type: contractsv1.ContextFabricRelationshipOwnedByTeam,
		From: repo, To: teamU, Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
		Authorization: contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/owned"}, TeamIDs: []string{"team-u"}}, EvidenceRefIDs: []string{"evidence_relationship_owned_team_u"},
		ObservedAt: now, ValidFrom: &switched, SourceVersion: "v1"})
	teamT := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:team-t", Label: "Team T"}
	ledger := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:ledger", Label: "Ledger"}
	started := "started"
	entities.Entities = append(entities.Entities, contextfabric.EntityProjection{Subject: ledger, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{},
		Properties: map[string]contextfabric.ScalarValue{"state": {String: &started}}, Authorization: contextfabric.AuthorizationScope{ProjectIDs: []string{"ledger"}},
		EvidenceRefIDs: []string{"evidence_project_ledger"}, ObservedAt: now, ValidFrom: &created, SourceVersion: "v1"})
	for _, o := range []struct {
		id         string
		owner      contextfabric.SubjectRef
		teamID     string
		from, till *time.Time
	}{{"relationship_ledger_team_t", teamT, "team-t", &created, &switched}, {"relationship_ledger_team_u", teamU, "team-u", &switched, nil}} {
		edges.Relationships = append(edges.Relationships, contextfabric.RelationshipProjection{RelationshipID: o.id, Type: contractsv1.ContextFabricRelationshipOwnedByTeam,
			From: ledger, To: o.owner, Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
			Authorization: contextfabric.AuthorizationScope{ProjectIDs: []string{"ledger"}, TeamIDs: []string{o.teamID}}, EvidenceRefIDs: []string{"evidence_" + o.id},
			ObservedAt: now, ValidFrom: o.from, ValidTo: o.till, SourceVersion: "v1"})
	}
	for _, b := range []contextfabric.ProjectionBatch{entities, edges} {
		if _, err := adapter.ApplyProjectionBatch(ctx, b); err != nil {
			t.Fatalf("ApplyProjectionBatch(%s) error = %v", b.BatchID, err)
		}
	}
	asOf := now.Add(-24 * time.Hour)
	historical := contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf}
	for _, tc := range []struct {
		team    string
		kind    contextfabric.SubjectKind
		status  string
		members int
	}{
		{"team-t", contextfabric.SubjectPullRequest, "open", 0}, {"team-u", contextfabric.SubjectPullRequest, "open", 3},
		{"team-t", contextfabric.SubjectProject, "started", 0}, {"team-u", contextfabric.SubjectProject, "started", 1},
	} {
		scoped := &contextfabric.ScopedSetExpression{AnchorTerms: []string{"Acme"}, MemberKind: tc.kind,
			MemberQualifier: contextfabric.MemberQualifierStatus, MemberQualifierValue: tc.status}
		request := contextfabric.GraphDiscoveryRequest{
			Request: contextfabric.InvestigationRequest{
				SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_12327_historical_owner",
				Question: "which pull requests were open a day ago?", TimeContext: historical,
				RequestedScope: contextfabric.RequestedScope{TeamIDs: []string{tc.team}},
				Options: contextfabric.InvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
					MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144},
				Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
			},
			Interpretation: contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeDiscoveredCohort, TimeContext: historical},
			Resolution:     contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{}, Candidates: []contextfabric.SubjectCandidate{}},
			Frame: &contextfabric.QuestionFrame{
				Goals:             []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
				SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionChildrenOfScope, Scoped: scoped},
				Temporal:          contextfabric.TemporalIntentCurrent, Version: contextfabric.QuestionFrameVersion,
			},
		}
		gc, err := adapter.DiscoverContext(ctx, storage.Principal{OrgID: orgID}, request)
		if err != nil {
			t.Fatalf("%s %s: DiscoverContext() error = %v", tc.team, tc.kind, err)
		}
		members := 0
		if gc.Cohort != nil {
			members = len(gc.Cohort.Members)
		}
		if members != tc.members || gc.CohortPopulation != tc.members {
			t.Fatalf("%s %s as of a day ago: members=%d population=%d, want %d: the narrowing reads the CURRENT owner", tc.team, tc.kind, members, gc.CohortPopulation, tc.members)
		}
	}
}
