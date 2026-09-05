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
	// Every reason the shared vocabulary declares must map to a basis this
	// seam declares. Quantified over the vocabulary, so a new member fails
	// here rather than reaching a log line as an undeclared basis.
	for _, reason := range contextfabric.CohortDiscoverabilityVocabulary() {
		basis := cohortKindBasisForDiscoverability(reason)
		if !ValidCohortKindBasis(basis) {
			t.Errorf("reason %q maps to basis %q, which this seam's vocabulary does not declare", reason, basis)
		}
	}

	checked := 0
	for _, published := range contractsv1.ContextFabricSubjectKindVocabulary() {
		kind := contextfabric.SubjectKind(published)
		frame := rankingFrameForKind(kind)

		_, _, reason := contextfabric.CohortMemberKindFor(frame.SubjectExpression)
		_, _, basis := cohortKindFromFrame(&frame)

		if want := cohortKindBasisForDiscoverability(reason); basis != want {
			t.Errorf("kind %q: the shared predicate said %q, the seam reported basis %q, want %q", kind, reason, basis, want)
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
