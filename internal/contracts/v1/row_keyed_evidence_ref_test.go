package v1

import "testing"

func TestRowKeyedEvidenceRefIsExactlyTheSingleRowKinds(t *testing.T) {
	rowKeyed := map[ContextFabricEvidenceEntityType]bool{
		ContextFabricEvidenceEntityPullRequest: true,
		ContextFabricEvidenceEntityWorkItem:    true,
		ContextFabricEvidenceEntityRepository:  true,
		ContextFabricEvidenceEntityCI:          true,
		ContextFabricEvidenceEntityDeployment:  true,
		ContextFabricEvidenceEntityReviewV2:    true,
		ContextFabricEvidenceEntityIncident:    true,
	}
	for _, kind := range ContextFabricEvidenceEntityTypeVocabulary() {
		ref := ContextFabricEvidenceRefPrefix + string(kind) + ":0b8f2a4e-1c3d-4e5f-8a9b-0c1d2e3f4a5b:x"
		if got := RowKeyedEvidenceRef(ref); got != rowKeyed[kind] {
			t.Errorf("RowKeyedEvidenceRef(%s) = %v, want %v", kind, got, rowKeyed[kind])
		}
	}
	for _, ref := range []string{"", "ev_abc", ContextFabricEvidenceRefPrefix + "ci", ContextFabricEvidenceRefPrefix + "ci:", ContextFabricEvidenceRefPrefix + "not-a-kind:x"} {
		if RowKeyedEvidenceRef(ref) {
			t.Errorf("RowKeyedEvidenceRef(%q) = true", ref)
		}
	}
}
