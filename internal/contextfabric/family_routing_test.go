package contextfabric

import "testing"

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
