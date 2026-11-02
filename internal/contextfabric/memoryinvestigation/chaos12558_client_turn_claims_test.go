package memoryinvestigation_test

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// clientTurn stamps a result as one call of a client synthesis: the input turn
// (no answer) or the answer turn the caller wrote.
func clientTurn(result contextfabric.InvestigationResult, answered bool) contextfabric.InvestigationResult {
	result.Versions.SynthesisSource = contextfabric.SynthesisSourceClient
	result.Versions.SynthesisVersion = contextfabric.SynthesisVersionNotSynthesized
	if answered {
		result.Versions.SynthesisVersion = "context-fabric-synthesis.v29"
	}
	return result
}

// CHAOS-12558: a caller that writes its own answer makes two calls with the same
// receipt. The input turn claims nothing, the answer turn claims the offer, and a
// second redemption of that offer is still refused, for every member.
func TestAClientSynthesisTurnRedeemsItsOfferOnTheAnswerCall(t *testing.T) {
	ctx := context.Background()
	store := memoryinvestigation.NewStore()
	principal := storage.Principal{OrgID: "org-client-turn"}
	members := contractsv1.ContextFabricStructureNeedKindVocabulary()
	if len(members) < 5 {
		t.Fatalf("%d members, want all five", len(members))
	}
	save := func(result contextfabric.InvestigationResult) error {
		return store.Save(ctx, principal, result, nil, nil, "unkeyed", contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0, "", contextfabric.SemanticStateAbsent(contextfabric.SemanticStateAbsenceTurnEndedBeforeInterpretation))
	}
	for _, member := range members {
		t.Run(string(member), func(t *testing.T) {
			prior := "result-client-turn-offer-" + string(member)
			input := clientTurn(resultWithConfirmedStructure("result-client-input-"+string(member), member, prior, "kindr_client00000001"), false)
			if err := save(input); err != nil {
				t.Fatalf("save input turn: %v", err)
			}
			if held, err := store.IsStructureSuperseded(ctx, principal.OrgID, prior, member); err != nil || held {
				t.Fatalf("after the input turn: superseded = %v, err = %v, want the offer still open", held, err)
			}
			answer := clientTurn(resultWithConfirmedStructure("result-client-answer-"+string(member), member, prior, "kindr_client00000001"), true)
			if err := save(answer); err != nil {
				t.Fatalf("save answer turn: %v", err)
			}
			if held, err := store.IsStructureSuperseded(ctx, principal.OrgID, prior, member); err != nil || !held {
				t.Fatalf("after the answer turn: superseded = %v, err = %v, want the offer claimed", held, err)
			}
			again := clientTurn(resultWithConfirmedStructure("result-client-again-"+string(member), member, prior, "kindr_client00000001"), true)
			var conflict *contextfabric.ErrStructureOfferSuperseded
			if err := save(again); !errors.As(err, &conflict) {
				t.Fatalf("a second answer redeeming the same offer: err = %v, want ErrStructureOfferSuperseded", err)
			}
		})
	}
}
