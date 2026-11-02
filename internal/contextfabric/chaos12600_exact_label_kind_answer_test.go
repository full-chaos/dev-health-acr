package contextfabric

import (
	"context"
	"slices"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12600: a comparison whose kind was resolved from unique exact
// full-label matches says how it read the sides. The engine states the
// sentence after synthesis, so it stands on no model, and it is a service
// sentence a model draft cannot copy.
func TestTheExactLabelKindIsStatedOnTheAnswer(t *testing.T) {
	t.Parallel()
	for _, kind := range []SubjectKind{SubjectRepository, SubjectProject, SubjectTeam} {
		sentence := contractsv1.ContextFabricExactLabelKindLimitation(kind)
		if sentence == "" {
			t.Fatalf("%s has no sentence", kind)
		}
		engine, request := engineForDegradationEdited(t, false, []string{}, TimeContext{Axis: TemporalCurrent}, func(resolution *SubjectResolution, _ *GraphContext) {
			resolution.ExactLabelKind = kind
		})
		result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
		if err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		if !slices.Contains(result.Limitations, sentence) {
			t.Fatalf("limitations %q, want %q", result.Limitations, sentence)
		}
		if !contractsv1.IsContextFabricServiceAuthoredLimitation(sentence) || serviceLimitationRecogniserID(sentence) != "exact_label_kind" {
			t.Fatalf("%q is not recognised as the engine's own exact-label kind sentence", sentence)
		}
		if kept, dropped, _ := stripServiceAuthoredLookalikes([]string{sentence}); dropped != 1 || len(kept) != 0 {
			t.Fatalf("a draft copy of %q was kept: %q", sentence, kept)
		}
	}
	// No kind, no sentence: a stated or unresolved comparison says nothing.
	engine, request := engineForDegradationEdited(t, false, []string{}, TimeContext{Axis: TemporalCurrent}, func(*SubjectResolution, *GraphContext) {})
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	for _, limitation := range result.Limitations {
		if contractsv1.IsContextFabricExactLabelKindLimitation(limitation) {
			t.Fatalf("a turn with no exact-label kind stated %q", limitation)
		}
	}
	if contractsv1.ContextFabricExactLabelKindLimitation(SubjectPullRequest) != "" {
		t.Fatal("the rule resolves only repository, project and team; a pull request kind has no sentence")
	}
}
