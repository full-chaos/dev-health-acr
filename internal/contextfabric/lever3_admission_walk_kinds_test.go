package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// This file is SEPARATE from lever3_admission_binding_class_test.go on
// purpose. The test below names forEachCitableSynthesisSubject directly, so
// it cannot compile at the fix parent -- and a red-first proof that fails
// only as a build error proves nothing. Keeping it here leaves the pinning
// tests in a file that COMPILES at the parent and fails there on its own
// assertions.

// TestTheAdmissionWalkVisitsOnlyClosedVocabularySubjectKinds is the guard
// against the walk itself becoming a hole. Every subject it admits carries a
// kind from the closed registry; a kind outside it would be a subject no
// contract validates.
func TestTheAdmissionWalkVisitsOnlyClosedVocabularySubjectKinds(t *testing.T) {
	t.Parallel()
	input, _ := everyAdmissionSourceInput()
	visited := 0
	forEachCitableSynthesisSubject(input, func(subject SubjectRef) {
		visited++
		if !contractsv1.ValidContextFabricSubjectKind(subject.Kind) {
			t.Errorf("admitted subject %q carries kind %q, which is outside the closed vocabulary", subject.CanonicalID, subject.Kind)
		}
	})
	if visited == 0 {
		t.Fatal("the admission walk visited nothing: the fixture no longer exercises it")
	}
}
