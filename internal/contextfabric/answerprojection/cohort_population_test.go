package answerprojection

import (
	"fmt"
	"strings"
	"testing"

	v1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestProjectionOfACappedTeamCohortKeepsThePopulationAndTheCutSentence(t *testing.T) {
	result := richResult()
	if result.Cohort == nil || len(result.Cohort.Members) == 0 {
		t.Fatal("fixture has no cohort")
	}
	result.Cohort.Kind = v1.ContextFabricSubjectTeam
	listed := len(result.Cohort.Members)
	result.Cohort.Population = listed + 8
	result.Cohort.Complete, result.Cohort.Truncated = false, true
	sentence, ok := v1.ContextFabricWorkItemListedLimitation(listed, listed+8, false, v1.ContextFabricWorkItemListCutRequest, "")
	if !ok {
		t.Fatal("no sentence")
	}
	result.Limitations = []string{sentence}
	projection := Project(result, DefaultBudget)
	if projection.Cohort == nil || projection.Cohort.Population != listed+8 {
		t.Fatalf("projected population = %+v, want %d", projection.Cohort, listed+8)
	}
	want := fmt.Sprintf("%d of %d members are listed", len(projection.Cohort.Members), listed+8)
	found := 0
	for _, limitation := range projection.Limitations {
		if v1.IsContextFabricWorkItemListedLimitation(limitation) {
			found++
			if !strings.Contains(limitation, want) {
				t.Errorf("sentence %q lacks %q", limitation, want)
			}
		}
	}
	if found != 1 {
		t.Errorf("limitations %q carry %d N of M sentences, want 1", projection.Limitations, found)
	}
}
