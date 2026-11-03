package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cf "github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	v1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12608 (Codex #1949 r1): the API's serving filter never received the
// completion distribution switch, so it removed every forecast shape even with
// the switch on, and left a stored summary on its claim with the switch off.
// Both doors now serve the forecast shape and summary exactly when it is on.
func TestAStoredForecastIsServedOnlyWithTheSwitchOn(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprintf("enabled=%v", enabled), func(t *testing.T) {
			store := memoryinvestigation.NewStore()
			row := validContextFabricInvestigationResult()
			row.ResultID = "result_stored_forecast01"
			p50, p85, horizon := int64(12), int64(30), int64(365)
			claim := "claim_stored_forecast"
			row.ClaimedFacts = append(row.ClaimedFacts, v1.ContextFabricClaimedFact{
				ClaimID: claim, Kind: v1.ContextFabricFactWorkload, Field: v1.ContextFabricForecastSummaryP50Days,
				Subject: v1.ContextFabricSubjectRef{Kind: v1.ContextFabricSubjectTeam, CanonicalID: "team:chaos", Label: "CHAOS"},
				Value:   v1.ContextFabricScalarValue{Integer: &p50},
				CompletionForecast: &v1.ContextFabricClaimedCompletionForecast{
					WorkScopeID: "scope-a", State: v1.ContextFabricCompletionDistributionNotStored, ComputedAt: "2026-11-02T04:00:00Z",
					HorizonDays: &horizon, P50Days: &p50, P85Days: &p85,
				},
			})
			var points []v1.ContextFabricRenderPoint
			for _, field := range []string{v1.ContextFabricForecastSummaryP50Days, v1.ContextFabricForecastSummaryP85Days} {
				word, ok := v1.ContextFabricForecastPercentileLabel(field)
				if !ok {
					t.Fatalf("field %q has no word", field)
				}
				value, _ := row.ClaimedFacts[len(row.ClaimedFacts)-1].CompletionForecast.Number(field)
				points = append(points, v1.ContextFabricRenderPoint{
					Label: word, Value: float64(value),
					Source: v1.ContextFabricRenderPointSource{Kind: v1.ContextFabricRenderSourceClaimedFactForecastSummary, ClaimID: claim, Field: field},
				})
			}
			row.RenderShapes = []v1.ContextFabricRenderShape{{
				ShapeID: "rs_1", Kind: v1.ContextFabricRenderKindTable, SelectedBy: v1.ContextFabricRenderRuleCompletionForecastPercentiles,
				Title: "Completion forecast percentiles", AxisKind: v1.ContextFabricRenderAxisCategory, AxisLabel: "Percentile", ValueLabel: "Days to complete",
				Series: []v1.ContextFabricRenderSeries{{Key: claim, Label: "scope-a", Points: points}},
			}}
			row.Completeness.ClaimedFactsCount = len(row.ClaimedFacts)
			seedResult3355(t, store, callerOrgID, row)
			app, token := newParityHostedApp(t, investigatorFunc(func(context.Context, storage.Principal, cf.InvestigationRequest) (cf.InvestigationResult, error) {
				return row, nil
			}), store)
			app.config.ContextFabricCompletionDistributionEnabled = enabled
			serveAsNarrowedUser(t, app)

			for door, request := range map[string]*http.Request{
				"by id":  investigationResultRequest(t, token, row.ResultID),
				"create": investigationRequest(t, token),
			} {
				recorder := httptest.NewRecorder()
				app.Handler().ServeHTTP(recorder, request)
				if recorder.Code != http.StatusOK {
					t.Fatalf("%s: %d %s", door, recorder.Code, recorder.Body.String())
				}
				body := recorder.Body.String()
				if served := strings.Contains(body, `"completion_forecast_percentiles"`); served != enabled {
					t.Fatalf("%s: enabled=%v, served the table=%v", door, enabled, served)
				}
				if served := strings.Contains(body, `"completion_forecast":{`); served != enabled {
					t.Fatalf("%s: enabled=%v, served the summary=%v", door, enabled, served)
				}
			}
		})
	}
}
