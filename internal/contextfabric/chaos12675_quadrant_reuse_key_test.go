package contextfabric

import "testing"

// CHAOS-12675: the landscape quadrant switch joins the question family reuse
// dimension, so an answer stored while it was off is not reused once it is on
// (and the other way round).
func TestTheReuseFamilyVersionNamesTheQuadrantSwitch(t *testing.T) {
	t.Parallel()
	seen := map[string]RenderShapeOptions{}
	for mask := 0; mask < 16; mask++ {
		options := RenderShapeOptions{InvestmentThemeShare: mask&1 != 0, ComparisonSideBySide: mask&2 != 0, CompletionForecast: mask&4 != 0, LandscapePosition: mask&8 != 0}
		version := ReuseQuestionFamilyVersion(options)
		if len(version) > 128 {
			t.Errorf("%+v: %d characters, past the column's 128", options, len(version))
		}
		if earlier, repeated := seen[version]; repeated {
			t.Errorf("%+v and %+v share the reuse version %q: an answer stored under one would be served under the other", earlier, options, version)
		}
		seen[version] = options
	}
	if got, want := ReuseQuestionFamilyVersion(RenderShapeOptions{LandscapePosition: true}), QuestionFamilyTableVersion+"+render:landscape_position"; got != want {
		t.Fatalf("quadrant on: %q, want %q", got, want)
	}
}
