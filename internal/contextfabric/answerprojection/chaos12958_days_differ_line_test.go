package answerprojection

import (
	"fmt"
	"slices"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12958: the refusal's line is the answer's only word about the missing
// quadrant, so a limitation cap never cuts it, with or without a quadrant.
func TestTheDaysDifferLineSurvivesTheLimitationCaps(t *testing.T) {
	t.Parallel()
	line := contractsv1.ContextFabricLandscapeDaysDifferLimitation([]string{"Ops Team Oct 7, 2026", "Fullchaos Nov 4, 2026"}, "Oct 7, 2026")
	for name, tc := range map[string]struct {
		count, size int
		budget      Budget
	}{
		"past the count cap": {contractsv1.ContextFabricProjectedNarrativeMaxCount, 40, Budget{}},
		"past the byte cap":  {60, 400, Budget{MaxSerializedBytes: 64 * 1024}},
	} {
		result := canonicalWithQuadrant(t)
		result.RenderShapes = nil
		caveats := make([]string, 0, tc.count+1)
		for index := range tc.count {
			caveat := fmt.Sprintf("Model caveat %03d about this answer.", index)
			for len(caveat) < tc.size {
				caveat += " More words of the same caveat."
			}
			caveats = append(caveats, caveat)
		}
		result.Limitations = append(caveats, line)
		projection := Project(result, tc.budget)
		if !slices.Contains(projection.Limitations, line) {
			t.Errorf("%s: the refusal line was cut (%d limitations kept)", name, len(projection.Limitations))
		}
		if err := projection.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
