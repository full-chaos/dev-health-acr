package contextfabric

import (
	"context"
	"slices"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12505 ruling 2: when the keyed link read that scopes a work-item census
// to a requested repository failed or was cut, nothing shows that no work item
// matched. The terminal says the read's sentence, and never "No work item
// matched" or "found no candidate", for every caller class.
func TestAnUnreadLinkScopeNeverReadsAsNoWorkItemMatched(t *testing.T) {
	t.Parallel()
	unread := SubjectResolution{
		Candidates: []SubjectCandidate{}, Committed: []SubjectRef{},
		WorkItemLinkLimitation:  contractsv1.ContextFabricWorkItemLinksUnreadLimitation,
		WorkItemLinkScopeUnread: true,
	}
	readers := map[string]GraphReader{
		"the floor withheld every candidate": flooredGraphReader{authzDroppingGraphReader{resolution: unread}},
		"an empty pool":                      authzDroppingGraphReader{resolution: unread},
	}
	principals := map[string]storage.Principal{
		"organization-wide": {OrgID: "org_1"},
		"restricted":        {OrgID: "org_1", RepositoryScopes: []string{"acme/alpha"}},
	}
	for name, reader := range readers {
		for who, principal := range principals {
			t.Run(name+", "+who, func(t *testing.T) {
				t.Parallel()
				engine := mustEngineForTerminalReasonTest(t, reader, &recordingTelemetry{})
				result, err := engine.Investigate(context.Background(), principal, validInvestigationRequest())
				if err != nil {
					t.Fatalf("Investigate() error = %v", err)
				}
				if result.Status != InvestigationNoMatch {
					t.Fatalf("status = %q, want no_match", result.Status)
				}
				if !slices.Contains(result.Limitations, contractsv1.ContextFabricWorkItemLinksUnreadLimitation) {
					t.Fatalf("limitations %q lack the read's sentence", result.Limitations)
				}
				for _, limitation := range result.Limitations {
					if strings.HasPrefix(limitation, "No ") || limitation == noMatchLimitationOfferFloorEmptied || limitation == noMatchLimitationUnproven {
						t.Fatalf("limitations %q say nothing matched beside a read that decided nothing", result.Limitations)
					}
				}
			})
		}
	}
}

// The control: the same floor without the unread read keeps its sentence, so
// the test above tells the two apart. A withheld pool keeps its own prose with
// the flag, since it is about the search and stays true.
func TestTheFloorAndTheWithheldPoolKeepTheirSentencesOtherwise(t *testing.T) {
	t.Parallel()
	floored := mustEngineForTerminalReasonTest(t, flooredGraphReader{authzDroppingGraphReader{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}}}, &recordingTelemetry{})
	result, err := floored.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	said := false
	for _, limitation := range result.Limitations {
		said = said || strings.HasPrefix(limitation, "No subject matched") || strings.HasPrefix(limitation, "No work item matched")
	}
	if !said {
		t.Fatalf("limitations %q: the floor's sentence is gone without an unread read", result.Limitations)
	}
	request := validInvestigationRequest()
	request.Options.AllowClarification = false
	withheld := mustEngineForTerminalReasonTest(t, authzDroppingGraphReader{resolution: SubjectResolution{
		Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}, ClarificationPrompt: OfferPoolEmptiedClarificationPrompt,
		WorkItemLinkLimitation: contractsv1.ContextFabricWorkItemLinksUnreadLimitation, WorkItemLinkScopeUnread: true,
	}}, &recordingTelemetry{})
	result, err = withheld.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if !slices.Contains(result.Limitations, noMatchLimitationOfferPoolEmptied) || !slices.Contains(result.Limitations, contractsv1.ContextFabricWorkItemLinksUnreadLimitation) {
		t.Fatalf("limitations %q, want the withheld pool's prose and the read's sentence", result.Limitations)
	}
}
