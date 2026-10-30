package contextfabric

import (
	"context"
	"reflect"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12147: a count over a cohort that authorization cut must not derive
// completeness `complete`.
//
// THE LIVE CASE. kiac askdev-1 (acr eabe6e6c, org 70d529e0), "How many
// repositories does the organization have?" with an assertion scoped to 4
// of the org's 34 repositories (result_e0b82dee27bfcdfa79b27a5ff12deebc).
// The graph read filed the degrading graph_cohort_denied_by_authorization
// detail (count 30), the cohort itself said complete=true, and the count row
// read `satisfied` 4/4, so completeness.state was `complete` while
// coverage.partial was true and the status read `degraded`. The design says a
// cut member set is never read as the whole one
// (docs/design/context-fabric-architecture-diagrams.md, "A cohort that
// authorization cut is partial"); only the served STATUS honoured it.

const cohortAuthorizationCutDenied = 30

func cohortAuthorizationCutCoverage(denied int, degrading bool) Coverage {
	count := denied
	detail := CoverageDetail{
		DetailID:  "cov-graph-01",
		Source:    "context-fabric:graph",
		Code:      contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization,
		Degrading: degrading,
		Count:     &count,
		Raw:       "cohort_denied_by_authorization:30",
	}
	detail.Label = contractsv1.ComposeCoverageDetailLabel(detail)
	return Coverage{
		Sources:         []SourceObservation{},
		DegradedReasons: []string{"cohort_denied_by_authorization:30"},
		Partial:         degrading,
		Details:         []CoverageDetail{detail},
	}
}

func runAuthorizationCutCount(t *testing.T, cohortSize int, coverage Coverage) InvestigationResult {
	t.Helper()
	frame := countingFrame(SubjectTeam)
	cohort := countingCohort(SubjectTeam, cohortSize)
	engine := newCountingEngineWithGraphCoverage(t, cohort, 0, coverage, frame, &recordingTelemetry{})
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_50210001"
	request.Question = "count question"
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("served result does not validate: %v", err)
	}
	return result
}

func assembledCountRow(t *testing.T, result InvestigationResult) RequirementOutcomeRow {
	t.Helper()
	rows := countOutcomeRows(result, contractsv1.ContextFabricOutcomeStageAssembledResult)
	if len(rows) != 1 {
		t.Fatalf("assembled-result `count` rows = %d, want 1 -- the fixture never reached the count step, so it proves nothing", len(rows))
	}
	return rows[0]
}

func TestACountOverAnAuthorizationCutCohortIsNotComplete(t *testing.T) {
	t.Parallel()
	result := runAuthorizationCutCount(t, 4, cohortAuthorizationCutCoverage(cohortAuthorizationCutDenied, true))
	row := assembledCountRow(t, result)

	if got := result.Completeness.State; got == contractsv1.ContextFabricAnswerCompletenessComplete {
		t.Fatalf("completeness.state = %q over a member set authorization cut by %d -- a cut member set read as the whole one",
			got, cohortAuthorizationCutDenied)
	}
	if got := result.Completeness.State; got != contractsv1.ContextFabricAnswerCompletenessPartial {
		t.Fatalf("completeness.state = %q, want %q", got, contractsv1.ContextFabricAnswerCompletenessPartial)
	}
	if row.Outcome != contractsv1.ContextFabricRequirementNarrowed {
		t.Fatalf("count row outcome = %q, want %q", row.Outcome, contractsv1.ContextFabricRequirementNarrowed)
	}
	if row.Impact != contractsv1.ContextFabricAnswerImpactScope {
		t.Fatalf("count row impact = %q, want %q -- the reader is shown fewer of the counted things", row.Impact, contractsv1.ContextFabricAnswerImpactScope)
	}
	if row.CauseCoverage != contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization {
		t.Fatalf("count row cause_coverage = %q, want %q -- the row must name the mechanism the coverage detail names",
			row.CauseCoverage, contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization)
	}
	if !row.CauseObserved {
		t.Fatal("the graph read REPORTED the denial with a count, so the cause is observed, not defaulted")
	}
	if row.Served != 4 || row.Declared != 4+cohortAuthorizationCutDenied {
		t.Fatalf("count row served/declared = %d/%d, want %d/%d -- the served members plus the members the graph counted as denied",
			row.Served, row.Declared, 4, 4+cohortAuthorizationCutDenied)
	}
	if len(row.Refinements) != 1 || row.Refinements[0].Coverage != contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization ||
		row.Refinements[0].Before != 4+cohortAuthorizationCutDenied || row.Refinements[0].After != 4 {
		t.Fatalf("count row refinements = %+v, want one step %d->4 naming the authorization cut", row.Refinements, 4+cohortAuthorizationCutDenied)
	}
}

// The control: the same fixture with no denial keeps its `satisfied` row and
// its `complete` state. Without it the test above could pass because the
// fixture is partial for some other reason.
func TestACountOverAnUncutCohortStaysComplete(t *testing.T) {
	t.Parallel()
	result := runAuthorizationCutCount(t, 4, Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}})
	row := assembledCountRow(t, result)
	if row.Outcome != contractsv1.ContextFabricRequirementSatisfied || row.Served != 4 || row.Declared != 4 {
		t.Fatalf("uncut count row = %q %d/%d, want satisfied 4/4", row.Outcome, row.Served, row.Declared)
	}
	if got := result.Completeness.State; got != contractsv1.ContextFabricAnswerCompletenessComplete {
		t.Fatalf("uncut completeness.state = %q, want %q", got, contractsv1.ContextFabricAnswerCompletenessComplete)
	}
}

// A denial detail that does not degrade, or counts nobody, measured no cut:
// the row has no reduction to state, and inventing one would publish a loss
// nothing observed.
func TestAnAuthorizationDetailThatMeasuredNoCutLeavesTheCountAlone(t *testing.T) {
	t.Parallel()
	for name, coverage := range map[string]Coverage{
		"not degrading": cohortAuthorizationCutCoverage(cohortAuthorizationCutDenied, false),
		"zero denied":   cohortAuthorizationCutCoverage(0, true),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			row := assembledCountRow(t, runAuthorizationCutCount(t, 4, coverage))
			if row.Outcome != contractsv1.ContextFabricRequirementSatisfied || row.Declared != 4 {
				t.Fatalf("count row = %q %d/%d, want satisfied 4/4", row.Outcome, row.Served, row.Declared)
			}
		})
	}
}

func TestCohortAuthorizationDeniedReadsTheLargestDegradingCount(t *testing.T) {
	t.Parallel()
	five, nine, eleven := 5, 9, 11
	coverage := Coverage{Details: []CoverageDetail{
		{Code: contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization, Degrading: true, Count: &five},
		{Code: contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization, Degrading: true, Count: &nine},
		// Not degrading: a disclosure, not a cut.
		{Code: contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization, Degrading: false, Count: &eleven},
		// Another code with a larger count: not this mechanism.
		{Code: contractsv1.ContextFabricCoverageDetailKindCensusTruncated, Degrading: true, Count: &eleven},
		// No count at all: nothing measured.
		{Code: contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization, Degrading: true},
	}}
	if got := cohortAuthorizationDenied(coverage); got != nine {
		t.Fatalf("cohortAuthorizationDenied = %d, want %d", got, nine)
	}
	if got := cohortAuthorizationDenied(Coverage{}); got != 0 {
		t.Fatalf("cohortAuthorizationDenied(empty) = %d, want 0", got)
	}
}

// A stored answer that carries the denial detail but no count row (written
// before the count step existed) is backfilled on reuse. The backfilled row
// must state the same cut a fresh one does: a cut member set read as the whole
// one is no better for arriving from cache.
func TestAReusedAuthorizationCutCountIsBackfilledAsNarrowed(t *testing.T) {
	t.Parallel()
	project, candidate := reusableCandidate()
	candidate.Cohort = countingCohort(SubjectTeam, 3)
	candidate.SubjectResolution.Candidates = []SubjectCandidate{scopeAnchorMatch(project)}
	cut := cohortAuthorizationCutCoverage(cohortAuthorizationCutDenied, true)
	candidate.Coverage.Details = append(candidate.Coverage.Details, cut.Details...)
	candidate.Coverage.DegradedReasons = append(candidate.Coverage.DegradedReasons, cut.DegradedReasons...)
	candidate.Coverage.Partial = true
	candidate.Completeness.Outcomes = []RequirementOutcomeRow{{
		Stage:       contractsv1.ContextFabricOutcomeStagePlanning,
		Requirement: string(ObligationCount) + "/" + string(SubjectRoleMember) + "/" + string(SubjectTeam),
		Obligation:  string(ObligationCount),
		Outcome:     contractsv1.ContextFabricRequirementSatisfied,
		Impact:      contractsv1.ContextFabricAnswerImpactNone,
	}}
	candidate.Completeness = ComputeAnswerCompleteness(candidate)

	committed := []SubjectRef{project}
	for _, member := range candidate.Cohort.Members {
		committed = append(committed, member.Subject)
	}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph:     graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: committed}},
		Results:   &resultStoreStub{},
		ReuseGate: readingReuseGate{stored: candidate, frame: countingFrame(SubjectTeam)},
	})
	served, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if !served.Reused {
		t.Fatal("the fixture did not take the reuse path, so it proves nothing about it")
	}
	row := assembledCountRow(t, served)
	if row.Outcome != contractsv1.ContextFabricRequirementNarrowed ||
		row.CauseCoverage != contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization ||
		row.Served != 3 || row.Declared != 3+cohortAuthorizationCutDenied {
		t.Fatalf("backfilled row = %q cause %q %d/%d, want narrowed cause %q 3/%d",
			row.Outcome, row.CauseCoverage, row.Served, row.Declared,
			contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization, 3+cohortAuthorizationCutDenied)
	}
	if got := served.Completeness.State; got == contractsv1.ContextFabricAnswerCompletenessComplete {
		t.Fatalf("reused completeness.state = %q over an authorization cut member set", got)
	}
	if err := contractsv1.ValidateContextFabricPlanRequirementOutcomeRow(row); err != nil {
		t.Fatalf("the backfilled row does not validate: %v", err)
	}
}

// A count that a narrowing step ALSO cut names both mechanisms: the
// narrowing basis it carried, and the authorization cut on top.
func TestAnAuthorizationCutOnANarrowedCountNamesBothMechanisms(t *testing.T) {
	t.Parallel()
	cardinality := MembershipCardinality{
		Resolved: true, Kind: SubjectTeam, Served: 3, Declared: 8,
		Basis:               contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical,
		AuthorizationDenied: cohortAuthorizationCutDenied,
	}
	row := membershipCardinalityOutcomeRow(cardinality, "count/member/team", string(ObligationCount))
	if row.Outcome != contractsv1.ContextFabricRequirementNarrowed || row.Served != 3 || row.Declared != 8+cohortAuthorizationCutDenied {
		t.Fatalf("row = %q %d/%d, want narrowed 3/%d", row.Outcome, row.Served, row.Declared, 8+cohortAuthorizationCutDenied)
	}
	if row.CauseCoverage != contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization {
		t.Fatalf("cause_coverage = %q, want the authorization cut", row.CauseCoverage)
	}
	if row.CauseNarrowing != contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical {
		t.Fatalf("cause_narrowing = %q, want the narrowing step's basis carried", row.CauseNarrowing)
	}
	if err := contractsv1.ValidateContextFabricPlanRequirementOutcomeRow(row); err != nil {
		t.Fatalf("row does not validate: %v", err)
	}
}

// The guards on withCohortAuthorizationCut: no resolved count, or a
// work-item count (whose census has its own authorization gap), is left as
// it is.
func TestWithCohortAuthorizationCutLeavesWhatItDoesNotQualify(t *testing.T) {
	t.Parallel()
	coverage := cohortAuthorizationCutCoverage(cohortAuthorizationCutDenied, true)
	for name, cardinality := range map[string]MembershipCardinality{
		"unresolved": {Kind: SubjectTeam},
		"work item":  {Resolved: true, Kind: SubjectWorkItem, Served: 4, Declared: 4},
	} {
		if got := withCohortAuthorizationCut(cardinality, coverage); got.AuthorizationDenied != 0 {
			t.Fatalf("%s: AuthorizationDenied = %d, want 0", name, got.AuthorizationDenied)
		}
	}
	if got := withCohortAuthorizationCut(MembershipCardinality{Resolved: true, Kind: SubjectRepository, Served: 4, Declared: 4}, coverage); got.AuthorizationDenied != cohortAuthorizationCutDenied {
		t.Fatalf("repository count: AuthorizationDenied = %d, want %d", got.AuthorizationDenied, cohortAuthorizationCutDenied)
	}
}

// A stored answer written BEFORE the cut was stated carries a `satisfied`
// assembled-result count row beside the denial detail. Reuse never re-runs
// the count step for a row that exists, so the served copy must restate the
// cut by APPENDING the narrowed row (the stored row is never rewritten), or
// the reused answer keeps reading `complete`.
func TestAReusedStoredSatisfiedCountOverACutCohortIsRestated(t *testing.T) {
	t.Parallel()
	project, candidate := reusableCandidate()
	candidate.Cohort = countingCohort(SubjectTeam, 3)
	candidate.SubjectResolution.Candidates = []SubjectCandidate{scopeAnchorMatch(project)}
	cut := cohortAuthorizationCutCoverage(cohortAuthorizationCutDenied, true)
	candidate.Coverage.Details = append(candidate.Coverage.Details, cut.Details...)
	candidate.Coverage.DegradedReasons = append(candidate.Coverage.DegradedReasons, cut.DegradedReasons...)
	candidate.Coverage.Partial = true
	identity := string(ObligationCount) + "/" + string(SubjectRoleMember) + "/" + string(SubjectTeam)
	storedRow := RequirementOutcomeRow{
		Stage: contractsv1.ContextFabricOutcomeStageAssembledResult, Requirement: identity, Obligation: string(ObligationCount),
		Outcome: contractsv1.ContextFabricRequirementSatisfied, Impact: contractsv1.ContextFabricAnswerImpactNone,
		Served: 3, Declared: 3,
	}
	candidate.Completeness.Outcomes = []RequirementOutcomeRow{{
		Stage: contractsv1.ContextFabricOutcomeStagePlanning, Requirement: identity, Obligation: string(ObligationCount),
		Outcome: contractsv1.ContextFabricRequirementSatisfied, Impact: contractsv1.ContextFabricAnswerImpactNone,
	}, storedRow}
	candidate.Completeness = ComputeAnswerCompleteness(candidate)
	if candidate.Completeness.State != contractsv1.ContextFabricAnswerCompletenessComplete {
		t.Fatalf("fixture: stored state = %q, want the pre-fix `complete`", candidate.Completeness.State)
	}

	committed := []SubjectRef{project}
	for _, member := range candidate.Cohort.Members {
		committed = append(committed, member.Subject)
	}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph:     graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: committed}},
		Results:   &resultStoreStub{},
		ReuseGate: readingReuseGate{stored: candidate, frame: countingFrame(SubjectTeam)},
	})
	served, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if !served.Reused {
		t.Fatal("the fixture did not take the reuse path, so it proves nothing about it")
	}
	if got := served.Completeness.State; got == contractsv1.ContextFabricAnswerCompletenessComplete {
		t.Fatalf("reused completeness.state = %q over an authorization cut member set", got)
	}
	rows := countOutcomeRows(served, contractsv1.ContextFabricOutcomeStageAssembledResult)
	if len(rows) != 2 || !reflect.DeepEqual(rows[0], storedRow) {
		t.Fatalf("assembled count rows = %+v, want the stored row kept as it was, then the restated cut", rows)
	}
	restated := rows[1]
	if restated.Outcome != contractsv1.ContextFabricRequirementNarrowed ||
		restated.CauseCoverage != contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization ||
		restated.Served != 3 || restated.Declared != 3+cohortAuthorizationCutDenied {
		t.Fatalf("restated row = %q cause %q %d/%d, want narrowed cause %q 3/%d", restated.Outcome, restated.CauseCoverage,
			restated.Served, restated.Declared, contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization, 3+cohortAuthorizationCutDenied)
	}
	if err := served.Validate(); err != nil {
		t.Fatalf("served result does not validate: %v", err)
	}
}

// The restatement directly, over every shape it must leave alone, plus its
// idempotence: a second pass appends nothing.
func TestRestateCohortAuthorizationCutCountTouchesOnlyAStaleSatisfiedCount(t *testing.T) {
	t.Parallel()
	identity := "count/member/team"
	satisfied := RequirementOutcomeRow{
		Stage: contractsv1.ContextFabricOutcomeStageAssembledResult, Requirement: identity, Obligation: string(ObligationCount),
		Outcome: contractsv1.ContextFabricRequirementSatisfied, Impact: contractsv1.ContextFabricAnswerImpactNone, Served: 4, Declared: 4,
	}
	base := func() InvestigationResult {
		return InvestigationResult{
			Cohort:       countingCohort(SubjectTeam, 4),
			Coverage:     cohortAuthorizationCutCoverage(cohortAuthorizationCutDenied, true),
			Completeness: AnswerCompleteness{Outcomes: []RequirementOutcomeRow{satisfied}},
		}
	}
	restated, changed := restateCohortAuthorizationCutCount(base())
	if !changed || len(restated.Completeness.Outcomes) != 2 || !reflect.DeepEqual(restated.Completeness.Outcomes[0], satisfied) {
		t.Fatalf("restate = changed %v rows %+v, want the stored row then one appended row", changed, restated.Completeness.Outcomes)
	}
	if again, changedAgain := restateCohortAuthorizationCutCount(restated); changedAgain || len(again.Completeness.Outcomes) != 2 {
		t.Fatalf("second restate appended again: changed %v rows %d", changedAgain, len(again.Completeness.Outcomes))
	}
	narrowedByBudget := satisfied
	narrowedByBudget.Outcome = contractsv1.ContextFabricRequirementNarrowed
	narrowedByBudget.Impact = contractsv1.ContextFabricAnswerImpactScope
	narrowedByBudget.CauseNarrowing = contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical
	narrowedByBudget.CauseObserved = true
	narrowedByBudget.Declared = 8
	readRow := satisfied
	readRow.Obligation, readRow.Requirement = "state", "state/member/team"
	for name, mutate := range map[string]func(*InvestigationResult){
		"no cohort":        func(r *InvestigationResult) { r.Cohort = nil },
		"work item cohort": func(r *InvestigationResult) { r.Cohort.Kind = SubjectWorkItem },
		"no denial":        func(r *InvestigationResult) { r.Coverage = Coverage{} },
		"denial not degrading": func(r *InvestigationResult) {
			r.Coverage = cohortAuthorizationCutCoverage(cohortAuthorizationCutDenied, false)
		},
		"no count row":           func(r *InvestigationResult) { r.Completeness.Outcomes = []RequirementOutcomeRow{readRow} },
		"count already narrowed": func(r *InvestigationResult) { r.Completeness.Outcomes = []RequirementOutcomeRow{narrowedByBudget} },
		"work item count requirement": func(r *InvestigationResult) {
			r.AnswerPlan = &AnswerPlan{Requirements: []contractsv1.ContextFabricPlanRequirement{{Requirement: identity, Obligation: string(ObligationCount), Subject: SubjectWorkItem}}}
		},
		"planning row only": func(r *InvestigationResult) {
			planning := satisfied
			planning.Stage, planning.Served, planning.Declared = contractsv1.ContextFabricOutcomeStagePlanning, 0, 0
			r.Completeness.Outcomes = []RequirementOutcomeRow{planning}
		},
	} {
		result := base()
		mutate(&result)
		before := len(result.Completeness.Outcomes)
		if got, changed := restateCohortAuthorizationCutCount(result); changed || len(got.Completeness.Outcomes) != before {
			t.Fatalf("%s: restate changed %v rows %d -> %d, want untouched", name, changed, before, len(got.Completeness.Outcomes))
		}
	}
}

// The operator line describes the decision the served document ends on: with
// a stored row and a restated one, that is the restated row.
func TestTheCardinalityEventReadsTheRestatedCountRow(t *testing.T) {
	t.Parallel()
	identity := "count/member/team"
	stored := RequirementOutcomeRow{
		Stage: contractsv1.ContextFabricOutcomeStageAssembledResult, Requirement: identity, Obligation: string(ObligationCount),
		Outcome: contractsv1.ContextFabricRequirementSatisfied, Impact: contractsv1.ContextFabricAnswerImpactNone, Served: 4, Declared: 4,
	}
	result, changed := restateCohortAuthorizationCutCount(InvestigationResult{
		Cohort:       countingCohort(SubjectTeam, 4),
		Coverage:     cohortAuthorizationCutCoverage(cohortAuthorizationCutDenied, true),
		Completeness: AnswerCompleteness{Outcomes: []RequirementOutcomeRow{stored}},
	})
	if !changed {
		t.Fatal("fixture: nothing was restated")
	}
	event, ok := membershipCardinalityEventFrom(result, QuestionFamilyScopedCohortStatus)
	if !ok || event.Outcome != contractsv1.ContextFabricRequirementNarrowed ||
		event.Cause != contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization || event.Declared != 4+cohortAuthorizationCutDenied {
		t.Fatalf("event = %+v (ok %v), want the restated narrowed row", event, ok)
	}
}
