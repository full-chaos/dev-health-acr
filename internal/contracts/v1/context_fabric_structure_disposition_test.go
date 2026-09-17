package v1

import "testing"

// TestContextFabricStructureDispositionVocabularyAndValidatorAgree pins
// ContextFabricStructureDispositionVocabulary and
// ValidContextFabricStructureDisposition against the ONE closed set both
// read: applied, vetoed_unresolved, vetoed_conflict, vetoed_stale,
// superseded_by_caller, not_evaluated, in that declared order -- the empty
// value is deliberately excluded (a field emitting this type alongside ""
// declares that empty separately). A member dropped from the backing array
// changes what both the accessor and the validator report, so this test
// fails on either regression.
func TestContextFabricStructureDispositionVocabularyAndValidatorAgree(t *testing.T) {
	t.Parallel()
	want := [...]ContextFabricStructureDisposition{
		ContextFabricStructureDispositionApplied,
		ContextFabricStructureDispositionVetoedUnresolved,
		ContextFabricStructureDispositionVetoedConflict,
		ContextFabricStructureDispositionVetoedStale,
		ContextFabricStructureDispositionSupersededByCaller,
		ContextFabricStructureDispositionNotEvaluated,
	}
	got := ContextFabricStructureDispositionVocabulary()
	if got != want {
		t.Fatalf("ContextFabricStructureDispositionVocabulary() = %#v, want %#v", got, want)
	}
	for _, member := range want {
		if !ValidContextFabricStructureDisposition(member) {
			t.Errorf("ValidContextFabricStructureDisposition(%q) = false, want true", member)
		}
	}
	for _, notMember := range []ContextFabricStructureDisposition{"", "bogus", "vetoed"} {
		if ValidContextFabricStructureDisposition(notMember) {
			t.Errorf("ValidContextFabricStructureDisposition(%q) = true, want false", notMember)
		}
	}
}
