package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// A question whose subject hint names an inactive team is answered by the real
// investigate_question tool as that team being inactive, with the active team
// of the same name named by id only; nothing is committed and no fact is read.
func TestInvestigateQuestionAnswersAnInactiveTeamHintWithTheActiveTwinAsAPointer(t *testing.T) {
	rig := newServedSentencesRig(t, servedSentencesScenario{
		frame: servedSentencesCountFrame(contextfabric.SubjectRepository),
		resolution: contextfabric.SubjectResolution{
			Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{},
			InactiveHints: []contractsv1.ContextFabricInactiveHint{{
				Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:platform", Label: "Platform"},
				Answer:  contractsv1.TeamInactiveReason, ActiveCanonicalID: "team:jira:platform",
			}},
		},
		bases: contextfabric.CommitBasisSet{},
		facts: servedSentencesFacts{capabilities: servedSentencesCapabilities()},
	})
	answer := rig.ask(t, "How is team Platform doing?", contractsv1.ContextFabricRelativeWindowTrailing30D)
	var served struct {
		Structured struct {
			Status        string `json:"status"`
			Limitations   []string
			InactiveHints []struct {
				Subject           struct{ Kind, CanonicalID string }
				Answer            string `json:"answer"`
				ActiveCanonicalID string `json:"active_canonical_id"`
			} `json:"inactive_hints"`
			SubjectReceipts []json.RawMessage `json:"subject_receipts"`
		} `json:"structured"`
	}
	if err := json.Unmarshal(answer.structured, &served); err != nil {
		t.Fatal(err)
	}
	hints := served.Structured.InactiveHints
	if len(hints) != 1 || hints[0].Answer != "team_inactive" || hints[0].ActiveCanonicalID != "team:jira:platform" {
		t.Fatalf("served inactive_hints = %+v\nstructured = %s", hints, answer.structured)
	}
	if served.Structured.Status != "no_match" || len(served.Structured.SubjectReceipts) != 0 {
		t.Fatalf("status=%q receipts=%d, want no_match with no subject bound", served.Structured.Status, len(served.Structured.SubjectReceipts))
	}
	var limitations []string
	if raw, ok := answer.field(t, "structured", "limitations").([]any); ok {
		for _, l := range raw {
			limitations = append(limitations, l.(string))
		}
	}
	found := false
	for _, l := range limitations {
		found = found || l == contractsv1.ContextFabricInactiveTeamHintLimitation
	}
	if !found {
		t.Fatalf("limitations = %v, want the inactive-team sentence", limitations)
	}
	if text := answer.servedText(t); !strings.Contains(text, "inactive") {
		t.Fatalf("served answer text %q does not say the team is inactive", text)
	}
}
