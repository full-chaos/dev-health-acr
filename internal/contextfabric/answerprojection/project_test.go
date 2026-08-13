package answerprojection

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func subject(kind contractsv1.ContextFabricSubjectKind, id, label string) contractsv1.ContextFabricSubjectRef {
	return contractsv1.ContextFabricSubjectRef{Kind: kind, CanonicalID: id, Label: label}
}

func stringValue(value string) contractsv1.ContextFabricScalarValue {
	return contractsv1.ContextFabricScalarValue{String: &value}
}

// richResult is a canonical result with every shape the projection has to
// narrow: drivers across all standings (including a withheld one), claimed
// facts, a cohort, candidates in several resolution states, and coverage.
func richResult() contractsv1.ContextFabricInvestigationResult {
	project := subject(contractsv1.ContextFabricSubjectProject, "project_ask_dev", "Ask Dev")
	teamA := subject(contractsv1.ContextFabricSubjectTeam, "team_a", "Team A")
	teamB := subject(contractsv1.ContextFabricSubjectTeam, "team_b", "Team B")

	return contractsv1.ContextFabricInvestigationResult{
		SchemaVersion: contractsv1.ContextFabricInvestigationResultSchema,
		ResultID:      "result_12345678",
		RequestID:     "request_12345678",
		GeneratedAt:   time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC),
		Status:        contractsv1.ContextFabricInvestigationComplete,
		Question:      "What is the actual status of Ask Dev and what is driving it?",
		Interpretation: contractsv1.ContextFabricInterpretedQuestion{
			Shape:             contractsv1.ContextFabricShapeOpen,
			RequestedJudgment: "status_and_drivers",
			TimeContext:       contractsv1.ContextFabricTimeContext{Axis: contractsv1.ContextFabricTemporalCurrent},
			FactRequirements:  []contractsv1.ContextFabricFactRequirement{{Kind: contractsv1.ContextFabricFactStatus}},
		},
		SubjectResolution: contractsv1.ContextFabricSubjectResolution{
			Candidates: []contractsv1.ContextFabricSubjectCandidate{
				{ReceiptID: "receipt_committed_1", Subject: project, State: contractsv1.ContextFabricResolutionCommitted, MatchReasons: []string{"exact name"}, Confidence: 0.99, EvidenceRefIDs: []string{}},
				{ReceiptID: "receipt_proposed_1", Subject: teamA, State: contractsv1.ContextFabricResolutionProposed, MatchReasons: []string{"owns the project"}, Confidence: 0.7, EvidenceRefIDs: []string{}},
				{ReceiptID: "receipt_ambiguous", Subject: teamB, State: contractsv1.ContextFabricResolutionAmbiguous, MatchReasons: []string{"similar name"}, Confidence: 0.3, EvidenceRefIDs: []string{}},
			},
			Committed: []contractsv1.ContextFabricSubjectRef{project},
		},
		DirectJudgment:     "Ask Dev is not release-ready.",
		CurrentState:       "Required work remains open.",
		StrongestPressures: []string{"open blockers", "failing checks"},
		Drivers: []contractsv1.ContextFabricDriverJudgment{
			// Deliberately NOT in standing order, so the ordering test
			// proves the projection sorts by standing rather than
			// echoing the canonical array order.
			driver("driver_context_01", contractsv1.ContextFabricDriverContext, "narrative", "Context", nil, project),
			driver("driver_principal_1", contractsv1.ContextFabricDriverPrincipal, "blockers", "Blockers remain", []string{"claim_blockers_1"}, project),
			driver("driver_symptom_001", contractsv1.ContextFabricDriverSymptom, "reviews", "Reviews stalled", []string{"claim_reviews_01"}, project),
			driver("driver_principal_2", contractsv1.ContextFabricDriverPrincipal, "status", "Status is amber", []string{"claim_status_001"}, project),
			withheldDriver("driver_withheld_01", project),
			driver("driver_contrib_001", contractsv1.ContextFabricDriverContributing, "work", "Work outstanding", []string{"claim_work_00001"}, project),
		},
		RemainingWork: []contractsv1.ContextFabricFinding{},
		ReadinessGaps: []contractsv1.ContextFabricFinding{},
		Paths:         []contractsv1.ContextFabricRelationshipPath{},
		Conflicts:     []contractsv1.ContextFabricFinding{},
		Cohort: &contractsv1.ContextFabricCohort{
			Kind: contractsv1.ContextFabricSubjectTeam,
			Members: []contractsv1.ContextFabricCohortMember{
				{Subject: teamA, Rank: 1, InclusionReasons: []string{"highest load"}, EvidenceRefIDs: []string{"evidence_cohort_1"}},
				{Subject: teamB, Rank: 2, InclusionReasons: []string{"rising load"}, EvidenceRefIDs: []string{"evidence_cohort_2"}},
			},
			Rationale: "Teams carrying the most open work.",
			Complete:  true,
		},
		Limitations:    []string{"deployments source unavailable"},
		EvidenceRefIDs: []string{"evidence_driver_01", "evidence_driver_02"},
		ClaimedFacts: []contractsv1.ContextFabricClaimedFact{
			{ClaimID: "claim_blockers_1", Kind: contractsv1.ContextFabricFactBlockers, Subject: project, Field: "open_blockers", Value: stringValue("3")},
			{ClaimID: "claim_status_001", Kind: contractsv1.ContextFabricFactStatus, Subject: project, Field: "status", Value: stringValue("amber")},
			{ClaimID: "claim_reviews_01", Kind: contractsv1.ContextFabricFactReviews, Subject: project, Field: "stalled_reviews", Value: stringValue("2")},
			{ClaimID: "claim_work_00001", Kind: contractsv1.ContextFabricFactWork, Subject: project, Field: "open_items", Value: stringValue("7")},
		},
		Coverage: contractsv1.ContextFabricCoverage{
			Sources: []contractsv1.ContextFabricSourceObservation{
				{Source: "work_items", State: contractsv1.ContextFabricSourceAvailable},
				{Source: "deployments", State: contractsv1.ContextFabricSourceUnavailable, Reason: "source not configured"},
			},
			Partial:         true,
			DegradedReasons: []string{"deployments unavailable"},
		},
		Versions: contractsv1.ContextFabricVersionSet{
			ServiceVersion: "acr-v1", ContractVersion: contractsv1.ContextFabricInvestigationResultSchema, Backend: "graph",
			ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1",
			SynthesisVersion: "synthesis-v1", CanonicalServiceVersion: "ops-v1",
		},
		DeterministicAnswer: "Ask Dev is not release-ready because required work remains.",
		Warnings:            []string{},
	}
}

func driver(id string, standing contractsv1.ContextFabricDriverStanding, category, title string, claims []string, subjects ...contractsv1.ContextFabricSubjectRef) contractsv1.ContextFabricDriverJudgment {
	return contractsv1.ContextFabricDriverJudgment{
		DriverID: id, Standing: standing, Category: category, Title: title,
		Summary:          title + " summary.",
		AffectedSubjects: subjects,
		EvidenceRefIDs:   []string{"evidence_" + id},
		ClaimedFactIDs:   claims,
		Derivation:       contractsv1.ContextFabricDerivationCanonicalStructured,
		EpistemicStatus:  contractsv1.ContextFabricEpistemicObserved,
		Confidence:       0.8,
		Current:          true,
	}
}

func withheldDriver(id string, subjects ...contractsv1.ContextFabricSubjectRef) contractsv1.ContextFabricDriverJudgment {
	value := driver(id, contractsv1.ContextFabricDriverWithheld, "narrative", "Withheld judgment", nil, subjects...)
	value.Qualification = "Evidence was too thin to stand behind."
	return value
}

// TestFixtureIsCanonicallyValid keeps the rest of this file honest: every
// assertion below describes how a VALID canonical result projects, so a
// fixture that could never come out of the engine would prove nothing.
func TestFixtureIsCanonicallyValid(t *testing.T) {
	if err := richResult().Validate(); err != nil {
		t.Fatalf("fixture is not a valid canonical result: %v", err)
	}
}

func TestProjectionIsValidAndCopiesJudgmentVerbatim(t *testing.T) {
	result := richResult()
	projection := Project(result, Budget{})

	if err := projection.Validate(); err != nil {
		t.Fatalf("projection failed contract validation: %v", err)
	}
	if projection.SchemaVersion != contractsv1.ContextFabricAnswerProjectionSchema {
		t.Errorf("schema_version = %q", projection.SchemaVersion)
	}
	// The judgment itself must survive projection untouched. This is the
	// core CHAOS-3746 parity requirement: consumer-specific truncation
	// cannot change the underlying judgment.
	if projection.DirectJudgment != result.DirectJudgment {
		t.Errorf("direct_judgment was rewritten: %q != %q", projection.DirectJudgment, result.DirectJudgment)
	}
	if projection.CurrentState != result.CurrentState {
		t.Errorf("current_state was rewritten: %q != %q", projection.CurrentState, result.CurrentState)
	}
	if projection.ResultID != result.ResultID || projection.RequestID != result.RequestID {
		t.Errorf("replay identifiers were not carried through")
	}
	if !projection.GeneratedAt.Equal(result.GeneratedAt) || projection.Status != result.Status {
		t.Errorf("generated_at or status was not carried through")
	}
	if !reflect.DeepEqual(projection.Versions, result.Versions) {
		t.Errorf("versions must be copied whole for replay and diagnostics")
	}
	if !reflect.DeepEqual(projection.CommittedSubjects, result.SubjectResolution.Committed) {
		t.Errorf("committed subjects must mirror the canonical resolution exactly")
	}
	if projection.CoveragePartial != result.Coverage.Partial {
		t.Errorf("coverage_partial must be carried through")
	}
}

// TestRetainedDriversKeepCanonicalStandingAndCategory proves the projection
// never re-judges. A projection that promoted a contributing driver to
// principal would change the answer while looking like a summary.
func TestRetainedDriversKeepCanonicalStandingAndCategory(t *testing.T) {
	result := richResult()
	projection := Project(result, Budget{})

	canonical := make(map[string]contractsv1.ContextFabricDriverJudgment, len(result.Drivers))
	for _, driver := range result.Drivers {
		canonical[driver.DriverID] = driver
	}
	for _, projected := range projection.PrincipalDrivers {
		source, ok := canonical[projected.DriverID]
		if !ok {
			t.Fatalf("projection invented driver %q", projected.DriverID)
		}
		if projected.Standing != source.Standing {
			t.Errorf("driver %q standing changed: %q != %q", projected.DriverID, projected.Standing, source.Standing)
		}
		if projected.Category != source.Category {
			t.Errorf("driver %q category changed: %q != %q", projected.DriverID, projected.Category, source.Category)
		}
		if projected.Title != source.Title || projected.Summary != source.Summary {
			t.Errorf("driver %q text was reworded", projected.DriverID)
		}
		if projected.Confidence != source.Confidence {
			t.Errorf("driver %q confidence changed", projected.DriverID)
		}
	}
}

// TestWithheldDriversAreExcludedAndDeclared proves a withheld judgment never
// reaches a consumer as part of the answer, and that its absence is
// visible rather than silent.
func TestWithheldDriversAreExcludedAndDeclared(t *testing.T) {
	projection := Project(richResult(), Budget{})

	for _, driver := range projection.PrincipalDrivers {
		if driver.Standing == contractsv1.ContextFabricDriverWithheld {
			t.Fatalf("withheld driver %q reached the projection", driver.DriverID)
		}
	}
	if projection.ProjectionBudget.WithheldDriversOmitted != 1 {
		t.Errorf("withheld_drivers_omitted = %d, want 1", projection.ProjectionBudget.WithheldDriversOmitted)
	}
	if !projection.ProjectionBudget.Truncated {
		t.Errorf("a projection that dropped a withheld driver must declare truncation")
	}
}

// TestDriverSelectionOrdersByStandingAndKeepsCanonicalOrderWithinIt pins
// both halves of the ordering rule: selection follows the engine's own
// standing field, and within one standing the canonical array order is
// preserved rather than re-sorted by some attribute the projection picked.
func TestDriverSelectionOrdersByStandingAndKeepsCanonicalOrderWithinIt(t *testing.T) {
	projection := Project(richResult(), Budget{})

	got := make([]string, 0, len(projection.PrincipalDrivers))
	for _, driver := range projection.PrincipalDrivers {
		got = append(got, driver.DriverID)
	}
	want := []string{
		"driver_principal_1", // principal, first in canonical order
		"driver_principal_2", // principal, second in canonical order
		"driver_contrib_001", // contributing
		"driver_symptom_001", // symptom
		"driver_context_01",  // context
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("driver order = %v, want %v", got, want)
	}
}

// TestBudgetTruncationIsDeclared proves the honesty rule: what the budget
// drops is always counted, and Truncated matches.
func TestBudgetTruncationIsDeclared(t *testing.T) {
	result := richResult()
	projection := Project(result, Budget{MaxDrivers: 2, MaxCohortMembers: 1})

	if len(projection.PrincipalDrivers) != 2 {
		t.Fatalf("retained %d drivers, want 2", len(projection.PrincipalDrivers))
	}
	// Five non-withheld drivers, two retained, so three were dropped.
	if projection.ProjectionBudget.DriversOmitted != 3 {
		t.Errorf("drivers_omitted = %d, want 3", projection.ProjectionBudget.DriversOmitted)
	}
	if projection.ProjectionBudget.CohortMembersOmitted != 1 {
		t.Errorf("cohort_members_omitted = %d, want 1", projection.ProjectionBudget.CohortMembersOmitted)
	}
	if !projection.ProjectionBudget.Truncated {
		t.Errorf("truncated must be set when content was dropped")
	}
	if projection.Cohort == nil || projection.Cohort.Total != 2 {
		t.Errorf("cohort total must report the canonical size, not the retained count")
	}
	if err := projection.Validate(); err != nil {
		t.Fatalf("truncated projection failed validation: %v", err)
	}
}

// TestUntruncatedProjectionDoesNotClaimTruncation guards the other
// direction of the if-and-only-if: a projection that claimed truncation it
// never performed would teach a caller to distrust complete answers.
func TestUntruncatedProjectionDoesNotClaimTruncation(t *testing.T) {
	result := richResult()
	result.Drivers = result.Drivers[1:2] // one principal driver, nothing withheld
	result.Cohort = nil

	projection := Project(result, Budget{})
	if projection.ProjectionBudget.Truncated {
		t.Errorf("projection declared truncation with nothing dropped: %+v", projection.ProjectionBudget)
	}
	if err := projection.Validate(); err != nil {
		t.Fatalf("projection failed validation: %v", err)
	}
}

// TestRetainedDriverClaimsAlwaysResolve is the invariant that makes
// value-level evidence usable: a consumer must be able to check every claim
// a retained driver cites, so the projection may never keep a driver whose
// facts it dropped.
func TestRetainedDriverClaimsAlwaysResolve(t *testing.T) {
	for _, maxFacts := range []int{1, 2, 3, 4, 50} {
		projection := Project(richResult(), Budget{MaxFacts: maxFacts})
		carried := make(map[string]struct{}, len(projection.KeyFacts))
		for _, fact := range projection.KeyFacts {
			carried[fact.ClaimID] = struct{}{}
		}
		for _, driver := range projection.PrincipalDrivers {
			for _, claimID := range driver.ClaimedFactIDs {
				if _, ok := carried[claimID]; !ok {
					t.Fatalf("max_facts=%d: driver %q cites claim %q the projection dropped", maxFacts, driver.DriverID, claimID)
				}
			}
		}
		if err := projection.Validate(); err != nil {
			t.Fatalf("max_facts=%d: projection failed validation: %v", maxFacts, err)
		}
	}
}

// TestReceiptsBindOnlyResolvedSubjectsToThisResult proves bounded
// continuation is safe: an ambiguous candidate gets no receipt, so a
// follow-up turn cannot silently carry unresolved ambiguity forward.
func TestReceiptsBindOnlyResolvedSubjectsToThisResult(t *testing.T) {
	result := richResult()
	projection := Project(result, Budget{})

	got := make([]string, 0, len(projection.SubjectReceipts))
	for _, receipt := range projection.SubjectReceipts {
		if receipt.ResultID != result.ResultID {
			t.Errorf("receipt %q bound to result %q, want %q", receipt.ReceiptID, receipt.ResultID, result.ResultID)
		}
		got = append(got, receipt.ReceiptID)
	}
	want := []string{"receipt_committed_1", "receipt_proposed_1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("receipts = %v, want %v (ambiguous candidates must not get one)", got, want)
	}
}

// TestClarificationAppearsOnlyWhenTheEngineAskedForIt keeps a settled
// answer from looking unsettled.
func TestClarificationAppearsOnlyWhenTheEngineAskedForIt(t *testing.T) {
	complete := Project(richResult(), Budget{})
	if complete.Clarification != nil {
		t.Errorf("a complete answer must not carry a clarification block")
	}

	ambiguous := richResult()
	ambiguous.Status = contractsv1.ContextFabricInvestigationClarificationRequired
	ambiguous.SubjectResolution.ClarificationPrompt = "Which team did you mean?"
	projection := Project(ambiguous, Budget{})
	if projection.Clarification == nil {
		t.Fatalf("clarification_required answer carried no clarification block")
	}
	if projection.Clarification.Prompt != ambiguous.SubjectResolution.ClarificationPrompt {
		t.Errorf("clarification prompt was reworded")
	}
	if len(projection.Clarification.Candidates) != 3 {
		t.Errorf("carried %d candidates, want 3", len(projection.Clarification.Candidates))
	}
	if err := projection.Validate(); err != nil {
		t.Fatalf("clarification projection failed validation: %v", err)
	}
}

// TestEvidenceRefsIndexOnlyRetainedContent proves the projection never
// advertises evidence for content it dropped. A caller that saw a reference
// with no visible driver behind it would have no way to tell what claim it
// was supposed to support.
func TestEvidenceRefsIndexOnlyRetainedContent(t *testing.T) {
	result := richResult()
	projection := Project(result, Budget{MaxDrivers: 1})

	if len(projection.PrincipalDrivers) != 1 {
		t.Fatalf("retained %d drivers, want 1", len(projection.PrincipalDrivers))
	}
	retained := map[string]struct{}{}
	for _, id := range projection.EvidenceRefIDs {
		retained[id] = struct{}{}
	}
	// Every reference the surviving driver cites must be indexed.
	for _, id := range projection.PrincipalDrivers[0].EvidenceRefIDs {
		if _, ok := retained[id]; !ok {
			t.Errorf("retained driver cites %q but the index omits it", id)
		}
	}
	// No reference belonging only to a dropped driver may appear.
	kept := projection.PrincipalDrivers[0].DriverID
	for _, driver := range result.Drivers {
		if driver.DriverID == kept {
			continue
		}
		for _, id := range driver.EvidenceRefIDs {
			if _, ok := retained[id]; ok {
				t.Errorf("index carries %q from dropped driver %q", id, driver.DriverID)
			}
		}
	}
}

// TestProjectIsDeterministic pins replayability: the same result and budget
// must always produce byte-identical output, or a differential parity check
// across surfaces could never be trusted.
func TestProjectIsDeterministic(t *testing.T) {
	result := richResult()
	first, err := json.Marshal(Project(result, Budget{MaxDrivers: 3}))
	if err != nil {
		t.Fatalf("marshal first projection: %v", err)
	}
	for i := 0; i < 25; i++ {
		next, err := json.Marshal(Project(result, Budget{MaxDrivers: 3}))
		if err != nil {
			t.Fatalf("marshal projection %d: %v", i, err)
		}
		if string(first) != string(next) {
			t.Fatalf("projection is not deterministic at iteration %d", i)
		}
	}
}

// TestProjectDoesNotMutateTheCanonicalResult protects the hosted API, which
// projects a result it also persists and returns.
func TestProjectDoesNotMutateTheCanonicalResult(t *testing.T) {
	result := richResult()
	before, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	projection := Project(result, Budget{MaxDrivers: 1, MaxCohortMembers: 1, MaxFacts: 1, MaxEvidenceRefs: 1})
	// Mutating the projection's own slices must not reach back either.
	if len(projection.PrincipalDrivers) > 0 && len(projection.PrincipalDrivers[0].EvidenceRefIDs) > 0 {
		projection.PrincipalDrivers[0].EvidenceRefIDs[0] = "evidence_overwritten"
	}
	if len(projection.CommittedSubjects) > 0 {
		projection.CommittedSubjects[0].Label = "overwritten"
	}
	after, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("re-marshal result: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("Project mutated its input result")
	}
}

// TestBudgetClampsToContractMaxima proves no caller can request a
// projection that would fail validation on size alone.
func TestBudgetClampsToContractMaxima(t *testing.T) {
	bounds := Budget{MaxDrivers: 1 << 20, MaxCohortMembers: 1 << 20, MaxCandidates: 1 << 20, MaxFacts: 1 << 20, MaxEvidenceRefs: 1 << 20}.withDefaults()

	if bounds.MaxDrivers != contractsv1.ContextFabricProjectedDriversMaxCount {
		t.Errorf("max drivers = %d, want clamp to %d", bounds.MaxDrivers, contractsv1.ContextFabricProjectedDriversMaxCount)
	}
	if bounds.MaxCohortMembers != contractsv1.ContextFabricProjectedCohortMaxCount {
		t.Errorf("max cohort members = %d, want clamp to %d", bounds.MaxCohortMembers, contractsv1.ContextFabricProjectedCohortMaxCount)
	}
	if bounds.MaxCandidates != contractsv1.ContextFabricProjectedCandidatesMaxCount {
		t.Errorf("max candidates = %d, want clamp to %d", bounds.MaxCandidates, contractsv1.ContextFabricProjectedCandidatesMaxCount)
	}
	if bounds.MaxEvidenceRefs != contractsv1.ContextFabricProjectedEvidenceMaxCount {
		t.Errorf("max evidence refs = %d, want clamp to %d", bounds.MaxEvidenceRefs, contractsv1.ContextFabricProjectedEvidenceMaxCount)
	}
}

// TestMarkFullResultOmittedKeepsTheBudgetCoherent covers the option (a)
// byte-budget behavior: the full canonical result is dropped rather than
// truncated, and the drop is declared.
func TestMarkFullResultOmittedKeepsTheBudgetCoherent(t *testing.T) {
	projection := Project(richResult(), Budget{})
	MarkFullResultOmitted(&projection)

	if !projection.ProjectionBudget.FullResultOmitted || !projection.ProjectionBudget.Truncated {
		t.Errorf("full-result omission must be declared and set truncated")
	}
	if err := projection.Validate(); err != nil {
		t.Fatalf("projection failed validation after marking full-result omission: %v", err)
	}
}
