package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// decidingRequirementOutcomeRow and decidingUnevaluatedReadRequirement give
// the completeness-authority line the deciding requirement/stage/outcome/
// cause G1 asks for. These pin the SAME absorbing precedence the outcome
// derivation itself uses, over a set with more than one candidate row, so a
// change to either one's ordering is caught here rather than only in an
// end-to-end fixture that happens to carry a single candidate.

// TestUnavailableRequirementCauseNamesEachReasonDistinctly pins the wire
// split: unavailableRequirementCause maps EACH of the four derivation
// reasons to its own code, never collapsing no_declaring_producer and
// table_shape_undeclared onto the shared fact_unconfigured they used to
// share.
func TestUnavailableRequirementCauseNamesEachReasonDistinctly(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		reason RequirementUnavailableReason
		want   contractsv1.ContextFabricCoverageDetailCode
	}{
		{RequirementReasonSubjectKindUnsupported, contractsv1.ContextFabricCoverageDetailFactPruned},
		{RequirementReasonNoDeclaringProducer, contractsv1.ContextFabricCoverageDetailFactNoDeclaringProducer},
		{RequirementReasonTableShapeUndeclared, contractsv1.ContextFabricCoverageDetailFactTableShapeUndeclared},
		{RequirementReasonComputedPopulationAbsent, contractsv1.ContextFabricCoverageDetailFactPruned},
	} {
		if got := unavailableRequirementCause(tc.reason); got != tc.want {
			t.Errorf("unavailableRequirementCause(%q) = %q, want %q", tc.reason, got, tc.want)
		}
	}
	// No two of the four reasons may collapse onto the same code, except
	// the two the design deliberately shares (subject_kind_unsupported and
	// computed_population_absent both name fact_pruned -- neither is
	// actionable by a declaration or query change, so a shared code loses
	// nothing there).
	if unavailableRequirementCause(RequirementReasonNoDeclaringProducer) == unavailableRequirementCause(RequirementReasonTableShapeUndeclared) {
		t.Fatal("no_declaring_producer and table_shape_undeclared still collapse onto one wire code")
	}
}

func TestDecidingRequirementOutcomeRowPrecedence(t *testing.T) {
	t.Parallel()
	narrowedFirst := contractsv1.ContextFabricPlanRequirementOutcomeRow{
		Requirement: "state/member/team", Stage: contractsv1.ContextFabricOutcomeStageAssembledResult,
		Outcome: contractsv1.ContextFabricRequirementNarrowed, Impact: contractsv1.ContextFabricAnswerImpactDepth,
		CauseCoverage: contractsv1.ContextFabricCoverageDetailFactNarrowed,
	}
	notAttemptedSecond := contractsv1.ContextFabricPlanRequirementOutcomeRow{
		Requirement: "health/member/team", Stage: contractsv1.ContextFabricOutcomeStagePlanning,
		Outcome: contractsv1.ContextFabricRequirementNotAttempted, Impact: contractsv1.ContextFabricAnswerImpactDimension,
		CauseCoverage: contractsv1.ContextFabricCoverageDetailAnswerTerminatedBeforeAttempt,
	}
	unavailableThird := contractsv1.ContextFabricPlanRequirementOutcomeRow{
		Requirement: "principal_drivers/member/team", Stage: contractsv1.ContextFabricOutcomeStagePlanning,
		Outcome: contractsv1.ContextFabricRequirementUnavailable, Impact: contractsv1.ContextFabricAnswerImpactDimension,
		CauseCoverage: contractsv1.ContextFabricCoverageDetailFactNoDeclaringProducer,
	}
	satisfied := contractsv1.ContextFabricPlanRequirementOutcomeRow{
		Requirement: "readiness/member/team", Stage: contractsv1.ContextFabricOutcomeStagePlanning,
		Outcome: contractsv1.ContextFabricRequirementSatisfied,
	}
	// A SECOND, DISTINCT unavailable row -- the discriminator. A version
	// that merely REMEMBERS "the last unavailable row seen" instead of
	// short-circuiting on the FIRST would return unavailableAfterThird
	// here, because it is what a bare loop-and-overwrite ends on; only true
	// absorption (stop at the first one) returns unavailableThird.
	unavailableAfterThird := contractsv1.ContextFabricPlanRequirementOutcomeRow{
		Requirement: "trend_series/member/team", Stage: contractsv1.ContextFabricOutcomeStagePlanning,
		Outcome: contractsv1.ContextFabricRequirementUnavailable, Impact: contractsv1.ContextFabricAnswerImpactDimension,
		CauseCoverage: contractsv1.ContextFabricCoverageDetailFactTableShapeUndeclared,
	}

	for _, tc := range []struct {
		name string
		rows []contractsv1.ContextFabricPlanRequirementOutcomeRow
		want contractsv1.ContextFabricPlanRequirementOutcomeRow
		ok   bool
	}{
		{"unavailable is absorbing regardless of position", []contractsv1.ContextFabricPlanRequirementOutcomeRow{
			narrowedFirst, notAttemptedSecond, unavailableThird, satisfied,
		}, unavailableThird, true},
		{"the FIRST unavailable row wins, not the last -- true short-circuit", []contractsv1.ContextFabricPlanRequirementOutcomeRow{
			narrowedFirst, unavailableThird, unavailableAfterThird,
		}, unavailableThird, true},
		{"first partial-making row wins when no row is unavailable", []contractsv1.ContextFabricPlanRequirementOutcomeRow{
			satisfied, narrowedFirst, notAttemptedSecond,
		}, narrowedFirst, true},
		{"all lossless rows: no deciding row", []contractsv1.ContextFabricPlanRequirementOutcomeRow{
			satisfied, satisfied,
		}, contractsv1.ContextFabricPlanRequirementOutcomeRow{}, false},
		{"empty set: no deciding row", nil, contractsv1.ContextFabricPlanRequirementOutcomeRow{}, false},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := decidingRequirementOutcomeRow(tc.rows)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if ok && (got.Requirement != tc.want.Requirement || got.Outcome != tc.want.Outcome) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestDecidingUnevaluatedReadRequirementFindsTheGap(t *testing.T) {
	t.Parallel()
	seed := contractsv1.ContextFabricPlanRequirementOutcomeRow{
		Requirement: "state/member/team", Obligation: "state",
		Stage: contractsv1.ContextFabricOutcomeStagePlanning, Outcome: contractsv1.ContextFabricRequirementSatisfied,
	}
	evaluated := contractsv1.ContextFabricPlanRequirementOutcomeRow{
		Requirement: "state/member/team", Obligation: "state",
		Stage: contractsv1.ContextFabricOutcomeStageAssembledResult, Outcome: contractsv1.ContextFabricRequirementSatisfied,
	}
	computedSeed := contractsv1.ContextFabricPlanRequirementOutcomeRow{
		Requirement: "ranking/member/team", Obligation: "ranking",
		Stage: contractsv1.ContextFabricOutcomeStagePlanning, Outcome: contractsv1.ContextFabricRequirementSatisfied,
	}

	if _, ok := decidingUnevaluatedReadRequirement([]contractsv1.ContextFabricPlanRequirementOutcomeRow{seed, evaluated}); ok {
		t.Fatal("a seed WITH an assembled-result row was reported as an unevaluated read requirement")
	}
	identity, ok := decidingUnevaluatedReadRequirement([]contractsv1.ContextFabricPlanRequirementOutcomeRow{seed})
	if !ok || identity != seed.Requirement {
		t.Fatalf("identity = %q, ok = %v, want %q, true", identity, ok, seed.Requirement)
	}
	if _, ok := decidingUnevaluatedReadRequirement([]contractsv1.ContextFabricPlanRequirementOutcomeRow{computedSeed}); ok {
		t.Fatal("a COMPUTED obligation's bare seed was reported as an unevaluated READ requirement")
	}
}

// TestDeriveCompletenessAuthorityNamesTheDecidingRow drives the full
// derivation over a mixed row set and pins that the observation's deciding
// fields and outcome-row digest agree with what the rows actually say --
// end to end, not just at the pure-helper level above.
func TestDeriveCompletenessAuthorityNamesTheDecidingRow(t *testing.T) {
	t.Parallel()
	result := InvestigationResult{
		Status: InvestigationComplete,
		Completeness: contractsv1.ContextFabricAnswerCompleteness{
			Outcomes: []contractsv1.ContextFabricPlanRequirementOutcomeRow{
				{Requirement: "state/member/team", Obligation: "state", Stage: contractsv1.ContextFabricOutcomeStageAssembledResult, Outcome: contractsv1.ContextFabricRequirementSatisfied},
				{
					Requirement: "health/member/team", Obligation: "health", Stage: contractsv1.ContextFabricOutcomeStagePlanning,
					Outcome: contractsv1.ContextFabricRequirementUnavailable, Impact: contractsv1.ContextFabricAnswerImpactDimension,
					CauseCoverage: contractsv1.ContextFabricCoverageDetailFactTableShapeUndeclared, CauseObserved: true,
				},
			},
		},
		ClaimedFacts: []ClaimedFact{
			{ClaimID: "c1", Kind: contractsv1.ContextFabricFactStatus, Field: "status"},
			{ClaimID: "c2", Kind: contractsv1.ContextFabricFactStatus, Field: "status"},
			{ClaimID: "c3", Kind: contractsv1.ContextFabricFactHealth, Field: "health"},
		},
	}

	observation := DeriveCompletenessAuthority(result)
	if observation.DecidingRequirement != "health/member/team" ||
		observation.DecidingStage != contractsv1.ContextFabricOutcomeStagePlanning ||
		observation.DecidingOutcome != contractsv1.ContextFabricRequirementUnavailable ||
		observation.DecidingCauseCoverage != contractsv1.ContextFabricCoverageDetailFactTableShapeUndeclared {
		t.Fatalf("deciding row = %+v, want the health/member/team unavailable row", observation)
	}
	if observation.DecidingReadEvaluationGap {
		t.Fatal("DecidingReadEvaluationGap is true, but a real unavailable row decided the state")
	}
	if observation.OutcomeRowsTotal != 2 {
		t.Fatalf("OutcomeRowsTotal = %d, want 2", observation.OutcomeRowsTotal)
	}
	satisfiedIndex, ok := outcomeTokenIndex(contractsv1.ContextFabricRequirementSatisfied)
	if !ok {
		t.Fatal("satisfied is not in its own vocabulary")
	}
	unavailableIndex, ok := outcomeTokenIndex(contractsv1.ContextFabricRequirementUnavailable)
	if !ok {
		t.Fatal("unavailable is not in its own vocabulary")
	}
	if observation.OutcomeRowsByKind[satisfiedIndex] != 1 || observation.OutcomeRowsByKind[unavailableIndex] != 1 {
		t.Fatalf("OutcomeRowsByKind = %+v, want exactly one satisfied and one unavailable", observation.OutcomeRowsByKind)
	}
	statusIndex, ok := factKindIndex(contractsv1.ContextFabricFactStatus)
	if !ok {
		t.Fatal("status is not in its own fact-kind vocabulary")
	}
	healthIndex, ok := factKindIndex(contractsv1.ContextFabricFactHealth)
	if !ok {
		t.Fatal("health is not in its own fact-kind vocabulary")
	}
	if observation.ClaimedFactsByKind[statusIndex] != 2 || observation.ClaimedFactsByKind[healthIndex] != 1 {
		t.Fatalf("ClaimedFactsByKind[status]=%d ClaimedFactsByKind[health]=%d, want 2 and 1",
			observation.ClaimedFactsByKind[statusIndex], observation.ClaimedFactsByKind[healthIndex])
	}
}

// TestDeriveCompletenessAuthorityReadEvaluationGapNamesTheRequirement drives
// the OTHER path to `partial`: every outcome token is lossless, and the
// read-evaluation pass alone (hasPlanningOnlyReadRequirement) demotes the
// state because a READ requirement's only account is its planning seed.
func TestDeriveCompletenessAuthorityReadEvaluationGapNamesTheRequirement(t *testing.T) {
	t.Parallel()
	result := InvestigationResult{
		Status: InvestigationComplete,
		Completeness: contractsv1.ContextFabricAnswerCompleteness{
			Outcomes: []contractsv1.ContextFabricPlanRequirementOutcomeRow{
				{Requirement: "state/member/team", Obligation: "state", Stage: contractsv1.ContextFabricOutcomeStagePlanning, Outcome: contractsv1.ContextFabricRequirementSatisfied},
			},
		},
	}
	observation := DeriveCompletenessAuthority(result)
	if observation.ServerState != contractsv1.ContextFabricAnswerCompletenessPartial {
		t.Fatalf("ServerState = %q, want partial (the read-evaluation gap)", observation.ServerState)
	}
	if !observation.DecidingReadEvaluationGap {
		t.Fatal("DecidingReadEvaluationGap = false, want true -- no outcome row decided this, the read-evaluation pass did")
	}
	if observation.DecidingRequirement != "state/member/team" {
		t.Fatalf("DecidingRequirement = %q, want state/member/team", observation.DecidingRequirement)
	}
	if observation.DecidingOutcome != contractsv1.ContextFabricRequirementSatisfied {
		t.Fatalf("DecidingOutcome = %q, want satisfied -- the seed's own honest outcome, not a fabricated one", observation.DecidingOutcome)
	}
	if observation.DecidingCauseCoverage != "" || observation.DecidingCauseOverrun != "" || observation.DecidingCauseNarrowing != "" {
		t.Fatalf("a read-evaluation gap fabricated a cause: %+v", observation)
	}
}
