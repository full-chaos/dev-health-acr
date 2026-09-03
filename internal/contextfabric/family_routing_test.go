package contextfabric

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// THE TABLE AND THE PR BODY MUST BE THE SAME ARTIFACT. These tests exist so
// that a class added to the agreement vocabulary without a routing decision
// fails the build rather than falling through to a default, and so that the
// dispositions a reader sees in the PR body are the ones the code holds.

// Every FamilyAgreementClass member has EXACTLY ONE row, in the agreement
// vocabulary's own order. Order matters as much as coverage: the two lists
// are read side by side by anyone auditing the flip, and a silently
// reordered table would make that reading wrong without making it fail.
func TestFamilyRouteTableCoversEveryAgreementClassInOrder(t *testing.T) {
	t.Parallel()
	classes := FamilyAgreementClassVocabulary()
	if len(familyRouteTable) != len(classes) {
		t.Fatalf("routing table has %d rows for %d agreement classes -- every class needs exactly one decision", len(familyRouteTable), len(classes))
	}
	for i, class := range classes {
		if familyRouteTable[i].class != class {
			t.Fatalf("routing table row %d is %q, want %q -- the table must follow the agreement vocabulary's order", i, familyRouteTable[i].class, class)
		}
		if !ValidFamilyRouteSource(familyRouteTable[i].source) {
			t.Errorf("row %q has source %q, outside the closed vocabulary", class, familyRouteTable[i].source)
		}
		if !ValidFamilyRouteDisposition(familyRouteTable[i].disposition) {
			t.Errorf("row %q has disposition %q, outside the closed vocabulary", class, familyRouteTable[i].disposition)
		}
	}
}

// A frame-absent turn must NOT be counted as an agreement.
//
// THE REGRESSION: the frame-absent path used to install Class=agreed /
// Disposition=identical, so every turn with no validated frame landed in the
// `agreed` bucket the flip decision reads. The counters could not tell "both
// tables produced the same family" from "only one table ran".
//
// THIS TEST DRIVES THE REAL PATH. Its first version built a
// FamilyRouteDecision literal in the test and asserted on that, which is a
// test that cannot fail: reintroducing the production bug would have left it
// green. Review caught it. It now runs Interpret() with a receipt carrying NO
// frame and reads the event the production sink actually received.
func TestFrameAbsentIsNotCountedAsAgreement(t *testing.T) {
	t.Parallel()
	spy := &familyTelemetrySpy{}
	receipt := validModelReceiptFixture(ModelOperationInterpret)
	// No frame reaches validation on this turn -- the condition under test.
	receipt.QuestionFrame = nil
	receipt.FrameOutcome = ""

	interpreter := RuntimeQuestionInterpreter{
		Runtime:         fakeModelRuntime{interpreted: groupedInterpretation(), receipt: receipt},
		Sink:            &fakeReceiptSink{},
		FamilyTelemetry: spy,
	}
	if _, _, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest()); err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if len(spy.events) != 1 {
		t.Fatalf("got %d family events, want exactly 1", len(spy.events))
	}
	route := spy.events[0].Route
	if spy.events[0].Shadow.FrameObserved {
		t.Fatal("the fixture was supposed to carry no frame; it observed one, so this test proves nothing about the frame-absent path")
	}
	if route.Class == FamilyAgreementAgreed {
		t.Fatal("a frame-absent turn reported the `agreed` class; the projection never ran, so there was no agreement to report")
	}
	if route.Class != "" {
		t.Fatalf("Class = %q, want empty -- the agreement vocabulary describes comparisons and there was none", route.Class)
	}
	if route.Disposition != FamilyRouteNoFrameObserved {
		t.Fatalf("Disposition = %q, want %q", route.Disposition, FamilyRouteNoFrameObserved)
	}
	if route.Source != FamilyRoutePrecedence || route.Switched {
		t.Fatalf("frame-absent turn routed %q switched=%v, want precedence and no switch", route.Source, route.Switched)
	}
}

// Every declared disposition is USED by some row. A disposition nothing
// produces is a dead label that reads as coverage -- the same shape as a
// gate tier with no positive fixture, which can be dead for its whole life
// and never fail.
func TestEveryFamilyRouteDispositionIsUsed(t *testing.T) {
	t.Parallel()
	used := map[FamilyRouteDisposition]bool{}
	for _, rule := range familyRouteTable {
		used[rule.disposition] = true
	}
	// Produced by the frame-absent path rather than by a table row. It is
	// marked used only because TestFrameAbsentIsNotCountedAsAgreement DRIVES
	// that path and asserts this exact disposition -- not as a bare
	// exemption, which would let a disposition nothing produces pass as
	// covered.
	used[FamilyRouteNoFrameObserved] = true
	// Same discipline: produced by applyCarriedPlan and asserted by
	// TestCarriedOutcomeDoesNotKeepThePreCarryRoute below.
	used[FamilyRouteCarried] = true
	for _, member := range FamilyRouteDispositionVocabulary() {
		if !used[member] {
			t.Errorf("disposition %q is declared but no routing row produces it", member)
		}
	}
}

// THE DECISIONS THEMSELVES, pinned one class at a time.
//
// This is a deliberate restatement rather than a loop over the table: a test
// that derived its expectations FROM the table would pass no matter what the
// table said, which is the co-editable-authority defect this program has hit
// repeatedly. These constants are the ruling, written out.
func TestFamilyRouteDecisionsMatchTheRuling(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		class       FamilyAgreementClass
		source      FamilyRouteSource
		disposition FamilyRouteDisposition
	}{
		{FamilyAgreementAgreed, FamilyRouteProjected, FamilyRouteIdentical},
		{FamilyAgreementShapeDivergence, FamilyRouteProjected, FamilyRouteIntendedChange},
		{FamilyAgreementPrecedenceUnclassified, FamilyRouteProjected, FamilyRouteSwitchedAdditiveUnobserved},
		{FamilyAgreementProjectionUnclassified, FamilyRoutePrecedence, FamilyRouteDeclinedProjectionSilent},
		{FamilyAgreementGoalRowUnreachable, FamilyRoutePrecedence, FamilyRouteDeclinedNotObserved},
		{FamilyAgreementPrecedenceComparisonRow, FamilyRoutePrecedence, FamilyRouteDeclinedIndistinguishable},
		{FamilyAgreementOrganizationRoute, FamilyRoutePrecedence, FamilyRouteDeclinedWithdrawnClaim},
		{FamilyAgreementUnexplained, FamilyRoutePrecedence, FamilyRouteDeclinedUnexplained},
	} {
		t.Run(string(tc.class), func(t *testing.T) {
			t.Parallel()
			decision := RouteQuestionFamily(FamilyAgreement{
				ProjectedFamily:  QuestionFamilySubjectInvestigation,
				PrecedenceFamily: QuestionFamilyDiscoveredCohortRanking,
				Class:            tc.class,
			})
			if decision.Source != tc.source {
				t.Fatalf("class %q routed from %q, want %q", tc.class, decision.Source, tc.source)
			}
			if decision.Disposition != tc.disposition {
				t.Fatalf("class %q has disposition %q, want %q", tc.class, decision.Disposition, tc.disposition)
			}
			want := QuestionFamilyDiscoveredCohortRanking
			if tc.source == FamilyRouteProjected {
				want = QuestionFamilySubjectInvestigation
			}
			if decision.Family != want {
				t.Fatalf("class %q served %q, want %q", tc.class, decision.Family, want)
			}
		})
	}
}

// `Switched` means "the served family DIFFERS from what precedence alone
// would have served", and it is NOT the same fact as "the source is the
// projection". The agreed class serves the projection and switches nothing,
// which is precisely why 21 of the 25 measured rows were safe -- a counter
// that conflated the two would report every agreeing answer as a behaviour
// change and make the flip look 21x larger than it is.
func TestSwitchedIsMeasuredAgainstPrecedenceNotAgainstSource(t *testing.T) {
	t.Parallel()
	agreed := RouteQuestionFamily(FamilyAgreement{
		ProjectedFamily:  QuestionFamilyGroupedCohortStatus,
		PrecedenceFamily: QuestionFamilyGroupedCohortStatus,
		Class:            FamilyAgreementAgreed,
	})
	if agreed.Source != FamilyRouteProjected {
		t.Fatalf("agreed source = %q, want the projection", agreed.Source)
	}
	if agreed.Switched {
		t.Fatal("agreed reported Switched -- the two tables produced the same family, so nothing changed")
	}

	diverged := RouteQuestionFamily(FamilyAgreement{
		ProjectedFamily:  QuestionFamilyGroupedCohortStatus,
		PrecedenceFamily: QuestionFamilyDiscoveredCohortRanking,
		Class:            FamilyAgreementShapeDivergence,
	})
	if !diverged.Switched {
		t.Fatal("shape_divergence with different families did not report Switched")
	}

	// A DECLINED class never switches, even when the two families differ --
	// that is what declining means.
	declined := RouteQuestionFamily(FamilyAgreement{
		ProjectedFamily:  QuestionFamilySubjectInvestigation,
		PrecedenceFamily: QuestionFamilyDiscoveredCohortRanking,
		Class:            FamilyAgreementOrganizationRoute,
	})
	if declined.Switched {
		t.Fatal("organization_route reported Switched -- it is declined, so the precedence family is served unchanged")
	}
	if declined.Family != QuestionFamilyDiscoveredCohortRanking {
		t.Fatalf("organization_route served %q, want the precedence family unchanged", declined.Family)
	}
}

// An unrecognized class -- which ClassifyFamilyAgreement cannot produce,
// since its last arm is `unexplained` -- must still route somewhere, and
// that somewhere is today's behaviour. A routing function that fell through
// to a zero-valued family would serve the empty string.
func TestUnknownClassFallsBackToPrecedence(t *testing.T) {
	t.Parallel()
	decision := RouteQuestionFamily(FamilyAgreement{
		ProjectedFamily:  QuestionFamilySubjectInvestigation,
		PrecedenceFamily: QuestionFamilyExplicitComparison,
		Class:            FamilyAgreementClass("a_class_that_does_not_exist"),
	})
	if decision.Family != QuestionFamilyExplicitComparison || decision.Source != FamilyRoutePrecedence {
		t.Fatalf("unknown class served %q from %q, want the precedence family", decision.Family, decision.Source)
	}
	if decision.Switched {
		t.Fatal("unknown class reported Switched")
	}
}

// A carried outcome must not keep reporting the routing decision made BEFORE
// the carry. Neither table decided that answer -- a prior turn did.
//
// This drives applyCarriedPlan itself rather than asserting on a literal,
// which is the correction round 3 forced on the sibling test above.
func TestCarriedOutcomeDoesNotKeepThePreCarryRoute(t *testing.T) {
	t.Parallel()
	// An outcome as the interpreter would leave it: unclassified, and
	// already carrying a routing decision from the frame-absent path.
	outcome := QuestionFamilyOutcome{
		Family: QuestionFamilyUnclassified,
		Route: FamilyRouteDecision{
			Family: QuestionFamilyUnclassified, Source: FamilyRoutePrecedence,
			Disposition: FamilyRouteNoFrameObserved,
		},
	}
	carried, applied := applyCarriedPlan(outcome, planCarryResult{
		Outcome: PlanCarryHit, Family: QuestionFamilyGroupedCohortStatus, GroupKind: SubjectTeam,
	})
	if !applied {
		t.Fatal("the carry did not apply; this test proves nothing")
	}
	if carried.Family != QuestionFamilyGroupedCohortStatus {
		t.Fatalf("carried family = %q, want the prior turn's", carried.Family)
	}
	if carried.Route.Disposition != FamilyRouteCarried {
		t.Fatalf("Route.Disposition = %q, want %q -- the outcome still reports a decision the carry superseded",
			carried.Route.Disposition, FamilyRouteCarried)
	}
	if carried.Route.Family != carried.Family {
		t.Fatalf("Route.Family = %q but the served family is %q; the outcome contradicts itself",
			carried.Route.Family, carried.Family)
	}
	// EVERY field of the decision, not just the disposition. The first
	// version of this test asserted the disposition alone, which left a
	// route claiming `precedence` produced the family (it did not -- it
	// produced unclassified, which is why the carry applied) and claiming
	// nothing switched (it did -- unclassified became a real family). Both
	// were caught by review because this test did not look at them.
	if carried.Route.Source != FamilyRouteSourceCarried {
		t.Fatalf("Route.Source = %q, want %q -- neither table produced this family, a prior turn did",
			carried.Route.Source, FamilyRouteSourceCarried)
	}
	if !carried.Route.Switched {
		t.Fatal("Route.Switched = false, but the carry replaced `unclassified` with a real family; that is a change to what is served")
	}
	if !ValidFamilyRouteSource(carried.Route.Source) {
		t.Fatalf("Route.Source %q is outside the closed vocabulary", carried.Route.Source)
	}
}

// A carry that does NOT change the family must not report a switch. Without
// this, `Switched` on the carry path could be hardcoded true and the test
// above would still pass -- the mirror of the bug it just caught.
func TestCarryThatChangesNothingReportsNoSwitch(t *testing.T) {
	t.Parallel()
	outcome := QuestionFamilyOutcome{Family: QuestionFamilyUnclassified}
	carried, applied := applyCarriedPlan(outcome, planCarryResult{
		Outcome: PlanCarryHit, Family: QuestionFamilyUnclassified,
	})
	if !applied {
		t.Fatal("the carry did not apply; this test proves nothing")
	}
	if carried.Route.Switched {
		t.Fatal("a carry that produced the same family reported Switched")
	}
}
