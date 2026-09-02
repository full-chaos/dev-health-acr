package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The B8 shadow gate's own tests: every basis reachable, the served
// document unchanged, and the declined derivation kept distinct from an
// agreement.

func statusShadowPlan() AnswerPlan {
	return AnswerPlan{
		Family:        QuestionFamilySubjectInvestigation,
		FamilyVersion: QuestionFamilyTableVersion,
	}
}

// TestEveryServerStatusBasisHasAFixtureThatLandsInIt is the dead-tier gate
// on the basis vocabulary.
//
// A basis token no input can produce is a dead tier: it reads as green for
// its whole life, and the distribution the flip decision reads shows a
// structural zero as though it were an empirical one. Each fixture below
// builds the situation its basis names.
func TestEveryServerStatusBasisHasAFixtureThatLandsInIt(t *testing.T) {
	t.Parallel()

	withPlan := func(mutate func(*AnswerPlan), result InvestigationResult) InvestigationResult {
		plan := statusShadowPlan()
		if mutate != nil {
			mutate(&plan)
		}
		result.AnswerPlan = &plan
		return result
	}

	cases := []struct {
		basis  ServerStatusBasis
		why    string
		result InvestigationResult
	}{
		{
			basis:  ServerStatusBasisNoPlan,
			why:    "a terminal exit stamps no plan, so there are no demands to hold the answer against and the server DECLINES rather than guessing",
			result: InvestigationResult{Status: InvestigationComplete},
		},
		{
			basis: ServerStatusBasisNoClaimedFacts,
			why:   "the plan named fact kinds to read and the answer carries no claimed fact at all",
			result: withPlan(func(plan *AnswerPlan) {
				plan.FactKinds = []contractsv1.ContextFabricFactKind{contractsv1.ContextFabricFactHealth}
			}, InvestigationResult{Status: InvestigationComplete}),
		},
		{
			basis: ServerStatusBasisDriversAbsent,
			why:   "the plan REQUIRES drivers -- not merely attempts them -- and the answer carries none",
			result: withPlan(func(plan *AnswerPlan) {
				plan.RequireDrivers = true
			}, InvestigationResult{
				Status:       InvestigationComplete,
				ClaimedFacts: []contractsv1.ContextFabricClaimedFact{{}},
			}),
		},
		{
			basis: ServerStatusBasisRankingAbsent,
			why:   "the plan REQUIRES a ranked cohort and there is no cohort to have ranked",
			result: withPlan(func(plan *AnswerPlan) {
				plan.RequireRanking = true
			}, InvestigationResult{
				Status:       InvestigationComplete,
				ClaimedFacts: []contractsv1.ContextFabricClaimedFact{{}},
			}),
		},
		{
			basis: ServerStatusBasisCoverageDegraded,
			why:   "the engine's own coverage block declares it did not see everything it meant to",
			result: withPlan(nil, InvestigationResult{
				Status:       InvestigationComplete,
				ClaimedFacts: []contractsv1.ContextFabricClaimedFact{{}},
				Coverage:     contractsv1.ContextFabricCoverage{DegradedReasons: []string{"a reason"}},
			}),
		},
		{
			basis: ServerStatusBasisLimitationDisclosed,
			why:   "a disclosed limitation, evaluated LAST because a limitation can be a note about scope rather than a gap",
			result: withPlan(nil, InvestigationResult{
				Status:       InvestigationComplete,
				ClaimedFacts: []contractsv1.ContextFabricClaimedFact{{}},
				Limitations:  []string{"a limitation"},
			}),
		},
		{
			basis: ServerStatusBasisServed,
			why:   "everything the plan demanded is present",
			result: withPlan(nil, InvestigationResult{
				Status:       InvestigationComplete,
				ClaimedFacts: []contractsv1.ContextFabricClaimedFact{{}},
			}),
		},
	}

	if len(cases) != ServerStatusBasisCount {
		t.Fatalf("%d fixtures for %d bases -- every basis needs one that LANDS in it", len(cases), ServerStatusBasisCount)
	}
	covered := map[ServerStatusBasis]bool{}
	for _, testCase := range cases {
		shadow := DeriveServerStatus(testCase.result)
		if shadow.Basis != testCase.basis {
			t.Errorf("fixture for %q landed on basis %q instead.\n  The fixture says: %s", testCase.basis, shadow.Basis, testCase.why)
		}
		if covered[testCase.basis] {
			t.Errorf("two fixtures declare basis %q; one basis is then untested", testCase.basis)
		}
		covered[testCase.basis] = true
		if shadow.Version != ServerStatusShadowVersion {
			t.Errorf("basis %q reported version %q -- every observation must name the rule that produced it, or two incomparable series get spliced", testCase.basis, shadow.Version)
		}
		if !ValidServerStatusBasis(shadow.Basis) {
			t.Errorf("basis %q is not a vocabulary member", shadow.Basis)
		}
	}
	for _, basis := range ServerStatusBasisVocabulary() {
		if !covered[basis] {
			t.Errorf("basis %q has no fixture that lands in it -- a closed token no input can produce is a dead tier that reads as green", basis)
		}
	}
}

// TestTheBasisOrderReportsTheMostSevereFailure pins the arm order.
//
// An answer with NO facts and a disclosed limitation has two things wrong
// with it. Reporting the limitation would name the smaller of the two, and
// an operator reading the distribution would conclude the gap is cosmetic.
// The order is the rule; this is what holds it.
func TestTheBasisOrderReportsTheMostSevereFailure(t *testing.T) {
	t.Parallel()
	plan := statusShadowPlan()
	plan.FactKinds = []contractsv1.ContextFabricFactKind{contractsv1.ContextFabricFactHealth}
	plan.RequireDrivers = true
	plan.RequireRanking = true

	shadow := DeriveServerStatus(InvestigationResult{
		Status:      InvestigationComplete,
		AnswerPlan:  &plan,
		Coverage:    contractsv1.ContextFabricCoverage{DegradedReasons: []string{"a reason"}},
		Limitations: []string{"a limitation"},
	})
	if shadow.Basis != ServerStatusBasisNoClaimedFacts {
		t.Fatalf("basis %q; an answer with every failure at once must report the COARSEST one, not the first weak signal found", shadow.Basis)
	}
}

// TestADeclinedDerivationIsNotAnAgreement is the countability gate.
//
// no_plan means the server could not compute a verdict. Folding that into
// "agreed" would put a measurement failure into the numerator's complement
// and make the disagreement rate -- the number the flip is decided on --
// quietly optimistic.
func TestADeclinedDerivationIsNotAnAgreement(t *testing.T) {
	t.Parallel()
	shadow := DeriveServerStatus(InvestigationResult{Status: InvestigationComplete})
	if shadow.Derived {
		t.Fatal("a result with no plan reported Derived")
	}
	if shadow.Disagreed {
		t.Fatal("a declined derivation reported a disagreement")
	}
	if shadow.ServerStatus != "" {
		t.Fatalf("a declined derivation produced server status %q -- a verdict it had no standing to reach", shadow.ServerStatus)
	}

	counters := NewServerStatusCounters()
	counters.Observe(shadow)
	if counters.Observed != 1 || counters.Derived != 0 || counters.Disagreed != 0 {
		t.Fatalf("counters observed=%d derived=%d disagreed=%d, want 1/0/0", counters.Observed, counters.Derived, counters.Disagreed)
	}
	for _, basis := range ServerStatusBasisVocabulary() {
		if _, present := counters.ByBasis[basis]; !present {
			t.Errorf("basis %q is absent from the distribution -- a distribution that omits its empty members cannot be told apart from one whose derivation never reaches them", basis)
		}
	}
}

// TestANonCompleteModelStatusIsNeverContradicted pins the gate's direction.
//
// The authorship move T6 proposes changes what happens when the model says
// COMPLETE and the plan's demands say otherwise. It has no standing to
// second-guess a clarification or a refusal, and manufacturing a
// disagreement in that direction would measure this placeholder
// derivation's opinion about paths it knows nothing about -- inflating the
// rate the flip is decided on with noise the flip would not remove.
func TestANonCompleteModelStatusIsNeverContradicted(t *testing.T) {
	t.Parallel()
	plan := statusShadowPlan()
	plan.RequireDrivers = true

	for _, status := range []InvestigationStatus{
		InvestigationPartial,
		contractsv1.ContextFabricInvestigationClarificationRequired,
		contractsv1.ContextFabricInvestigationNoMatch,
	} {
		shadow := DeriveServerStatus(InvestigationResult{
			Status:       status,
			AnswerPlan:   &plan,
			ClaimedFacts: []contractsv1.ContextFabricClaimedFact{{}},
		})
		if shadow.Disagreed {
			t.Errorf("model status %q was contradicted; the gate only ever contradicts `complete`", status)
		}
		if shadow.ServerStatus != status {
			t.Errorf("model status %q got server status %q; a non-complete status is agreed with, not overwritten", status, shadow.ServerStatus)
		}
		if shadow.Basis != ServerStatusBasisDriversAbsent {
			t.Errorf("model status %q reported basis %q; the BASIS is still reported on an agreement, or an operator cannot see why the two agreed", status, shadow.Basis)
		}
	}
}

// TestTheStatusShadowDoesNotTouchTheServedDocument is the "reports, does
// not route" property, asserted rather than claimed.
//
// The derivation is pure, so the strongest available statement is that a
// result handed to it comes back out of the served path unchanged. This
// runs the derivation over a result and asserts the result's own status,
// completeness block and terminal reason are all exactly what they were --
// including that ComputeAnswerCompleteness still reports the MODEL status,
// which is the field consumers branch on.
func TestTheStatusShadowDoesNotTouchTheServedDocument(t *testing.T) {
	t.Parallel()
	plan := statusShadowPlan()
	plan.RequireDrivers = true
	result := InvestigationResult{
		Status:       InvestigationComplete,
		AnswerPlan:   &plan,
		ClaimedFacts: []contractsv1.ContextFabricClaimedFact{{}},
	}

	before := ComputeAnswerCompleteness(result)
	shadow := DeriveServerStatus(result)
	after := ComputeAnswerCompleteness(result)

	if !shadow.Disagreed {
		t.Fatal("the fixture must DISAGREE, or this test proves nothing about a disagreement leaving the document alone")
	}
	if result.Status != InvestigationComplete {
		t.Fatalf("the served status moved to %q", result.Status)
	}
	if before != after {
		t.Fatalf("the completeness block moved: %+v -> %+v", before, after)
	}
	if after.TerminalStatus != InvestigationComplete {
		t.Fatalf("completeness.terminal_status is %q; it must still mirror the MODEL status, which is the field consumers branch on", after.TerminalStatus)
	}
	if after.TerminalReason != "" {
		t.Fatalf("terminal_reason moved to %q; answerTerminalReason is untouched by this slice", after.TerminalReason)
	}
}
