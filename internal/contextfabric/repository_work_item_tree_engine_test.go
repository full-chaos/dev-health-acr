package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The work items of a named repository are the issues linked to its pull
// requests, walked in the graph store; the canonical facts only qualify the
// walked members. These tests drive the engine with fake ports for both.

const treeZeroRepositoryID = "00000000-0000-0000-0000-000000000000"

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

func treeMemberID(t *testing.T, repoID, workItemID string) string {
	t.Helper()
	id, omitted, err := identity.Derive(identity.KindWorkItem, []string{repoID, workItemID}, nil)
	if err != nil || omitted {
		t.Fatalf("identity.Derive(%s, %s) = %v omitted=%v", repoID, workItemID, err, omitted)
	}
	return id
}

func treeMember(t *testing.T, workItemID, tier string) TreeWorkItemMember {
	t.Helper()
	id := treeMemberID(t, treeZeroRepositoryID, workItemID)
	return TreeWorkItemMember{Subject: SubjectRef{Kind: SubjectWorkItem, CanonicalID: id, Label: workItemID}, Tier: tier}
}

type treeGraphFake struct {
	walk    TreeWorkItemWalk
	err     error
	calls   int
	anchor  SubjectRef
	limit   int
	binding ResolvedGraphBinding
}

func (f *treeGraphFake) TreeWorkItemMembers(_ context.Context, _ storage.Principal, binding ResolvedGraphBinding, _ RequestedScope, anchor SubjectRef, limit int) (TreeWorkItemWalk, error) {
	f.calls++
	f.anchor, f.limit, f.binding = anchor, limit, binding
	return f.walk, f.err
}

type treeFilterFake struct {
	// keep, when set, names the members the fake finds matching; every walked
	// member matches otherwise.
	keep     map[string]bool
	unread   int
	err      error
	requests []TreeWorkItemFilterRequest
}

func (f *treeFilterFake) FilterTreeWorkItems(_ context.Context, _ storage.Principal, request TreeWorkItemFilterRequest) ([]string, int, error) {
	f.requests = append(f.requests, request)
	if f.err != nil {
		return nil, 0, f.err
	}
	kept := []string{}
	for _, member := range request.Members {
		if f.keep == nil || f.keep[member.CanonicalID] {
			kept = append(kept, member.CanonicalID)
		}
	}
	return kept, f.unread, nil
}

type repositoryTreeRun struct {
	result       InvestigationResult
	err          error
	graph        *treeGraphFake
	filter       *treeFilterFake
	walks        []RepositoryWorkItemWalkEvent
	savedCensus  *WorkItemTupleCensus
	projectReads int
	factReads    int
	verified     []SubjectKind
}

type repositoryTreeCase struct {
	frame     QuestionFrame
	request   InvestigationRequest
	principal storage.Principal
	walk      TreeWorkItemWalk
	walkErr   error
	filter    *treeFilterFake
	resolve   SubjectResolution
}

func runRepositoryTree(t *testing.T, c repositoryTreeCase) repositoryTreeRun {
	t.Helper()
	if c.frame.Version == "" && c.frame.SubjectExpression.Kind == "" {
		c.frame = prospectiveTupleFrame(GoalAssessState, GoalCountOrAggregate)
	}
	if c.request.Question == "" {
		c.request = validInvestigationRequestWithConfirmedWindow()
	}
	if c.principal.OrgID == "" {
		c.principal = storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"*"}}
	}
	if c.resolve.Committed == nil {
		c.resolve = repositoryAnchoredResolution(repositoryWorkItemAnchor)
	}
	run := repositoryTreeRun{graph: &treeGraphFake{walk: c.walk, err: c.walkErr}, filter: c.filter}
	if run.filter == nil {
		run.filter = &treeFilterFake{}
	}
	frame := ValidateFrame(c.frame, nil, "").Frame
	probe := &dispatchGraphProbe{graphReaderStub: graphReaderStub{resolution: c.resolve, bases: statisticalBases(c.resolve.Committed...)}}
	telemetry := &recordingTelemetry{}
	store := &staticResultStore{results: map[string]InvestigationResult{}}
	gate, _ := NewWorkItemMembershipGate(1, 0)
	outcome := QuestionFamilyOutcome{Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel, Frame: &frame, FrameObligations: frame.Obligations, Gate: workItemTupleFrameGate(DecideFrameGate(ValidateFrame(frame, nil, ""), true), &frame, workItemTupleFamilyPolicyForTest(QuestionFamilyScopedCohortStatus), TimeContext{Axis: TemporalCurrent}), WinningSample: FamilySample{ScopeAnchorKind: SubjectRepository, ScopeAnchorTerm: "acme/api"}}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: familyInterpreter{interpreted: InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactHealth}}}, outcome: outcome},
		Graph:       probe,
		CandidateVerifier: func(_ context.Context, _ storage.Principal, _ RequestedScope, _ ResolvedGraphBinding, kind SubjectKind, _ string) (bool, CandidateVerificationReason) {
			run.verified = append(run.verified, kind)
			return true, ""
		},
		// The project read must never serve a repository anchor.
		WorkItemMembership: tupleMembershipFunc(func(context.Context, storage.Principal, WorkItemMembershipRequest) (*WorkItemMembershipLease, WorkItemMembershipResult, error) {
			run.projectReads++
			return nil, WorkItemMembershipResult{}, errors.New("the project read must not serve a repository anchor")
		}),
		TreeWorkItemGraph: run.graph, TreeWorkItemFilter: run.filter, TreeWorkItemGate: gate,
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			run.factReads++
			return CanonicalFactBundle{Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-v1"}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{Status: InvestigationComplete, DirectJudgment: "Available work items.", CurrentState: "Available work items.", DeterministicAnswer: "Available work items.", StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{}, Paths: []RelationshipPath{}, Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{}, ClaimedFacts: []ClaimedFact{}, Warnings: []string{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Versions: VersionSet{Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1"}}, nil
		}), Results: store, Requirements: registryDeriver{}, Telemetry: telemetry,
	}, EngineOptions{ServiceVersion: "test", NewResultID: func() string { return "result_repository_tree_001" }})
	if err != nil {
		t.Fatal(err)
	}
	run.result, run.err = engine.Investigate(context.Background(), c.principal, c.request)
	run.walks = telemetry.repositoryWorkItemWalks
	if store.savedSemantic != nil && store.savedSemantic.State != nil {
		run.savedCensus = store.savedSemantic.State.WorkItemCensus
	}
	return run
}

func cohortReasons(result InvestigationResult) map[string]string {
	reasons := map[string]string{}
	if result.Cohort != nil {
		for _, member := range result.Cohort.Members {
			reasons[member.Subject.Label] = strings.Join(member.InclusionReasons, "|")
		}
	}
	return reasons
}

func TestARepositoryServesItsLinkedIssuesFromTheWalkWithEachTierNamed(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	run := runRepositoryTree(t, repositoryTreeCase{walk: TreeWorkItemWalk{
		Members: []TreeWorkItemMember{
			treeMember(t, "ENG-1", TreeLinkTierNative),
			treeMember(t, "ENG-2", TreeLinkTierExplicitText),
			treeMember(t, "ENG-3", TreeLinkTierHeuristic),
		},
		PullRequests: 4, LinkedIssues: 3,
	}})
	if run.err != nil {
		t.Fatal(run.err)
	}
	if run.projectReads != 0 {
		t.Fatalf("the project read served a repository anchor %d times", run.projectReads)
	}
	if run.graph.calls != 1 || run.graph.anchor != repositoryWorkItemAnchor || run.graph.limit != WorkItemMembershipCensusLimit+1 {
		t.Fatalf("walk calls=%d anchor=%+v limit=%d, want one walk of the committed repository at the census limit plus one", run.graph.calls, run.graph.anchor, run.graph.limit)
	}
	if len(run.filter.requests) != 0 {
		t.Fatalf("an unfiltered read called the filter: %+v", run.filter.requests)
	}
	if !slices.Contains(run.verified, SubjectRepository) || slices.Contains(run.verified, SubjectProject) {
		t.Fatalf("the anchor was re-checked as %v, want the repository's own kind", run.verified)
	}
	if run.result.RefusalBasis == contractsv1.ContextFabricRefusalBasisMemberKindUnservable {
		t.Fatal("a repository anchor is refused as member_kind_unservable")
	}
	want := map[string]string{
		"ENG-1": "Issue linked to a pull request of the named repository (native link)",
		"ENG-2": "Issue linked to a pull request of the named repository (link stated in text)",
		"ENG-3": "Issue linked to a pull request of the named repository (heuristic match)",
	}
	got := cohortReasons(run.result)
	for id, reason := range want {
		if got[id] != reason {
			t.Errorf("member %s reason = %q, want %q", id, got[id], reason)
		}
	}
	if run.result.Cohort == nil || run.result.Cohort.Rationale != contractsv1.ContextFabricWorkItemRepositoryMembershipRationale {
		t.Fatalf("cohort rationale = %+v", run.result.Cohort)
	}
	if run.factReads != 1 {
		t.Fatalf("fact reads = %d, want 1", run.factReads)
	}
	if err := ValidateWorkItemTuplePayload(run.result, storage.Principal{OrgID: "org-1"}); err != nil {
		t.Fatalf("the served repository tuple is not a valid tuple payload: %v", err)
	}
	if len(run.walks) != 1 || run.walks[0] != (RepositoryWorkItemWalkEvent{Outcome: RepositoryWorkItemWalkMembers, PullRequests: 4, LinkedIssues: 3, Members: 3, Measured: true}) {
		t.Fatalf("walk lines = %+v", run.walks)
	}
}

func TestAHeuristicOnlyMemberIsCountedInADisclosure(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	disclosure := func(n int) string {
		return fmt.Sprintf("%d of these members are linked only by a heuristic match (a pull request opened near the issue's last update in the issue's own repository).", n)
	}
	two := runRepositoryTree(t, repositoryTreeCase{walk: TreeWorkItemWalk{Members: []TreeWorkItemMember{treeMember(t, "ENG-1", TreeLinkTierNative), treeMember(t, "ENG-2", TreeLinkTierHeuristic), treeMember(t, "ENG-3", TreeLinkTierHeuristic)}, PullRequests: 2, LinkedIssues: 3}})
	if two.err != nil {
		t.Fatal(two.err)
	}
	if !limitationsContain(two.result.Limitations, disclosure(2)) {
		t.Fatalf("no heuristic disclosure of two: %v", two.result.Limitations)
	}
	if !contractsv1.IsContextFabricServiceAuthoredLimitation(disclosure(2)) {
		t.Fatal("the heuristic disclosure is not recognised as service-authored, so a later composer could displace it")
	}
	none := runRepositoryTree(t, repositoryTreeCase{walk: TreeWorkItemWalk{Members: []TreeWorkItemMember{treeMember(t, "ENG-1", TreeLinkTierNative), treeMember(t, "ENG-2", TreeLinkTierExplicitText)}, PullRequests: 2, LinkedIssues: 2}})
	for _, limitation := range none.result.Limitations {
		if strings.Contains(limitation, "heuristic") {
			t.Fatalf("a read with no heuristic-only member discloses one: %q", limitation)
		}
	}
	// A tier the vocabulary does not name is read as the weakest, never as native.
	unknown := runRepositoryTree(t, repositoryTreeCase{walk: TreeWorkItemWalk{Members: []TreeWorkItemMember{treeMember(t, "ENG-1", "")}, PullRequests: 1, LinkedIssues: 1}})
	if got := cohortReasons(unknown.result)["ENG-1"]; !strings.HasSuffix(got, "(heuristic match)") {
		t.Fatalf("a member with no known tier is presented as %q", got)
	}
}

func TestTheStatusAndCompletedWindowFilterTheWalkedMembersThroughTheFactFilter(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	a, b, c := treeMember(t, "ENG-1", TreeLinkTierNative), treeMember(t, "ENG-2", TreeLinkTierNative), treeMember(t, "ENG-3", TreeLinkTierExplicitText)
	walk := TreeWorkItemWalk{Members: []TreeWorkItemMember{a, b, c}, PullRequests: 3, LinkedIssues: 3, Denied: 5}

	statusFilter := &treeFilterFake{keep: map[string]bool{b.Subject.CanonicalID: true}}
	status := runRepositoryTree(t, repositoryTreeCase{frame: statusQualifiedTupleFrame(MemberQualifierStatus, "todo"), walk: walk, filter: statusFilter})
	if status.err != nil {
		t.Fatal(status.err)
	}
	if len(statusFilter.requests) != 1 || statusFilter.requests[0].Status != "todo" || !statusFilter.requests[0].CompletedStart.IsZero() || len(statusFilter.requests[0].Members) != 3 {
		t.Fatalf("filter requests = %+v, want one status read over the three walked members", statusFilter.requests)
	}
	if got := cohortReasons(status.result); len(got) != 1 || got["ENG-2"] == "" {
		t.Fatalf("status cohort = %v, want only the member the filter kept", got)
	}
	if status.savedCensus == nil || status.savedCensus.State != WorkItemMembershipCensusExact || status.savedCensus.Value != 1 {
		t.Fatalf("census = %+v, want exact over the kept population of one", status.savedCensus)
	}
	if !limitationsContain(status.result.Limitations, workItemStatusFilterDisclosure("todo")) {
		t.Fatalf("no status disclosure: %v", status.result.Limitations)
	}
	if len(status.walks) != 1 || !status.walks[0].Filtered || status.walks[0].Denied != 0 {
		t.Fatalf("a filtered walk line carries a denied count or is not marked filtered: %+v", status.walks)
	}

	windowFilter := &treeFilterFake{keep: map[string]bool{a.Subject.CanonicalID: true, c.Subject.CanonicalID: true}}
	period := runRepositoryTree(t, repositoryTreeCase{frame: periodTupleFrame(), request: statedPeriodRequest("Which work items of acme/api were closed in the last 30 days?"), walk: walk, filter: windowFilter})
	if period.err != nil {
		t.Fatal(period.err)
	}
	if len(windowFilter.requests) != 1 || windowFilter.requests[0].CompletedStart.IsZero() || !windowFilter.requests[0].CompletedEnd.After(windowFilter.requests[0].CompletedStart) || windowFilter.requests[0].Status != "" {
		t.Fatalf("filter requests = %+v, want one completed-window read", windowFilter.requests)
	}
	if got := cohortReasons(period.result); len(got) != 2 || got["ENG-1"] == "" || got["ENG-3"] == "" {
		t.Fatalf("period cohort = %v", got)
	}
}

func TestACreatedOrUpdatedPeriodOnARepositoryIsRefusedAndNamed(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	for _, question := range []string{
		"Which work items of acme/api were created in the last 30 days?",
		"Which work items of acme/api were updated in the last 30 days?",
	} {
		run := runRepositoryTree(t, repositoryTreeCase{frame: periodTupleFrame(), request: statedPeriodRequest(question), walk: TreeWorkItemWalk{Members: []TreeWorkItemMember{treeMember(t, "ENG-1", TreeLinkTierNative)}, PullRequests: 1, LinkedIssues: 1}})
		if run.err != nil {
			t.Fatal(run.err)
		}
		if run.graph.calls != 0 || len(run.filter.requests) != 0 || run.factReads != 0 {
			t.Fatalf("%q: a refused period walked=%d filtered=%d fact reads=%d", question, run.graph.calls, len(run.filter.requests), run.factReads)
		}
		if run.result.Cohort != nil && len(run.result.Cohort.Members) > 0 {
			t.Fatalf("%q: a refused period served members: %+v", question, run.result.Cohort)
		}
		if !limitationsContain(run.result.Limitations, contractsv1.ContextFabricWorkItemRepositoryPeriodRoleRefusalLimitation) {
			t.Fatalf("%q: the refusal is not named: %v", question, run.result.Limitations)
		}
	}
	// The completed role is served, through the completion filter.
	served := runRepositoryTree(t, repositoryTreeCase{frame: periodTupleFrame(), request: statedPeriodRequest("Which work items of acme/api were closed in the last 30 days?"), walk: TreeWorkItemWalk{Members: []TreeWorkItemMember{treeMember(t, "ENG-1", TreeLinkTierNative)}, PullRequests: 1, LinkedIssues: 1}})
	if served.err != nil || served.graph.calls != 1 || len(served.filter.requests) != 1 {
		t.Fatalf("completed period: err=%v walks=%d filters=%d", served.err, served.graph.calls, len(served.filter.requests))
	}
}

func TestACutWalkIsAFloorAndAnUnreadMemberMakesTheCountALowerBound(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	many := make([]TreeWorkItemMember, 0, WorkItemMembershipCensusLimit+1)
	for i := 0; i <= WorkItemMembershipCensusLimit; i++ {
		many = append(many, treeMember(t, fmt.Sprintf("ENG-%05d", i), TreeLinkTierNative))
	}
	over := runRepositoryTree(t, repositoryTreeCase{walk: TreeWorkItemWalk{Members: many, PullRequests: 9, LinkedIssues: 2001, Truncated: true}})
	if over.err != nil {
		t.Fatal(over.err)
	}
	if over.savedCensus == nil || over.savedCensus.State != WorkItemMembershipCensusFloor || over.savedCensus.Value != WorkItemMembershipCensusLimit {
		t.Fatalf("census = %+v, want a floor at the census limit", over.savedCensus)
	}
	if over.result.Cohort == nil || !over.result.Cohort.Truncated || len(over.result.Cohort.Members) > WorkItemMembershipServeLimit {
		t.Fatalf("cohort = %+v, want a truncated cohort within the serve limit", over.result.Cohort)
	}

	// A walk cut at its read bound is a lower bound without being a floor: a
	// floor's value is pinned to the census limit and would claim more than was read.
	bound := runRepositoryTree(t, repositoryTreeCase{walk: TreeWorkItemWalk{Members: many[:3], PullRequests: 9, LinkedIssues: 3, Truncated: true}})
	if bound.err != nil {
		t.Fatal(bound.err)
	}
	if bound.savedCensus == nil || bound.savedCensus.State != WorkItemMembershipCensusExact || bound.savedCensus.Value != 3 {
		t.Fatalf("census = %+v, want exact over the three read", bound.savedCensus)
	}
	if bound.result.Cohort == nil || bound.result.Cohort.Complete || !limitationsContain(bound.result.Limitations, contractsv1.ContextFabricWorkItemRepositoryPartialLimitation) {
		t.Fatalf("a cut walk is served as complete: cohort=%+v limitations=%v", bound.result.Cohort, bound.result.Limitations)
	}
	if len(bound.walks) != 1 || !bound.walks[0].Truncated {
		t.Fatalf("walk lines = %+v, want one marked truncated", bound.walks)
	}

	// Unread members under a filter: the kept set is a lower bound.
	filter := &treeFilterFake{keep: map[string]bool{many[1].Subject.CanonicalID: true}, unread: 2}
	unread := runRepositoryTree(t, repositoryTreeCase{frame: statusQualifiedTupleFrame(MemberQualifierStatus, "todo"), walk: TreeWorkItemWalk{Members: many[:4], PullRequests: 4, LinkedIssues: 4}, filter: filter})
	if unread.err != nil {
		t.Fatal(unread.err)
	}
	if unread.result.Cohort == nil || unread.result.Cohort.Complete || len(unread.result.Cohort.Members) != 1 {
		t.Fatalf("an unread member left the cohort complete: %+v", unread.result.Cohort)
	}
	if !limitationsContain(unread.result.Limitations, contractsv1.ContextFabricWorkItemRepositoryPartialLimitation) {
		t.Fatalf("no disclosure of the unread members: %v", unread.result.Limitations)
	}
	// Zero kept with an unread member is not a measured "no match".
	none := runRepositoryTree(t, repositoryTreeCase{frame: statusQualifiedTupleFrame(MemberQualifierStatus, "todo"), walk: TreeWorkItemWalk{Members: many[:4], PullRequests: 4, LinkedIssues: 4}, filter: &treeFilterFake{keep: map[string]bool{}, unread: 1}})
	if none.err != nil {
		t.Fatal(none.err)
	}
	for _, limitation := range none.result.Limitations {
		if strings.HasPrefix(limitation, contractsv1.ContextFabricWorkItemRepositoryNoMatchLimitationPrefix) {
			t.Fatalf("an unread member was served as a measured empty result: %q", limitation)
		}
	}
}

func TestEachRepositoryOutcomeIsOneDecisionLineWithItsOwnDisclosure(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	one := []TreeWorkItemMember{treeMember(t, "ENG-1", TreeLinkTierNative)}
	everyone := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"*"}}
	restricted := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/api"}}
	hasUnlinkedCode := func(result InvestigationResult) (int, bool) {
		for _, detail := range result.Coverage.Details {
			if detail.Code == contractsv1.ContextFabricCoverageDetailWorkItemRepositoryUnlinked {
				return *detail.Count, true
			}
		}
		return 0, false
	}
	for _, tc := range []struct {
		name         string
		c            repositoryTreeCase
		want         RepositoryWorkItemWalkEvent
		sentence     string
		unlinkedCode int
	}{
		{"members", repositoryTreeCase{walk: TreeWorkItemWalk{Members: one, PullRequests: 3, LinkedIssues: 1}},
			RepositoryWorkItemWalkEvent{Outcome: RepositoryWorkItemWalkMembers, PullRequests: 3, LinkedIssues: 1, Members: 1, Measured: true}, "", -1},
		{"no pull requests", repositoryTreeCase{walk: TreeWorkItemWalk{}},
			RepositoryWorkItemWalkEvent{Outcome: RepositoryWorkItemWalkNoPullRequests, Measured: true}, contractsv1.ContextFabricWorkItemRepositoryNoPullRequestsLimitation, -1},
		{"unlinked", repositoryTreeCase{walk: TreeWorkItemWalk{PullRequests: 4}},
			RepositoryWorkItemWalkEvent{Outcome: RepositoryWorkItemWalkUnlinked, PullRequests: 4, Measured: true}, contractsv1.ContextFabricWorkItemRepositoryUnlinkedLimitation, 4},
		{"no match", repositoryTreeCase{frame: statusQualifiedTupleFrame(MemberQualifierStatus, "todo"), walk: TreeWorkItemWalk{Members: one, PullRequests: 4, LinkedIssues: 2}, filter: &treeFilterFake{keep: map[string]bool{}}},
			RepositoryWorkItemWalkEvent{Outcome: RepositoryWorkItemWalkNoMatch, PullRequests: 4, LinkedIssues: 2, Filtered: true, Measured: true}, workItemNoMatchDisclosure(SubjectRepository, "currently has status todo"), -1},
		{"denied", repositoryTreeCase{principal: restricted, walk: TreeWorkItemWalk{PullRequests: 4, LinkedIssues: 2, Denied: 2}},
			RepositoryWorkItemWalkEvent{Outcome: RepositoryWorkItemWalkDenied, PullRequests: 4, LinkedIssues: 2, Denied: 2, Restricted: true, Measured: true}, workItemStatusDeniedExclusion, -1},
		{"every linked issue denied", repositoryTreeCase{walk: TreeWorkItemWalk{PullRequests: 2, LinkedIssues: 3, Denied: 3}},
			RepositoryWorkItemWalkEvent{Outcome: RepositoryWorkItemWalkNoMatch, PullRequests: 2, LinkedIssues: 3, Denied: 3, Measured: true},
			(workItemAuthorizationGap{AnchorKind: SubjectRepository, State: WorkItemMembershipCensusExact, Observed: 3, Denied: 3}).Limitation(), -1},
		{"unlinked but cut is no gap code", repositoryTreeCase{walk: TreeWorkItemWalk{PullRequests: 4, Truncated: true}},
			RepositoryWorkItemWalkEvent{Outcome: RepositoryWorkItemWalkUnlinked, PullRequests: 4, Truncated: true, Measured: true}, contractsv1.ContextFabricWorkItemRepositoryUnlinkedLimitation, -1},
		{"read failed", repositoryTreeCase{walkErr: errors.New("graph down")},
			RepositoryWorkItemWalkEvent{Outcome: RepositoryWorkItemWalkReadFailed, UnmeasuredReason: WorkItemMembershipUnmeasuredS1Error}, "", -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.c.principal = firstPrincipal(tc.c.principal, everyone)
			run := runRepositoryTree(t, tc.c)
			if run.err != nil {
				t.Fatal(run.err)
			}
			if len(run.walks) != 1 || run.walks[0] != tc.want {
				t.Fatalf("walk lines = %+v, want exactly %+v", run.walks, tc.want)
			}
			if tc.sentence != "" {
				if !limitationsContain(run.result.Limitations, tc.sentence) {
					t.Fatalf("limitations %v lack %q", run.result.Limitations, tc.sentence)
				}
				// The authorization-gap sentence is recognised by its own prefix
				// (hasWorkItemAuthorizationGapLimitation), for a project as for a repository.
				if !contractsv1.IsContextFabricServiceAuthoredLimitation(tc.sentence) && !hasWorkItemAuthorizationGapLimitation([]string{tc.sentence}) {
					t.Fatalf("%q is not recognised as service-authored", tc.sentence)
				}
			}
			count, coded := hasUnlinkedCode(run.result)
			if (tc.unlinkedCode >= 0) != coded || (coded && count != tc.unlinkedCode) {
				t.Fatalf("unlinked coverage code present=%t count=%d, want count=%d", coded, count, tc.unlinkedCode)
			}
			if !limitationsContain(run.result.Limitations, contractsv1.ContextFabricWorkItemRepositoryFreshnessLimitation) {
				t.Fatalf("no freshness sentence: %v", run.result.Limitations)
			}
		})
	}
}

func firstPrincipal(p, fallback storage.Principal) storage.Principal {
	if p.OrgID != "" {
		return p
	}
	return fallback
}

func TestARestrictedCallerWithNoMemberIsServedTheNeutralReasonNotTheCounts(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	restricted := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/api"}}
	hidden := runRepositoryTree(t, repositoryTreeCase{principal: restricted, walk: TreeWorkItemWalk{PullRequests: 7, LinkedIssues: 5, Denied: 5}})
	unlinked := runRepositoryTree(t, repositoryTreeCase{principal: restricted, walk: TreeWorkItemWalk{PullRequests: 7}})
	if hidden.err != nil || unlinked.err != nil {
		t.Fatal(hidden.err, unlinked.err)
	}
	for name, run := range map[string]repositoryTreeRun{"hidden": hidden, "unlinked": unlinked} {
		for _, limitation := range run.result.Limitations {
			if strings.HasPrefix(limitation, "Work items exist in this repository") || limitation == contractsv1.ContextFabricWorkItemRepositoryUnlinkedLimitation || limitation == contractsv1.ContextFabricWorkItemRepositoryNoPullRequestsLimitation {
				t.Fatalf("%s: a restricted caller is told %q", name, limitation)
			}
		}
		if len(run.result.Coverage.Details) > 0 {
			for _, detail := range run.result.Coverage.Details {
				if detail.Code == contractsv1.ContextFabricCoverageDetailWorkItemRepositoryUnlinked {
					t.Fatalf("%s: a restricted caller is given the unlinked coverage code", name)
				}
			}
		}
		if !limitationsContain(run.result.Limitations, workItemStatusDeniedExclusion) {
			t.Fatalf("%s: no neutral reason: %v", name, run.result.Limitations)
		}
		if run.walks[0].Outcome != RepositoryWorkItemWalkDenied {
			t.Fatalf("%s: outcome %q, want denied", name, run.walks[0].Outcome)
		}
	}
	if hidden.result.Status != unlinked.result.Status || !slices.Equal(hidden.result.Limitations, unlinked.result.Limitations) {
		t.Fatalf("the served answer tells a hidden repository from an unlinked one:\nhidden   %v %v\nunlinked %v %v", hidden.result.Status, hidden.result.Limitations, unlinked.result.Status, unlinked.result.Limitations)
	}
}

func TestAWalkOrFilterErrorIsUnmeasuredNeverAnEmptySuccess(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	member := []TreeWorkItemMember{treeMember(t, "ENG-1", TreeLinkTierNative)}
	for name, c := range map[string]repositoryTreeCase{
		"walk":   {walkErr: errors.New("graph down")},
		"filter": {frame: statusQualifiedTupleFrame(MemberQualifierStatus, "todo"), walk: TreeWorkItemWalk{Members: member, PullRequests: 1, LinkedIssues: 1}, filter: &treeFilterFake{err: errors.New("clickhouse down")}},
	} {
		run := runRepositoryTree(t, c)
		if run.err != nil {
			t.Fatalf("%s: %v", name, run.err)
		}
		if run.savedCensus == nil || run.savedCensus.State != WorkItemMembershipCensusUnmeasured {
			t.Fatalf("%s: census = %+v, want unmeasured", name, run.savedCensus)
		}
		if run.result.Cohort != nil && len(run.result.Cohort.Members) > 0 {
			t.Fatalf("%s: members served after a failed read", name)
		}
		if !limitationsContain(run.result.Limitations, WorkItemMembershipLimitation()) {
			t.Fatalf("%s: the failed read is not disclosed: %v", name, run.result.Limitations)
		}
		for _, limitation := range run.result.Limitations {
			if strings.HasPrefix(limitation, contractsv1.ContextFabricWorkItemRepositoryNoMatchLimitationPrefix) || limitation == contractsv1.ContextFabricWorkItemRepositoryUnlinkedLimitation {
				t.Fatalf("%s: a failed read is served as an empty result: %q", name, limitation)
			}
		}
		if len(run.walks) != 1 || run.walks[0].Outcome != RepositoryWorkItemWalkReadFailed || run.walks[0].UnmeasuredReason != WorkItemMembershipUnmeasuredS1Error || run.walks[0].Measured {
			t.Fatalf("%s: walk lines = %+v", name, run.walks)
		}
	}
}

func TestTheProjectSentencesAreByteIdenticalAndTheRepositoryFormsArePinned(t *testing.T) {
	// Project: unchanged, byte for byte.
	pinned := map[string]string{
		workItemStatusNoMatchDisclosure("todo"):                                                                 "No work item in this project within the authorized scope currently has status todo; that is a count of matches, not a statement about the project's health.",
		workItemNoMatchDisclosure(SubjectProject, "was completed in that period"):                               "No work item in this project within the authorized scope was completed in that period; that is a count of matches, not a statement about the project's health.",
		workItemNoMatchDisclosure("", "currently has status todo"):                                              "No work item in this project within the authorized scope currently has status todo; that is a count of matches, not a statement about the project's health.",
		workItemMembershipRationale:                                                                             "Work items are members of the resolved project within the authorized scope.",
		workItemMembershipRationaleFor(SubjectProject):                                                          "Work items are members of the resolved project within the authorized scope.",
		workItemAuthorizationGapPrefix:                                                                          "Work items exist in this project that are outside this principal's authorized scope",
		(workItemAuthorizationGap{State: WorkItemMembershipCensusExact, Authorized: 1, Denied: 2}).Limitation(): "Work items exist in this project that are outside this principal's authorized scope: 1 work items are authorized and 2 more are denied and are not counted.",
		// Repository: the relation is stated, and the forms say "this repository".
		workItemNoMatchDisclosure(SubjectRepository, "currently has status todo"):                                                                     "No work item of this repository within the authorized scope currently has status todo; that is a count of matches, not a statement about the repository's health.",
		workItemMemberFilterNoMatchDisclosure(workItemMemberFilter{AnchorKind: SubjectRepository, TimeRole: MemberTimeRoleCompleted, Status: "done"}): "No work item of this repository within the authorized scope was completed in that period and a current status of done; that is a count of matches, not a statement about the repository's health.",
		(workItemAuthorizationGap{AnchorKind: SubjectRepository, State: WorkItemMembershipCensusExact, Authorized: 1, Denied: 2}).Limitation():        "Work items exist in this repository that are outside this principal's authorized scope: 1 work items are authorized and 2 more are denied and are not counted.",
		contractsv1.ContextFabricWorkItemRepositoryMembershipReason(TreeLinkTierNative):                                                               "Issue linked to a pull request of the named repository (native link)",
		contractsv1.ContextFabricWorkItemRepositoryMembershipReason(TreeLinkTierExplicitText):                                                         "Issue linked to a pull request of the named repository (link stated in text)",
		contractsv1.ContextFabricWorkItemRepositoryMembershipReason(TreeLinkTierHeuristic):                                                            "Issue linked to a pull request of the named repository (heuristic match)",
		contractsv1.ContextFabricWorkItemRepositoryFreshnessLimitation:                                                                                "The issue to pull request links come from the last link build and can lag behind the source.",
	}
	for got, want := range pinned {
		if got != want {
			t.Errorf("sentence changed:\n got %q\nwant %q", got, want)
		}
	}
	for _, sentence := range []string{
		workItemNoMatchDisclosure(SubjectRepository, "currently has status todo"),
		workItemMemberFilterNoMatchDisclosure(workItemMemberFilter{AnchorKind: SubjectRepository, TimeRole: MemberTimeRoleCompleted}),
	} {
		if !contractsv1.IsContextFabricServiceAuthoredLimitation(sentence) {
			t.Errorf("not recognised as service-authored: %q", sentence)
		}
	}
	// The tier names the contract writes are the walk's own.
	if contractsv1.ContextFabricWorkItemRepositoryTierNative != TreeLinkTierNative || contractsv1.ContextFabricWorkItemRepositoryTierExplicitText != TreeLinkTierExplicitText || contractsv1.ContextFabricWorkItemRepositoryTierHeuristic != TreeLinkTierHeuristic {
		t.Fatal("the contract's tier names are not the walk's")
	}
}

func TestTwoCommittedAnchorsAndATeamStayRefusedForWorkItemMembers(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project-1", Label: "Project"}
	for _, resolution := range []SubjectResolution{
		repositoryAnchoredResolution(repositoryWorkItemAnchor, project),
		repositoryAnchoredResolution(repositoryWorkItemAnchor, SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:0d6e4b8a-2f3c-4e19-b7a0-5c9d8e1f2a33", Label: "acme/web"}),
		repositoryAnchoredResolution(SubjectRef{Kind: SubjectTeam, CanonicalID: "team:platform", Label: "Platform"}),
	} {
		run := runRepositoryTree(t, repositoryTreeCase{resolve: resolution})
		if run.err != nil {
			t.Fatal(run.err)
		}
		if run.graph.calls != 0 || run.result.RefusalBasis != contractsv1.ContextFabricRefusalBasisMemberKindUnservable {
			t.Fatalf("committed %v: walks=%d refusal=%q, want refused with no walk", resolution.Committed, run.graph.calls, run.result.RefusalBasis)
		}
	}
}

func TestTheRepositoryWalkLineCarriesCountsOnlyWhenMeasured(t *testing.T) {
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

// repositoryTreeReuseFixture is the project reuse fixture re-anchored on a
// repository: the stored answer, its reading and its census name the
// repository, and the stored member's reason names the tier of its link.
func repositoryTreeReuseFixture(t *testing.T, tier string) (storage.Principal, InvestigationRequest, StoredInvestigationResult, TreeWorkItemWalk) {
	t.Helper()
	principal, request, stored, current := tupleReuseFixture(t)
	request.RequestedScope.ProjectIDs = nil
	anchor := repositoryWorkItemAnchor
	candidate := stored.Result.SubjectResolution.Candidates[0]
	candidate.Subject = anchor
	candidate.MatchedTerms = []string{anchor.Label}
	stored.Result.SubjectResolution.Candidates = []SubjectCandidate{candidate}
	stored.Result.SubjectResolution.Committed = []SubjectRef{anchor}
	stored.Result.SubjectResolution.CommitDecisionDigests = identityProvenDigests(anchor)
	for index := range stored.Result.ClaimedFacts {
		if stored.Result.ClaimedFacts[index].Subject.Kind == SubjectProject {
			stored.Result.ClaimedFacts[index].Subject = anchor
		}
	}
	stored.SemanticState.ScopeAnchor = SemanticScopeAnchor{Kind: SubjectRepository, Term: anchor.Label}
	stored.SemanticState.Frame.SubjectExpression.Scoped.AnchorTerms = []string{anchor.Label}
	stored.SemanticState.WorkItemCensus.Value = 1
	stored.Result.Cohort.Members[0].InclusionReasons = []string{contractsv1.ContextFabricWorkItemRepositoryMembershipReason(tier)}
	walk := TreeWorkItemWalk{PullRequests: 2, LinkedIssues: 1}
	for _, member := range current.Members {
		walk.Members = append(walk.Members, TreeWorkItemMember{Subject: SubjectRef{Kind: SubjectWorkItem, CanonicalID: member.CanonicalID}, Tier: tier})
	}
	return principal, request, stored, walk
}

func reuseOnTheRepositoryWalk(t *testing.T, principal storage.Principal, request InvestigationRequest, stored StoredInvestigationResult, walk TreeWorkItemWalk, verified bool) (hit bool, verifiedKinds []SubjectKind, graph *treeGraphFake, walks []RepositoryWorkItemWalkEvent) {
	t.Helper()
	gate, err := NewWorkItemMembershipGate(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, owner := NewWorkItemResponseOwnerContext(context.Background())
	defer owner.Complete()
	graph = &treeGraphFake{walk: walk}
	telemetry := &recordingTelemetry{}
	engine := mustReuseTestEngine(t, EngineDependencies{ReuseGate: tupleReuseGate{stored}, Telemetry: telemetry,
		CandidateVerifier: func(_ context.Context, _ storage.Principal, _ RequestedScope, _ ResolvedGraphBinding, kind SubjectKind, _ string) (bool, CandidateVerificationReason) {
			verifiedKinds = append(verifiedKinds, kind)
			return verified, CandidateVerificationValid
		},
		WorkItemMembership: tupleMembershipFunc(func(context.Context, storage.Principal, WorkItemMembershipRequest) (*WorkItemMembershipLease, WorkItemMembershipResult, error) {
			t.Error("the project read re-measured a repository tuple")
			return nil, WorkItemMembershipResult{}, errors.New("project read")
		}),
		TreeWorkItemGraph: graph, TreeWorkItemFilter: &treeFilterFake{}, TreeWorkItemGate: gate,
	})
	_, hit, _, reuseErr := engine.tryReuse(ctx, principal, request, TimeContext{Axis: TemporalCurrent}, "", windowKeyRederivable, ResolvedGraphBinding{Epoch: 12})
	if reuseErr != nil {
		t.Fatal(reuseErr)
	}
	return hit, verifiedKinds, graph, telemetry.repositoryWorkItemWalks
}

func TestAStoredRepositoryTupleIsReusedOnlyAfterTheRepositoryIsReCheckedAndReWalked(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	principal, request, stored, walk := repositoryTreeReuseFixture(t, TreeLinkTierNative)
	if got := ClassifyWorkItemTuple(stored.Result, stored.SemanticState, stored.SemanticStateRead); got.Disposition != WorkItemTupleEligible {
		t.Fatalf("a stored repository tuple classified %s, want eligible", got.Disposition)
	}
	if err := ValidateWorkItemTuplePayload(stored.Result, principal); err != nil {
		t.Fatalf("a stored repository tuple payload is invalid: %v", err)
	}
	hit, kinds, graph, walks := reuseOnTheRepositoryWalk(t, principal, request, stored, walk, true)
	if !hit {
		t.Fatal("a stored repository tuple whose walk is unchanged was not reused")
	}
	if len(kinds) != 1 || kinds[0] != SubjectRepository {
		t.Fatalf("the stored anchor was re-checked as %v, want the repository", kinds)
	}
	if graph.calls != 1 || graph.anchor != repositoryWorkItemAnchor || graph.binding.Epoch != 12 {
		t.Fatalf("re-walks=%d anchor=%+v epoch=%d, want one walk of the stored repository under the request's binding", graph.calls, graph.anchor, graph.binding.Epoch)
	}
	if len(walks) != 1 || walks[0].Outcome != RepositoryWorkItemWalkMembers || !walks[0].Measured {
		t.Fatalf("a re-walk owes its decision line: %+v", walks)
	}

	// A repository the caller may no longer read is not reused, and not re-walked.
	hit, _, graph, _ = reuseOnTheRepositoryWalk(t, principal, request, stored, walk, false)
	if hit || graph.calls != 0 {
		t.Fatalf("an unverified repository: hit=%t walks=%d, want a miss with no walk", hit, graph.calls)
	}

	// The walk finds one more member: the stored answer is stale.
	more := walk
	more.Members = append(slices.Clone(walk.Members), treeMember(t, "ENG-99", TreeLinkTierNative))
	hit, _, _, _ = reuseOnTheRepositoryWalk(t, principal, request, stored, more, true)
	if hit {
		t.Fatal("a stored repository tuple was reused after the walk found another member")
	}

	// The same member now linked at another tier: the stored reason, and any
	// heuristic count beside it, would be stale.
	retiered := walk
	retiered.Members = []TreeWorkItemMember{{Subject: walk.Members[0].Subject, Tier: TreeLinkTierHeuristic}}
	hit, _, _, _ = reuseOnTheRepositoryWalk(t, principal, request, stored, retiered, true)
	if hit {
		t.Fatal("a stored repository tuple was reused after its member's link changed tier")
	}
}

func TestATupleWhoseCandidateAndAnchorDifferInKindIsNotATuple(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	_, _, stored, _ := repositoryTreeReuseFixture(t, TreeLinkTierNative)
	stored.Result.SubjectResolution.Candidates[0].Subject = SubjectRef{Kind: SubjectProject, CanonicalID: repositoryWorkItemAnchor.CanonicalID, Label: repositoryWorkItemAnchor.Label}
	if err := ValidateWorkItemTuplePayload(stored.Result, storage.Principal{OrgID: "org-1"}); err == nil {
		t.Fatal("a payload whose candidate is a project and whose anchor is a repository validated")
	}
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:platform", Label: "Platform"}
	stored.Result.SubjectResolution.Candidates[0].Subject = team
	stored.Result.SubjectResolution.Committed = []SubjectRef{team}
	if err := ValidateWorkItemTuplePayload(stored.Result, storage.Principal{OrgID: "org-1"}); err == nil {
		t.Fatal("a payload anchored on a team validated")
	}
	state := *stored.SemanticState
	state.ScopeAnchor = SemanticScopeAnchor{Kind: SubjectTeam, Term: "Platform"}
	if workItemTupleSemanticState(&state) {
		t.Fatal("a reading anchored on a team classified as a work-item tuple")
	}
}
