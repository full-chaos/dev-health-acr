package contextfabric

import (
	"encoding/json"
	"testing"
)

// A grouped list that states a status has no status filter to run: the answer
// says the status was not applied. The frame is the JSON a client's
// interpretation yields.
func TestAStatusOnGroupedMembersIsDisclosedAsNotApplied(t *testing.T) {
	var frame QuestionFrame
	raw := `{"goals":["rank_or_survey"],"subject_expression":{"kind":"grouped_members","grouped":{"group_kind":"team","member_kind":"repository","member_qualifier":"status","member_qualifier_value":"active"}}}`
	if err := json.Unmarshal([]byte(raw), &frame); err != nil {
		t.Fatal(err)
	}
	result := withMemberStatusDisclosure(InvestigationResult{Status: InvestigationComplete}, &frame)
	const want = `The status filter "active" was not applied: repositories have no status.`
	for _, l := range result.Limitations {
		if l == want {
			return
		}
	}
	t.Fatalf("limitations %q lack %q", result.Limitations, want)
}
