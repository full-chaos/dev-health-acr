package contextfabric

import (
	"context"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The repository a named-repository question commits on prod: a label match,
// committed on a statistical basis, never a caller-supplied canonical id.
const (
	zeroRepositoryIDForTest       = "00000000-0000-0000-0000-000000000000"
	repositoryLinkInclusionReason = "Issue linked to a pull request of the named repository."
)

var repositoryWorkItemAnchor = SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:3f1c9a52-6a1e-4f0b-9d55-1c2b7e9d4a10", Label: "acme/api"}

func repositoryAnchoredResolution(anchors ...SubjectRef) SubjectResolution {
	resolution := SubjectResolution{Committed: append([]SubjectRef{}, anchors...)}
	for index, anchor := range anchors {
		resolution.Candidates = append(resolution.Candidates, SubjectCandidate{
			ReceiptID: "receipt-repository-" + string(rune('a'+index)), Subject: anchor, State: ResolutionCommitted,
			MatchedTerms: []string{anchor.Label}, MatchReasons: []string{"exact"},
		})
	}
	return resolution
}

func statisticalBases(subjects ...SubjectRef) CommitBasisSet {
	bases := make(CommitBasisSet, len(subjects))
	for _, subject := range subjects {
		bases.Record(subject, CommitBasisStatistical)
	}
	return bases
}

func repositoryMemberID(t *testing.T, repoID, workItemID string) string {
	t.Helper()
	id, omitted, err := identity.Derive(identity.KindWorkItem, []string{repoID, workItemID}, nil)
	if err != nil || omitted {
		t.Fatalf("identity.Derive(%s, %s) = %v omitted=%v", repoID, workItemID, err, omitted)
	}
	return id
}

type repositoryTupleRun struct {
	statusFilterRun
	verifiedKinds []SubjectKind
}

func runRepositoryTuple(t *testing.T, resolution SubjectResolution, frame QuestionFrame, census WorkItemMembershipCensus, members []WorkItemMembershipMember, investigation InvestigationRequest, verified bool) repositoryTupleRun {
	t.Helper()
	var run repositoryTupleRun
	frame = ValidateFrame(frame, nil, "").Frame
	run.graph = &dispatchGraphProbe{graphReaderStub: graphReaderStub{resolution: resolution, bases: statisticalBases(resolution.Committed...)}}
	telemetry := &recordingTelemetry{}
	store := &staticResultStore{results: map[string]InvestigationResult{}}
	gate, _ := NewWorkItemMembershipGate(1, 0)
	outcome := QuestionFamilyOutcome{Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel, Frame: &frame, FrameObligations: frame.Obligations, Gate: workItemTupleFrameGate(DecideFrameGate(ValidateFrame(frame, nil, ""), true), &frame, workItemTupleFamilyPolicyForTest(QuestionFamilyScopedCohortStatus), TimeContext{Axis: TemporalCurrent}), WinningSample: FamilySample{ScopeAnchorKind: SubjectRepository, ScopeAnchorTerm: "acme/api"}}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: familyInterpreter{interpreted: InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactHealth}}}, outcome: outcome},
		Graph:       run.graph,
		CandidateVerifier: func(_ context.Context, _ storage.Principal, _ RequestedScope, _ ResolvedGraphBinding, kind SubjectKind, _ string) (bool, CandidateVerificationReason) {
			run.verifiedKinds = append(run.verifiedKinds, kind)
			return verified, ""
		},
		WorkItemMembership: tupleMembershipFunc(func(ctx context.Context, _ storage.Principal, request WorkItemMembershipRequest) (*WorkItemMembershipLease, WorkItemMembershipResult, error) {
			run.reads++
			run.request = request
			lease, err := gate.Acquire(ctx)
			return lease, WorkItemMembershipResult{Census: census, Members: members}, err
		}),
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			run.factReads++
			return CanonicalFactBundle{Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-v1"}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{Status: InvestigationComplete, DirectJudgment: "Available work items.", CurrentState: "Available work items.", DeterministicAnswer: "Available work items.", StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{}, Paths: []RelationshipPath{}, Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{}, ClaimedFacts: []ClaimedFact{}, Warnings: []string{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Versions: VersionSet{Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1"}}, nil
		}), Results: store, Requirements: registryDeriver{}, Telemetry: telemetry,
	}, EngineOptions{ServiceVersion: "test", NewResultID: func() string { return "result_repository_tuple_001" }})
	if err != nil {
		t.Fatal(err)
	}
	run.result, run.invokedErr = engine.Investigate(context.Background(), storage.Principal{OrgID: "org-1"}, investigation)
	run.admissions = telemetry.workItemTupleAdmissions
	if store.savedSemantic != nil && store.savedSemantic.State != nil {
		run.savedCensus = store.savedSemantic.State.WorkItemCensus
	}
	return run
}

func TestARepositoryAnchorServesItsLinkedIssuesAsWorkItemMembers(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	tracker := repositoryMemberID(t, zeroRepositoryIDForTest, "linear:ENG-12")
	github := repositoryMemberID(t, "9b0d2c11-0f4e-4a8e-8a51-6c0e5d3f7b21", "gh:acme/web#7")
	members := []WorkItemMembershipMember{
		{CanonicalID: tracker, RepoID: zeroRepositoryIDForTest, WorkItemID: "linear:ENG-12"},
		{CanonicalID: github, RepoID: "9b0d2c11-0f4e-4a8e-8a51-6c0e5d3f7b21", WorkItemID: "gh:acme/web#7", RepoSlug: "acme/web"},
	}
	run := runRepositoryTuple(t, repositoryAnchoredResolution(repositoryWorkItemAnchor), prospectiveTupleFrame(GoalAssessState, GoalCountOrAggregate),
		WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, PopulationComplete: true, AuthorizedPopulation: 2}, members, validInvestigationRequestWithConfirmedWindow(), true)
	if run.invokedErr != nil {
		t.Fatal(run.invokedErr)
	}
	if run.reads != 1 || run.request.Anchor.Subject != repositoryWorkItemAnchor {
		t.Fatalf("membership reads=%d anchor=%+v, want one read anchored on the committed repository", run.reads, run.request.Anchor.Subject)
	}
	if len(run.verifiedKinds) == 0 || run.verifiedKinds[len(run.verifiedKinds)-1] != SubjectRepository {
		t.Fatalf("the anchor was re-checked as %v, want the repository's own kind", run.verifiedKinds)
	}
	if run.result.RefusalBasis == contractsv1.ContextFabricRefusalBasisMemberKindUnservable {
		t.Fatal("a repository anchor is still refused as member_kind_unservable")
	}
	if run.result.Cohort == nil || len(run.result.Cohort.Members) != 2 {
		t.Fatalf("cohort = %+v, want the two linked issues", run.result.Cohort)
	}
	for _, member := range run.result.Cohort.Members {
		if len(member.InclusionReasons) != 1 || member.InclusionReasons[0] != repositoryLinkInclusionReason {
			t.Fatalf("member %s inclusion reasons = %v, want the repository link reason", member.Subject.CanonicalID, member.InclusionReasons)
		}
	}
	if run.factReads != 1 {
		t.Fatalf("fact reads = %d, want 1", run.factReads)
	}
	if len(run.admissions) != 1 || !run.admissions[0].Admitted {
		t.Fatalf("settled admission = %+v", run.admissions)
	}
	if err := ValidateWorkItemTuplePayload(run.result, storage.Principal{OrgID: "org-1"}); err != nil {
		t.Fatalf("the served repository tuple is not a valid tuple payload: %v", err)
	}
}

func TestARepositoryAnchorAppliesTheStatusAndPeriodQualifiers(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	census := WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, PopulationComplete: true, AuthorizedPopulation: 1}
	members := []WorkItemMembershipMember{{CanonicalID: repositoryMemberID(t, zeroRepositoryIDForTest, "linear:ENG-12"), RepoID: zeroRepositoryIDForTest, WorkItemID: "linear:ENG-12"}}

	status := runRepositoryTuple(t, repositoryAnchoredResolution(repositoryWorkItemAnchor), statusQualifiedTupleFrame(MemberQualifierStatus, "todo"), census, members, validInvestigationRequestWithConfirmedWindow(), true)
	if status.invokedErr != nil {
		t.Fatal(status.invokedErr)
	}
	if status.reads != 1 || status.request.Status != "todo" || status.request.Anchor.Subject != repositoryWorkItemAnchor {
		t.Fatalf("status read = %d %+v, want one repository read filtered on todo", status.reads, status.request)
	}
	if !limitationsContain(status.result.Limitations, workItemStatusFilterDisclosure("todo")) {
		t.Fatalf("no status disclosure: %v", status.result.Limitations)
	}

	period := runRepositoryTuple(t, repositoryAnchoredResolution(repositoryWorkItemAnchor), periodTupleFrame(), census, members, statedPeriodRequest("Which work items of acme/api were closed in the last 30 days?"), true)
	if period.invokedErr != nil {
		t.Fatal(period.invokedErr)
	}
	if period.reads != 1 || period.request.TimeColumn != "completed_at" || period.request.TimeStart.IsZero() || period.request.Anchor.Subject != repositoryWorkItemAnchor {
		t.Fatalf("period read = %d %+v, want one repository read on completed_at", period.reads, period.request)
	}
	if period.result.Cohort == nil || len(period.result.Cohort.Members) != 1 {
		t.Fatalf("period cohort = %+v", period.result.Cohort)
	}
}

func TestARepositoryWithNoMatchingWorkItemNamesTheRepository(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	run := runRepositoryTuple(t, repositoryAnchoredResolution(repositoryWorkItemAnchor), statusQualifiedTupleFrame(MemberQualifierStatus, "todo"),
		WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, PopulationComplete: true}, nil, validInvestigationRequestWithConfirmedWindow(), true)
	if run.invokedErr != nil {
		t.Fatal(run.invokedErr)
	}
	if run.factReads != 0 || run.result.Cohort == nil || len(run.result.Cohort.Members) != 0 {
		t.Fatalf("fact reads=%d cohort=%+v", run.factReads, run.result.Cohort)
	}
	if !limitationsContain(run.result.Limitations, "No work item of this repository within the authorized scope currently has status todo") {
		t.Fatalf("the empty repository result is not named for the repository: %v", run.result.Limitations)
	}
	if limitationsContain(run.result.Limitations, "this project") {
		t.Fatalf("a repository answer speaks of a project: %v", run.result.Limitations)
	}
}

func TestARepositoryAnchorTheCallerMayNotSeeReadsNoMembers(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	run := runRepositoryTuple(t, repositoryAnchoredResolution(repositoryWorkItemAnchor), prospectiveTupleFrame(GoalAssessState),
		WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, AuthorizedPopulation: 1},
		[]WorkItemMembershipMember{{CanonicalID: repositoryMemberID(t, zeroRepositoryIDForTest, "linear:ENG-12"), RepoID: zeroRepositoryIDForTest, WorkItemID: "linear:ENG-12"}},
		validInvestigationRequestWithConfirmedWindow(), false)
	if run.invokedErr != nil {
		t.Fatal(run.invokedErr)
	}
	if run.reads != 0 || run.factReads != 0 {
		t.Fatalf("an unverified anchor read membership=%d facts=%d", run.reads, run.factReads)
	}
	if run.result.Cohort != nil && len(run.result.Cohort.Members) > 0 {
		t.Fatalf("an unverified anchor served members: %+v", run.result.Cohort)
	}
}

func TestTwoCommittedAnchorsStayRefusedForWorkItemMembers(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project-1", Label: "Project"}
	for _, resolution := range []SubjectResolution{
		repositoryAnchoredResolution(repositoryWorkItemAnchor, project),
		repositoryAnchoredResolution(repositoryWorkItemAnchor, SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:0d6e4b8a-2f3c-4e19-b7a0-5c9d8e1f2a33", Label: "acme/web"}),
		repositoryAnchoredResolution(SubjectRef{Kind: SubjectTeam, CanonicalID: "team:platform", Label: "Platform"}),
	} {
		run := runRepositoryTuple(t, resolution, prospectiveTupleFrame(GoalAssessState), WorkItemMembershipCensus{}, nil, validInvestigationRequestWithConfirmedWindow(), true)
		if run.invokedErr != nil {
			t.Fatal(run.invokedErr)
		}
		if run.reads != 0 || run.result.RefusalBasis != contractsv1.ContextFabricRefusalBasisMemberKindUnservable {
			t.Fatalf("committed %v: reads=%d refusal=%q, want refused with no read", resolution.Committed, run.reads, run.result.RefusalBasis)
		}
	}
}

func runRepositoryTupleWalk(t *testing.T, frame QuestionFrame, census WorkItemMembershipCensus, members []WorkItemMembershipMember, principal storage.Principal, requested []string) []RepositoryWorkItemWalkEvent {
	t.Helper()
	frame = ValidateFrame(frame, nil, "").Frame
	graph := &dispatchGraphProbe{graphReaderStub: graphReaderStub{resolution: repositoryAnchoredResolution(repositoryWorkItemAnchor), bases: statisticalBases(repositoryWorkItemAnchor)}}
	telemetry := &recordingTelemetry{}
	gate, _ := NewWorkItemMembershipGate(1, 0)
	outcome := QuestionFamilyOutcome{Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel, Frame: &frame, FrameObligations: frame.Obligations, Gate: workItemTupleFrameGate(DecideFrameGate(ValidateFrame(frame, nil, ""), true), &frame, workItemTupleFamilyPolicyForTest(QuestionFamilyScopedCohortStatus), TimeContext{Axis: TemporalCurrent}), WinningSample: FamilySample{ScopeAnchorKind: SubjectRepository, ScopeAnchorTerm: "acme/api"}}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: familyInterpreter{interpreted: InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactHealth}}}, outcome: outcome},
		Graph:       graph,
		CandidateVerifier: func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, SubjectKind, string) (bool, CandidateVerificationReason) {
			return true, ""
		},
		WorkItemMembership: tupleMembershipFunc(func(ctx context.Context, _ storage.Principal, _ WorkItemMembershipRequest) (*WorkItemMembershipLease, WorkItemMembershipResult, error) {
			lease, err := gate.Acquire(ctx)
			return lease, WorkItemMembershipResult{Census: census, Members: members}, err
		}),
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-v1"}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{Status: InvestigationComplete, DirectJudgment: "Available work items.", CurrentState: "Available work items.", DeterministicAnswer: "Available work items.", StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{}, Paths: []RelationshipPath{}, Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{}, ClaimedFacts: []ClaimedFact{}, Warnings: []string{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Versions: VersionSet{Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1"}}, nil
		}), Results: &staticResultStore{results: map[string]InvestigationResult{}}, Requirements: registryDeriver{}, Telemetry: telemetry,
	}, EngineOptions{ServiceVersion: "test", NewResultID: func() string { return "result_repository_walk_001" }})
	if err != nil {
		t.Fatal(err)
	}
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestedScope.RepositorySlugs = requested
	if _, err := engine.Investigate(context.Background(), principal, request); err != nil {
		t.Fatal(err)
	}
	return telemetry.repositoryWorkItemWalks
}

func TestTheRepositoryWorkItemWalkLineNamesThePathTaken(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	member := []WorkItemMembershipMember{{CanonicalID: repositoryMemberID(t, zeroRepositoryIDForTest, "linear:ENG-12"), RepoID: zeroRepositoryIDForTest, WorkItemID: "linear:ENG-12"}}
	measured := func(authorized, denied, pullRequests, linked int) WorkItemMembershipCensus {
		return WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, PopulationComplete: true, CappedPopulation: authorized + denied, AuthorizedPopulation: authorized, DeniedPopulation: denied, RepositoryPullRequests: pullRequests, RepositoryLinkedIssues: linked}
	}
	everyone := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"*"}}
	restricted := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/api"}}
	for _, tc := range []struct {
		name      string
		frame     QuestionFrame
		census    WorkItemMembershipCensus
		members   []WorkItemMembershipMember
		principal storage.Principal
		requested []string
		want      RepositoryWorkItemWalkEvent
	}{
		{"members", prospectiveTupleFrame(GoalAssessState), measured(1, 0, 3, 1), member, everyone, nil,
			RepositoryWorkItemWalkEvent{Outcome: RepositoryWorkItemWalkMembers, PullRequests: 3, LinkedIssues: 1, Members: 1, Measured: true}},
		{"no pull requests", prospectiveTupleFrame(GoalAssessState), measured(0, 0, 0, 0), nil, everyone, nil,
			RepositoryWorkItemWalkEvent{Outcome: RepositoryWorkItemWalkNoPullRequests, Measured: true}},
		{"pull requests and no linked issue", prospectiveTupleFrame(GoalAssessState), measured(0, 0, 4, 0), nil, everyone, nil,
			RepositoryWorkItemWalkEvent{Outcome: RepositoryWorkItemWalkUnlinked, PullRequests: 4, Measured: true}},
		{"linked issues none matching the filter", statusQualifiedTupleFrame(MemberQualifierStatus, "todo"), measured(0, 0, 4, 2), nil, everyone, nil,
			RepositoryWorkItemWalkEvent{Outcome: RepositoryWorkItemWalkNoMatch, PullRequests: 4, LinkedIssues: 2, Filtered: true, Measured: true}},
		{"restricted caller sees none", prospectiveTupleFrame(GoalAssessState), measured(0, 2, 4, 2), nil, restricted, nil,
			RepositoryWorkItemWalkEvent{Outcome: RepositoryWorkItemWalkDenied, PullRequests: 4, LinkedIssues: 2, Denied: 2, Restricted: true, Measured: true}},
		{"requested scope with no member is denied", prospectiveTupleFrame(GoalAssessState), measured(0, 0, 0, 0), nil, everyone, []string{"acme/api"},
			RepositoryWorkItemWalkEvent{Outcome: RepositoryWorkItemWalkDenied, Restricted: true, Measured: true}},
		{"read failed", prospectiveTupleFrame(GoalAssessState), WorkItemMembershipCensus{State: WorkItemMembershipCensusUnmeasured, UnmeasuredReason: WorkItemMembershipUnmeasuredS1Error}, nil, everyone, nil,
			RepositoryWorkItemWalkEvent{Outcome: RepositoryWorkItemWalkReadFailed, UnmeasuredReason: WorkItemMembershipUnmeasuredS1Error}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			walks := runRepositoryTupleWalk(t, tc.frame, tc.census, tc.members, tc.principal, tc.requested)
			if len(walks) != 1 || walks[0] != tc.want {
				t.Fatalf("walk lines = %+v, want exactly %+v", walks, tc.want)
			}
		})
	}
}

func TestAProjectAnchorWritesNoRepositoryWalkLine(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	run := runStatusFilterTuple(t, prospectiveTupleFrame(GoalAssessState), 1)
	if run.invokedErr != nil {
		t.Fatal(run.invokedErr)
	}
	if run.reads != 1 || len(run.walks) != 0 {
		t.Fatalf("project membership reads = %d, repository walk lines = %+v; want one read and no walk line", run.reads, run.walks)
	}
}

func TestTheRepositoryWorkItemWalkLineCarriesCountsOnlyWhenMeasured(t *testing.T) {
	for _, tc := range []struct {
		name    string
		event   RepositoryWorkItemWalkEvent
		present []string
		absent  []string
	}{
		{"measured unfiltered", RepositoryWorkItemWalkEvent{Outcome: RepositoryWorkItemWalkUnlinked, PullRequests: 4, Measured: true},
			[]string{"outcome", "anchor_kind", "filtered", "restricted", "pull_requests", "linked_issues", "members", "truncated", "denied"}, []string{"reason"}},
		{"measured filtered", RepositoryWorkItemWalkEvent{Outcome: RepositoryWorkItemWalkNoMatch, Filtered: true, Measured: true},
			[]string{"pull_requests", "linked_issues", "members", "truncated"}, []string{"denied", "reason"}},
		{"read failed", RepositoryWorkItemWalkEvent{Outcome: RepositoryWorkItemWalkReadFailed, UnmeasuredReason: WorkItemMembershipUnmeasuredS1Error},
			[]string{"outcome", "reason"}, []string{"pull_requests", "linked_issues", "members", "truncated", "denied"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			records := captureSlogJSON(t, func(logger *slog.Logger) {
				NewSlogEngineTelemetry(logger).RecordRepositoryWorkItemWalk(context.Background(), storage.Principal{OrgID: "org_sink_test"}, tc.event)
			})
			if len(records) != 1 || records[0]["msg"] != "context_fabric: repository work item walk" {
				t.Fatalf("records = %v", records)
			}
			for _, key := range tc.present {
				if _, ok := records[0][key]; !ok {
					t.Errorf("key %q absent", key)
				}
			}
			for _, key := range tc.absent {
				if _, ok := records[0][key]; ok {
					t.Errorf("key %q present", key)
				}
			}
			if records[0]["outcome"] != string(tc.event.Outcome) || records[0]["anchor_kind"] != "repository" {
				t.Errorf("outcome/anchor = %v/%v", records[0]["outcome"], records[0]["anchor_kind"])
			}
		})
	}
}
