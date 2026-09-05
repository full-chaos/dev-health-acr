package graphrank

// THE VOCABULARY MAPPING between the shared predicate's reason and this seam's
// published basis, tested directly.
//
// A SEPARATE FILE from the agreement test beside it, deliberately: everything
// here names an identifier that does not exist on the parent commit, so this
// file cannot compile there. The agreement test DOES compile at the parent and
// fails at runtime with the ticket's measured twelve mismatches, which is what
// makes its red a statement about behaviour rather than about a missing
// identifier -- and keeping the two apart is what lets the red-at-parent proof
// copy that file VERBATIM instead of hand-trimming a variant nobody ships.
//
// It reuses `rankingFrameForKind` from the agreement file: same package, one
// fixture, so the two halves cannot come to disagree about what frame they are
// talking about.

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestTheSeamRefusesEveryKindTheDerivationCallsUnservable is the same
// agreement read from the other end, and it exists because the test above
// could be satisfied by two layers that are wrong together in the same way.
//
// This one does not ask the derivation at all. It asserts the seam's basis
// against the SHARED PREDICATE's own reason, which is the thing the derivation
// reads. If the mapping between the two vocabularies ever drifts -- a member
// added below, an arm re-pointed -- the seam's telemetry would name a basis
// that no longer describes the decision, and every consumer of that basis
// would be reading a stale label rather than a wrong answer, which is harder
// to notice.
func TestTheSeamRefusesEveryKindTheDerivationCallsUnservable(t *testing.T) {
	t.Parallel()
	// THE EXPECTED MAPPING, WRITTEN OUT INDEPENDENTLY. It is not derived from
	// `cohortKindBasisForDiscoverability`, because an expectation computed by
	// the thing it checks is decided by the very mutation it exists to catch.
	// A member added to either vocabulary fails the exhaustiveness check below
	// rather than silently acquiring whatever the default arm returns.
	want := map[contextfabric.CohortDiscoverability]CohortKindBasis{
		contextfabric.CohortDiscoverable:         CohortKindFromFrameMemberKind,
		contextfabric.CohortNotACohortVariant:    CohortKindNotACohortVariant,
		contextfabric.CohortNoMemberKind:         CohortKindNoMemberKind,
		contextfabric.CohortMemberKindUnservable: CohortKindMemberKindUnservable,
	}
	if len(want) != contextfabric.CohortDiscoverabilityCount {
		t.Fatalf("this table names %d reasons, the vocabulary declares %d -- a member was added and this table did not move with it", len(want), contextfabric.CohortDiscoverabilityCount)
	}

	// INJECTIVE, and that is the assertion a validity check cannot make. The
	// mapping's default arm returns a VALID basis by design (returning an
	// undeclared one would be worse), so a new vocabulary member falling
	// through to it would pass any "is the result valid" test while quietly
	// collapsing onto another member's basis. Distinctness is what sees that.
	seen := make(map[CohortKindBasis]contextfabric.CohortDiscoverability, len(want))
	for _, reason := range contextfabric.CohortDiscoverabilityVocabulary() {
		expected, named := want[reason]
		if !named {
			t.Errorf("reason %q is declared by the vocabulary but this test's expected mapping does not name it", reason)
			continue
		}
		basis := cohortKindBasisForDiscoverability(reason)
		if basis != expected {
			t.Errorf("reason %q maps to basis %q, want %q", reason, basis, expected)
		}
		if !ValidCohortKindBasis(basis) {
			t.Errorf("reason %q maps to basis %q, which this seam's vocabulary does not declare", reason, basis)
		}
		if other, collided := seen[basis]; collided {
			t.Errorf("reasons %q and %q both map to basis %q; the mapping must be injective, or a member that fell through to the default arm is indistinguishable from one that is mapped", other, reason, basis)
		}
		seen[basis] = reason
	}
	if len(seen) != contextfabric.CohortDiscoverabilityCount {
		t.Fatalf("the %d declared reasons produced %d distinct bases", contextfabric.CohortDiscoverabilityCount, len(seen))
	}

	checked := 0
	for _, published := range contractsv1.ContextFabricSubjectKindVocabulary() {
		kind := contextfabric.SubjectKind(published)
		frame := rankingFrameForKind(kind)

		_, _, reason := contextfabric.CohortMemberKindFor(frame.SubjectExpression)
		_, _, basis := cohortKindFromFrame(&frame)

		// Read from the INDEPENDENT table above, never from the mapping under
		// test: `basis != cohortKindBasisForDiscoverability(reason)` compares
		// the function with itself and is true of any mapping whatsoever.
		expected, named := want[reason]
		if !named {
			t.Fatalf("kind %q produced reason %q, which the expected mapping does not name", kind, reason)
		}
		if basis != expected {
			t.Errorf("kind %q: the shared predicate said %q, the seam reported basis %q, want %q", kind, reason, basis, expected)
		}
		if resolvable := contextfabric.CohortMemberSetResolvable(frame.SubjectExpression); resolvable != (basis == CohortKindFromFrameMemberKind) {
			t.Errorf("kind %q: CohortMemberSetResolvable = %v but the seam's basis is %q; the derivation reads the first and the seam publishes the second, so they must be one decision", kind, resolvable, basis)
		}
		checked++
	}
	if checked != contractsv1.ContextFabricSubjectKindCount {
		t.Fatalf("swept %d kinds, the published vocabulary has %d", checked, contractsv1.ContextFabricSubjectKindCount)
	}
}
