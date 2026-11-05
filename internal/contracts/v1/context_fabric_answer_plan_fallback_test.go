package v1

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func validFallbackPlan() ContextFabricAnswerPlan {
	return ContextFabricAnswerPlan{
		Family: ContextFabricQuestionFamilyDiscoveredCohortRanking, FamilySource: ContextFabricQuestionFamilySourceModel,
		FamilyVersion: "v1", RequireRanking: true, FactKinds: []ContextFabricFactKind{ContextFabricFactBlockers, ContextFabricFactMetrics},
		Fallback: &ContextFabricAnswerPlanFallback{
			Reason:       ContextFabricReadFallbackReasonJudgmentUnservable,
			PlannedKinds: []ContextFabricFactKind{ContextFabricFactMetrics},
			Outcome:      ContextFabricReadFallbackOutcomeApplied,
		},
	}
}

// TestAnswerPlanFallbackValidates: a producer writes only known members, a
// non-empty kind list and no repeated kind; a reader of a stored document
// tolerates a well-formed token it does not know.
func TestAnswerPlanFallbackValidates(t *testing.T) {
	t.Parallel()
	if err := validFallbackPlan().Validate(); err != nil {
		t.Fatalf("valid fallback plan refused: %v", err)
	}
	withoutFallback := validFallbackPlan()
	withoutFallback.Fallback = nil
	if err := withoutFallback.Validate(); err != nil {
		t.Fatalf("a plan with no fallback refused: %v", err)
	}
	tooMany := make([]ContextFabricFactKind, 0, ContextFabricFactKindListMaxItems+1)
	for i := range ContextFabricFactKindListMaxItems + 1 {
		tooMany = append(tooMany, ContextFabricFactKind(fmt.Sprintf("kind_%d", i)))
	}
	for _, tc := range []struct {
		name     string
		mutate   func(*ContextFabricAnswerPlanFallback)
		wantRead bool
	}{
		{name: "empty reason", mutate: func(f *ContextFabricAnswerPlanFallback) { f.Reason = "" }},
		{name: "unknown reason", mutate: func(f *ContextFabricAnswerPlanFallback) { f.Reason = "family_tired" }, wantRead: true},
		{name: "empty outcome", mutate: func(f *ContextFabricAnswerPlanFallback) { f.Outcome = "" }},
		{name: "unknown outcome", mutate: func(f *ContextFabricAnswerPlanFallback) { f.Outcome = "replanned" }, wantRead: true},
		{name: "no planned kinds", mutate: func(f *ContextFabricAnswerPlanFallback) { f.PlannedKinds = nil }},
		{name: "unknown planned kind", mutate: func(f *ContextFabricAnswerPlanFallback) { f.PlannedKinds = []ContextFabricFactKind{"cycle_time"} }, wantRead: true},
		{name: "malformed planned kind", mutate: func(f *ContextFabricAnswerPlanFallback) { f.PlannedKinds = []ContextFabricFactKind{"cycle time"} }},
		{name: "repeated planned kind", mutate: func(f *ContextFabricAnswerPlanFallback) {
			f.PlannedKinds = []ContextFabricFactKind{ContextFabricFactMetrics, ContextFabricFactMetrics}
		}},
		{name: "more planned kinds than a list may hold", mutate: func(f *ContextFabricAnswerPlanFallback) { f.PlannedKinds = slices.Clone(tooMany) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			plan := validFallbackPlan()
			tc.mutate(plan.Fallback)
			err := plan.Validate()
			if err == nil {
				t.Fatal("a producer wrote it")
			}
			if !strings.Contains(err.Error(), "fallback") {
				t.Fatalf("error %q does not name the fallback", err)
			}
			if readErr := plan.validate(contextFabricLegacyBounds); (readErr == nil) != tc.wantRead {
				t.Fatalf("read validation = %v, want accepted %v", readErr, tc.wantRead)
			}
		})
	}
}

func TestReadFallbackLimitationIsServiceAuthored(t *testing.T) {
	t.Parallel()
	if !IsContextFabricServiceAuthoredLimitation(ContextFabricReadFallbackLimitation) {
		t.Fatal("the read fallback sentence is not recognised as service-authored")
	}
}
