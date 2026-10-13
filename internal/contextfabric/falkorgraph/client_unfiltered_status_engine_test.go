package falkorgraph

import (
	"encoding/json"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func clientStatusFrame(t *testing.T, expression map[string]any) *contextfabric.QuestionFrame {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"shape": "discovered_cohort", "requested_judgment": "which repositories are active",
		"requested_subject_kind": "repository", "subject_terms": []string{},
		"time_context":      map[string]any{"axis": "current"},
		"fact_requirements": []map[string]any{{"kind": "identity"}}, "clarification_needed": false,
		"question_frame": map[string]any{"goals": []string{"rank_or_survey"}, "subject_expression": expression},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, capture, err := genkitruntime.ParseInterpretationOutputSignals(raw, contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent})
	if err != nil {
		t.Fatalf("ParseInterpretationOutputSignals: %v", err)
	}
	frame := capture.Frame.Frame
	return &frame
}

// TestAStatusOnAKindWithNoStatusIsServedUnfilteredAndSaysSo: "which
// repositories are active" has no status to filter by. The list is served, and
// the answer says the status was not applied, whichever shape states it.
func TestAStatusOnAKindWithNoStatusIsServedUnfilteredAndSaysSo(t *testing.T) {
	useStatusHarnessKind(t, "repository")
	const want = `The status filter "active" was not applied: repositories have no status.`
	for name, expression := range map[string]map[string]any{
		"discovered_kind": {"kind": "discovered_kind", "member_kind": "repository", "member_qualifier": "status", "member_qualifier_value": "active"},
		"children_of_scope": {"kind": "children_of_scope", "anchor_terms": []string{"Acme"}, "member_kind": "repository",
			"member_qualifier": "status", "member_qualifier_value": "active"},
	} {
		t.Run(name, func(t *testing.T) {
			result, _ := investigatePullRequestsFramed(t, countingSynthesizer{}, organizationPullRequests(6, 3), "active", storage.Principal{OrgID: "org-1"}, name == "children_of_scope",
				func(string) *contextfabric.QuestionFrame { return clientStatusFrame(t, expression) })
			if len(servedCohortIDs(result)) == 0 {
				t.Fatalf("served no members (status %q, limitations %q)", result.Status, result.Limitations)
			}
			for _, l := range result.Limitations {
				if l == want {
					return
				}
			}
			t.Fatalf("limitations %q lack %q", result.Limitations, want)
		})
	}
}
