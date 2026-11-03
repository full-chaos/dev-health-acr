package contextfabric

import (
	"context"
	"fmt"
	"slices"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12663 ruling 2: a term read by case over a same-name twin is stated on
// the answer, one service sentence per term. The engine states it after
// synthesis, so it stands on no model; a full list displaces a model caveat
// rather than the sentence, and a draft copy of it is not served.
func TestCHAOS12663_TheCaseTwinReadingIsStatedOnTheAnswer(t *testing.T) {
	t.Parallel()
	gitlab := contractsv1.ContextFabricCaseTwinSubject{Kind: SubjectTeam, Label: "fullchaos", Provider: "gitlab"}
	linear := contractsv1.ContextFabricCaseTwinSubject{Kind: SubjectTeam, Label: "Fullchaos", Provider: "linear"}
	sentences := []string{
		contractsv1.ContextFabricCaseTwinReadingLimitation("Fullchaos", linear, []contractsv1.ContextFabricCaseTwinSubject{gitlab}),
		contractsv1.ContextFabricCaseTwinReadingLimitation("fullchaos", gitlab, []contractsv1.ContextFabricCaseTwinSubject{linear}),
	}
	full := make([]string, 0, contractsv1.ContextFabricLimitationsMaxCount)
	for i := 0; i < contractsv1.ContextFabricLimitationsMaxCount; i++ {
		full = append(full, fmt.Sprintf("Model caveat %d.", i))
	}
	for name, draft := range map[string][]string{"room to spare": {}, "a full list": full} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			engine, request := engineForDegradationEdited(t, false, draft, TimeContext{Axis: TemporalCurrent}, func(resolution *SubjectResolution, _ *GraphContext) {
				resolution.CaseTwinReadingLimitations = sentences
			})
			result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
			if err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			for _, sentence := range sentences {
				if !slices.Contains(result.Limitations, sentence) {
					t.Fatalf("limitations %q, want %q", result.Limitations, sentence)
				}
			}
			if len(draft) == contractsv1.ContextFabricLimitationsMaxCount && result.LimitationsDisplaced < len(sentences) {
				t.Fatalf("limitations_displaced = %d, want each sentence to have displaced a model caveat", result.LimitationsDisplaced)
			}
		})
	}
	for _, sentence := range sentences {
		if got := serviceLimitationRecogniserID(sentence); got != "case_twin_reading" {
			t.Fatalf("%q is recognised as %q, want the engine's own case-twin sentence", sentence, got)
		}
		if kept, dropped, _ := stripServiceAuthoredLookalikes([]string{sentence}); dropped != 1 || len(kept) != 0 {
			t.Fatalf("a draft copy of %q was kept: %q", sentence, kept)
		}
	}
}
