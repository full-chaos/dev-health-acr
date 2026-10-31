package contextfabric

import (
	"context"
	"slices"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12242: an answer whose graph read could not verify the requested
// project narrowing (CHAOS-12231's degrading read_population_unverified
// detail, raw project_narrowing_unverified) is not served complete: one floor
// in ApplyServedStatusFloors lowers complete to partial. It never promotes,
// and it matches the detail's code AND raw, so the read-population outcome
// rows' own use of the code is not affected.

// narrowingAnswer runs the acceptance engine on a project-narrowed request
// whose graph coverage carries detail (none when nil): the model draft says
// complete.
func narrowingAnswer(t *testing.T, detail *CoverageDetail) InvestigationResult {
	t.Helper()
	project := acceptanceProject()
	graphContext := bootstrapGraphContext(project)
	if detail != nil {
		graphContext.Coverage = Coverage{
			Sources:         []SourceObservation{},
			Partial:         detail.Degrading,
			DegradedReasons: []string{},
			Details:         []CoverageDetail{*detail},
		}
		if detail.Degrading {
			graphContext.Coverage.DegradedReasons = []string{detail.Raw}
		}
	}
	graph := &acceptanceGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		context:    graphContext,
	}
	facts := factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
		return bootstrapFactBundle(project), nil
	})
	engine := buildAcceptanceEngineWithTelemetry(t, graph, facts, bootstrapInterpretation(), bootstrapDraft(project), nil, &recordingTelemetry{})
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestedScope.ProjectIDs = []string{"proj-1"}
	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("served result does not validate: %v", err)
	}
	return result
}

func withCount(n int) func(*CoverageDetail) {
	return func(d *CoverageDetail) { d.Count = &n }
}

func unverifiedNarrowingDetail() CoverageDetail {
	return detailFixture(contractsv1.ContextFabricCoverageDetailReadPopulationUnverified, "context-fabric:graph", true, ProjectNarrowingUnverifiedRaw, nil)
}

// Red on main: the draft's complete was served beside coverage.partial.
func TestAnUnverifiedProjectNarrowingFloorsACompleteAnswerToPartial(t *testing.T) {
	t.Parallel()
	if control := narrowingAnswer(t, nil); control.Status != InvestigationComplete {
		t.Fatalf("fixture: with no detail the draft serves %q, want complete", control.Status)
	}
	detail := unverifiedNarrowingDetail()
	result := narrowingAnswer(t, &detail)
	if result.Status != InvestigationPartial {
		t.Fatalf("status = %q over an unverified project narrowing, want partial", result.Status)
	}
	if !result.Coverage.Partial {
		t.Fatal("coverage.partial = false")
	}
	if result.Completeness.State == contractsv1.ContextFabricAnswerCompletenessComplete {
		t.Fatalf("completeness.state = %q beside a partial status", result.Completeness.State)
	}
	// The floor uses the swap every no_match floor uses: the draft's prose,
	// written for a complete answer, is recomposed and its withholding said.
	if !slices.Contains(result.Limitations, synthesisNarrativeWithheldLimitation) {
		t.Fatalf("limitations = %v, want the narrative-withheld limitation of a floored status", result.Limitations)
	}
}

// The floor is this detail's only: another degrading graph detail with no
// floor of its own keeps the draft's status (unchanged by this change).
func TestAnotherGraphDegradationKeepsItsDraftStatus(t *testing.T) {
	t.Parallel()
	detail := detailFixture(contractsv1.ContextFabricCoverageDetailGraphEndpointLookupFailed, "context-fabric:graph", true, "endpoint_lookup_failed:1", withCount(1))
	if result := narrowingAnswer(t, &detail); result.Status != InvestigationComplete {
		t.Fatalf("status = %q over endpoint_lookup_failed, want the draft's complete: this floor must not widen to other details", result.Status)
	}
}

// Each clause of the match and of the status rule, on the one apply function
// every serving point runs.
func TestTheUnverifiedNarrowingFloorMatchesCodeRawAndDegradingAndNeverPromotes(t *testing.T) {
	t.Parallel()
	served := narrowingAnswer(t, nil)
	withDetail := func(detail CoverageDetail) InvestigationResult {
		stored := served
		stored.Coverage.Details = append([]CoverageDetail{}, detail)
		if detail.Degrading {
			stored.Coverage.DegradedReasons = []string{detail.Raw}
			stored.Coverage.Partial = true
		}
		stored.Completeness = ComputeAnswerCompleteness(stored)
		return stored
	}
	cases := map[string]struct {
		detail CoverageDetail
		status InvestigationStatus
		refuse bool
		want   InvestigationStatus
	}{
		"the detail lowers complete":   {unverifiedNarrowingDetail(), InvestigationComplete, false, InvestigationPartial},
		"another code with the raw":    {detailFixture(contractsv1.ContextFabricCoverageDetailGraphEndpointLookupFailed, "context-fabric:graph", true, ProjectNarrowingUnverifiedRaw, withCount(1)), InvestigationComplete, false, InvestigationComplete},
		"the code with another raw":    {detailFixture(contractsv1.ContextFabricCoverageDetailReadPopulationUnverified, "context-fabric:graph", true, "read_population_unverified", nil), InvestigationComplete, false, InvestigationComplete},
		"not degrading":                {detailFixture(contractsv1.ContextFabricCoverageDetailReadPopulationUnverified, "context-fabric:graph", false, ProjectNarrowingUnverifiedRaw, nil), InvestigationComplete, false, InvestigationComplete},
		"partial stays partial":        {unverifiedNarrowingDetail(), InvestigationPartial, false, InvestigationPartial},
		"degraded stays degraded":      {unverifiedNarrowingDetail(), InvestigationDegraded, false, InvestigationDegraded},
		"no_match stays no_match":      {unverifiedNarrowingDetail(), InvestigationNoMatch, false, InvestigationNoMatch},
		"a refusal keeps its status":   {unverifiedNarrowingDetail(), InvestigationComplete, true, InvestigationComplete},
		"clarification stays as it is": {unverifiedNarrowingDetail(), InvestigationClarificationRequired, false, InvestigationClarificationRequired},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			stored := withDetail(tc.detail)
			stored.Status = tc.status
			if tc.refuse {
				stored.RefusalBasis = "planted refusal basis"
			}
			got := ApplyServedStatusFloors(stored)
			if got.Status != tc.want {
				t.Fatalf("served status = %q, want %q", got.Status, tc.want)
			}
			if tc.want == InvestigationPartial && tc.status == InvestigationComplete {
				if got.Completeness.State == contractsv1.ContextFabricAnswerCompletenessComplete {
					t.Fatalf("completeness.state = %q: it was not recomputed after the floor", got.Completeness.State)
				}
				if again := ApplyServedStatusFloors(got); again.Status != InvestigationPartial || len(again.Limitations) != len(got.Limitations) {
					t.Fatalf("a second apply moved the answer again: %q, %d limitations (was %d)", again.Status, len(again.Limitations), len(got.Limitations))
				}
			}
		})
	}
}

// Replay: a result stored complete before this floor existed is served
// partial by id (ApplyServedStatusFloors is the by-id route's apply step) and
// by reuse (the engine's late writer); the stored document is not rewritten.
func TestAStoredCompleteAnswerOverAnUnverifiedNarrowingIsServedPartial(t *testing.T) {
	t.Parallel()
	stored := narrowingAnswer(t, nil)
	stored.Coverage.Details = []CoverageDetail{unverifiedNarrowingDetail()}
	stored.Coverage.DegradedReasons = []string{ProjectNarrowingUnverifiedRaw}
	stored.Coverage.Partial = true
	stored.Completeness = ComputeAnswerCompleteness(stored)
	if stored.Status != InvestigationComplete {
		t.Fatalf("fixture: stored status = %q, want the pre-floor complete", stored.Status)
	}

	byID := ApplyServedStatusFloors(stored)
	if byID.Status != InvestigationPartial {
		t.Fatalf("served by id = %q, want partial", byID.Status)
	}
	if stored.Status != InvestigationComplete {
		t.Fatal("the floor rewrote the stored document")
	}

	// Reuse: the cohort status fixture the reuse tests use (it reaches the
	// reuse path), stored complete with the detail added.
	cohortStored := runAuthorizationCutCohortQuestion(t, 4, Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, []InvestigationGoal{GoalAssessState})
	if cohortStored.Status != InvestigationComplete {
		t.Fatalf("fixture: the cohort answer is %q, want complete", cohortStored.Status)
	}
	cohortStored.Coverage.Details = append(cohortStored.Coverage.Details, unverifiedNarrowingDetail())
	cohortStored.Coverage.DegradedReasons = append(cohortStored.Coverage.DegradedReasons, ProjectNarrowingUnverifiedRaw)
	cohortStored.Coverage.Partial = true
	cohortStored.Completeness = ComputeAnswerCompleteness(cohortStored)
	committed := []SubjectRef{{Kind: SubjectOrganization, CanonicalID: "org_1", Label: "Org"}}
	for _, member := range cohortStored.Cohort.Members {
		committed = append(committed, member.Subject)
	}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph:     graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: committed}},
		Results:   &resultStoreStub{},
		ReuseGate: readingReuseGate{stored: cohortStored, frame: frameWithPointer([]InvestigationGoal{GoalAssessState}, scopedExpression(SubjectTeam))},
	})
	reused, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if !reused.Reused {
		t.Fatal("the fixture did not take the reuse path, so it proves nothing about it")
	}
	if reused.Status != InvestigationPartial {
		t.Fatalf("served by reuse = %q, want partial", reused.Status)
	}
}
