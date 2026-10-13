package answerprojection

import (
	"encoding/json"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestServedProjectRankingRowCarriesSubjectKeysAndDeprecatedTeamKeys(t *testing.T) {
	t.Parallel()
	result := canonicalWithScoreShape(46.7)
	result.RenderShapes = nil
	subject := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: "project:p1", Label: "Payments"}
	result.Cohort.Kind = contractsv1.ContextFabricSubjectProject
	result.Cohort.Members[0].Subject = subject
	projection := Project(result, Budget{})
	if err := projection.Validate(); err != nil {
		t.Fatalf("projection invalid: %v", err)
	}
	if len(projection.Cohort.RankingTable) != 1 {
		t.Fatalf("ranking rows = %d", len(projection.Cohort.RankingTable))
	}
	fields := projection.Cohort.RankingTable[0].Fields
	for key, want := range map[string]string{
		"subject_canonical_id": "project:p1", "team_canonical_id": "project:p1",
		"subject_label": "Payments", "team_label": "Payments", "subject_kind": "project",
	} {
		if got := fields[key]; got.String == nil || *got.String != want {
			t.Errorf("%s = %+v, want %q", key, got, want)
		}
	}
	payload, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	if err := contractcheck.ValidateSerializedStrict("", "context_fabric_answer_projection.v1.schema.json", payload); err != nil {
		t.Fatalf("served payload fails the strict producer check: %v", err)
	}
}
