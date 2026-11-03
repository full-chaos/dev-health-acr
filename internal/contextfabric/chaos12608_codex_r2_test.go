package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Codex #1949 r2: reuse is keyed by the render switches that are on, and a
// forecast scope is a pair no texts can forge.

func TestTheReuseFamilyVersionNamesTheRenderSwitchesOn(t *testing.T) {
	if got := ReuseQuestionFamilyVersion(RenderShapeOptions{}); got != QuestionFamilyTableVersion {
		t.Fatalf("every switch off: %q, want the table version: a deployment that turns nothing on keeps its stored answers", got)
	}
	seen := map[string]RenderShapeOptions{}
	for mask := 0; mask < 8; mask++ {
		options := RenderShapeOptions{InvestmentThemeShare: mask&1 != 0, ComparisonSideBySide: mask&2 != 0, CompletionForecast: mask&4 != 0}
		version := ReuseQuestionFamilyVersion(options)
		if len(version) > 128 {
			t.Errorf("%+v: %d characters, past the column's 128", options, len(version))
		}
		if earlier, repeated := seen[version]; repeated {
			t.Errorf("%+v and %+v share the reuse version %q: an answer stored under one would be served under the other", earlier, options, version)
		}
		seen[version] = options
	}
	if got, want := ReuseQuestionFamilyVersion(forecastOn), QuestionFamilyTableVersion+"+render:completion_forecast"; got != want {
		t.Fatalf("forecast on: %q, want %q", got, want)
	}
}

func TestTwoForecastScopesNeverShareAKey(t *testing.T) {
	rows := forecastDistributionRows([]int64{5, 12, 365}, []float64{0.2, 0.8, 0.8})
	first := forecastClaim("claim_forecast_first", rows)
	first.Subject.CanonicalID = "team:a"
	first.CompletionForecast.WorkScopeID = "b\x00team:c\x00x"
	second := forecastClaim("claim_forecast_second", rows)
	second.Subject.CanonicalID = "team:a\x00b"
	second.CompletionForecast.WorkScopeID = "team:c\x00x"
	if forecastScopeOf(first) == forecastScopeOf(second) {
		t.Fatal("two forecasts read as one scope")
	}
	shapes, event := selectRenderShapes(forecastResult(first, second), forecastFrame(ObligationCompletion), forecastOn)
	if reason := forecastSkipOf(t, event); reason != RenderShapeSkipMoreThanOneScope || len(forecastsOf(shapes)) != 0 {
		t.Fatalf("two forecasts: skip = %q, curves = %d; want no curve", reason, len(forecastsOf(shapes)))
	}
	if contractsv1.ContextFabricForecastScopeOf(first.Subject, first.CompletionForecast.WorkScopeID) == contractsv1.ContextFabricForecastScopeOf(second.Subject, second.CompletionForecast.WorkScopeID) {
		t.Fatal("the contract reads two forecasts as one scope")
	}
}
