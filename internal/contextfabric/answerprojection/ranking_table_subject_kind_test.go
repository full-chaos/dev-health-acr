package answerprojection

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestRankingTableRowKeepsStructuredKeysAndCarriesSubjectKind(t *testing.T) {
	t.Parallel()
	member := contractsv1.ContextFabricCohortMember{
		Subject:         contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: "project_1", Label: "P One"},
		RankingComputed: true, AttentionRank: 1,
	}
	row := rankingTableRow(member)
	for key, want := range map[string]string{"subject_canonical_id": "project_1", "subject_label": "P One", "subject_kind": "project", "team_canonical_id": "project_1", "team_label": "P One"} {
		got, ok := row.Fields[key]
		if !ok || got.String == nil || *got.String != want {
			t.Errorf("field %s = %+v, want %q", key, got, want)
		}
	}
}
