package contextfabric

import (
	"context"
	"slices"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12557 (ruling (c)): a question that names several subjects by their
// identifiers beside one repository, and states no kind, keeps the repository's
// answer; the engine says, after synthesis, that it does not answer for them.
// Only the engine states it.
func TestTheUnansweredHandlesSentenceIsStatedOnTheAnswer(t *testing.T) {
	t.Parallel()
	sentence := contractsv1.ContextFabricSeveralHandlesUnansweredLimitation
	engine, request := engineForDegradationEdited(t, false, []string{}, TimeContext{Axis: TemporalCurrent}, func(resolution *SubjectResolution, _ *GraphContext) {
		resolution.UnansweredHandlesLimitation = sentence
	})
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/alpha"}}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if !slices.Contains(result.Limitations, sentence) {
		t.Fatalf("limitations %q, want the sentence", result.Limitations)
	}
	if !contractsv1.IsContextFabricServiceAuthoredLimitation(sentence) || serviceLimitationRecogniserID(sentence) != "unanswered_handles" {
		t.Fatalf("%q is not recognised as the engine's own sentence", sentence)
	}
	if kept, dropped, _ := stripServiceAuthoredLookalikes([]string{sentence}); dropped != 1 || len(kept) != 0 {
		t.Fatalf("a draft copy was kept: %q", kept)
	}
}
