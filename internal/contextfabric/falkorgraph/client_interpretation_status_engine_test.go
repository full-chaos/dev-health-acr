package falkorgraph

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// clientInterpretationFrame is the frame the service builds from the JSON a
// client model sends for a question that names no organization.
func clientInterpretationFrame(t *testing.T, status string) *contextfabric.QuestionFrame {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"shape": "discovered_cohort", "requested_judgment": "which pull requests are " + status,
		"requested_subject_kind": "pull_request", "subject_terms": []string{},
		"time_context":      map[string]any{"axis": "current"},
		"fact_requirements": []map[string]any{{"kind": "pull_requests"}}, "clarification_needed": false,
		"question_frame": map[string]any{
			"goals": []string{"rank_or_survey"},
			"subject_expression": map[string]any{
				"kind": "discovered_kind", "member_kind": "pull_request",
				"member_qualifier": "status", "member_qualifier_value": status,
			},
		},
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

// TestAClientInterpretationWithoutAnOrganizationAppliesItsStatus runs the JSON
// a client sends through the real parse, engine and adapter.
func TestAClientInterpretationWithoutAnOrganizationAppliesItsStatus(t *testing.T) {
	pulls := organizationPullRequests(30, 20)
	served := map[string]int{}
	for _, status := range []string{"awaiting_review", "merged"} {
		result, _ := investigatePullRequestsFramed(t, countingSynthesizer{}, pulls, status, storage.Principal{OrgID: "org-1"}, false,
			func(string) *contextfabric.QuestionFrame { return clientInterpretationFrame(t, status) })
		served[status] = len(servedCohortIDs(result))
		named := false
		for _, l := range result.Limitations {
			if strings.Contains(l, contextfabric.PullRequestStatusFilterDisclosure(status)) {
				named = true
			}
		}
		if !named {
			t.Errorf("%s: no limitation names the status filter: %q", status, result.Limitations)
		}
	}
	if served["awaiting_review"] == served["merged"] {
		t.Errorf("awaiting_review and merged both served %d pull requests, want different sets", served["merged"])
	}
}
