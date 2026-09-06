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

// TestTheMappingDefaultArmFailsClosed pins the arm the vocabulary loop cannot
// reach.
//
// FOUND BY AN ADVERSARIAL ROUND, as a SURVIVING mutant: changing the default
// arm's return to `""` passed the whole suite. The loop above walks only
// DECLARED reasons, and the default is by construction unreachable from those,
// so the fail-closed guarantee the mapping's own comment makes was asserted
// nowhere.
//
// It matters because the guarantee is not decoration. `cohortKindFromFrame`
// hands its result straight to this seam's telemetry, so a default returning
// the empty basis would publish a value `ValidCohortKindBasis` refuses -- an
// undeclared token on an operator's log line, which is worse than a
// slightly-wrong-but-declared one.
func TestTheMappingDefaultArmFailsClosed(t *testing.T) {
	t.Parallel()
	// Values the vocabulary does not declare. The zero value is included on
	// purpose: it is what a caller gets from an unset field, and it is the one
	// an "is it valid" check is most likely to wave through.
	for _, undeclared := range []contextfabric.CohortDiscoverability{
		"",
		"not_a_declared_reason",
		"discoverable_but_misspelled",
		contextfabric.CohortDiscoverability("member_kind_unservable_typo"),
	} {
		// The premise: this really is outside the vocabulary. Without it a
		// renamed member would quietly turn these into declared values and the
		// test would stop exercising the default arm at all.
		if contextfabric.ValidCohortDiscoverability(undeclared) {
			t.Fatalf("%q is a DECLARED reason, so it does not reach the default arm and this case proves nothing", undeclared)
		}
		basis := cohortKindBasisForDiscoverability(undeclared)
		if !ValidCohortKindBasis(basis) {
			t.Errorf("the default arm returned %q for undeclared reason %q; that is not a basis this seam declares, so it would reach an operator's log line as an undefined token", basis, undeclared)
		}
		if basis != CohortKindMemberKindUnservable {
			t.Errorf("the default arm returned %q for undeclared reason %q, want %q -- the fail-closed choice is the REFUSING basis, never the discovering one", basis, undeclared, CohortKindMemberKindUnservable)
		}
		// The direction that would actually hurt: a default that discovers.
		if basis == CohortKindFromFrameMemberKind {
			t.Errorf("the default arm returned the DISCOVERING basis for undeclared reason %q -- an unknown reason must never read as 'a cohort was discovered'", undeclared)
		}
	}
}
