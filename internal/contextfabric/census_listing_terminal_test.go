package contextfabric

import (
	"context"
	"slices"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12467: a census count never reads as "found nothing", on any arm of the
// empty-pool terminal. Each false sentence is named: the two the ticket's trace
// found, and the floor's.
func TestACountNeverReadsAsNothingFound(t *testing.T) {
	t.Parallel()
	sentence := contractsv1.ContextFabricCensusUnlistedLimitation(10)
	for name, resolution := range map[string]SubjectResolution{
		"an empty pool":          {Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}, CensusLimitation: sentence},
		"the exclusion's prompt": {Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}, CensusLimitation: sentence, ClarificationPrompt: OfferPoolEmptiedClarificationPrompt},
	} {
		for _, allowClarification := range []bool{true, false} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				request := validInvestigationRequest()
				request.Options.AllowClarification = allowClarification
				engine := mustEngineForTerminalReasonTest(t, authzDroppingGraphReader{resolution: resolution}, &recordingTelemetry{})
				result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/alpha"}}, request)
				if err != nil {
					t.Fatalf("Investigate() error = %v", err)
				}
				if result.Status != InvestigationNoMatch {
					t.Fatalf("status = %q, want no_match: nothing is listed to confirm", result.Status)
				}
				if !slices.Contains(result.Limitations, sentence) {
					t.Fatalf("limitations %q lack the count", result.Limitations)
				}
				for _, limitation := range result.Limitations {
					switch limitation {
					case noMatchLimitationUnproven, clarificationRequiredLimitationOne, noMatchLimitationOfferPoolEmptied, noMatchLimitationOfferFloorEmptied:
						t.Fatalf("limitations %q say nothing was found", result.Limitations)
					}
				}
			})
		}
	}
}

// With a listing cut at the bound, the terminal asks and says how many it holds.
func TestAPartListingIsStatedOnTheClarification(t *testing.T) {
	t.Parallel()
	sentence := contractsv1.ContextFabricCensusListedLimitation(10, 2)
	subject := SubjectRef{Kind: SubjectPullRequest, CanonicalID: "pull_request:r:12", Label: "pull request #12 in acme/alpha"}
	other := SubjectRef{Kind: SubjectPullRequest, CanonicalID: "pull_request:s:12", Label: "pull request #12 in acme/beta"}
	candidates := []SubjectCandidate{
		{ReceiptID: "receipt_census_a1", Subject: subject, State: ResolutionAmbiguous, MatchReasons: []string{"census"}, Confidence: 0.5},
		{ReceiptID: "receipt_census_b1", Subject: other, State: ResolutionAmbiguous, MatchReasons: []string{"census"}, Confidence: 0.5},
	}
	engine := mustEngineForTerminalReasonTest(t, authzDroppingGraphReader{resolution: SubjectResolution{Candidates: candidates, Committed: []SubjectRef{}, CensusLimitation: sentence, ClarificationPrompt: "Which one?"}}, &recordingTelemetry{})
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/alpha", "acme/beta"}}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationClarificationRequired {
		t.Fatalf("status = %q", result.Status)
	}
	if !slices.Contains(result.Limitations, sentence) {
		t.Fatalf("limitations %q lack the partial count", result.Limitations)
	}
}

// The terminal reason names the state, so a count is not an empty pool in the
// logs either.
func TestACountIsNotAnEmptyPoolInTheTerminalReason(t *testing.T) {
	t.Parallel()
	resolution := SubjectResolution{CensusLimitation: contractsv1.ContextFabricCensusUnlistedLimitation(10), ClarificationPrompt: OfferPoolEmptiedClarificationPrompt}
	if got := subjectlessTerminalReason(FrameGate{}, resolution, 0, declaredKindDecision{}); got != censusUnlistedTerminalReason {
		t.Fatalf("reason = %q", got)
	}
	if !slices.Contains(SubjectlessTerminalReasonVocabulary(), censusUnlistedTerminalReason) {
		t.Fatalf("%q is outside the vocabulary", censusUnlistedTerminalReason)
	}
}
