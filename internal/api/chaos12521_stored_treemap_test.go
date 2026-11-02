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

// CHAOS-12521: the flag protects a consumer on a pin without the rule, so a by-id
// read must not hand that consumer a treemap stored while the flag was on.
func TestAStoredTreemapIsServedOnlyWithTheFlagOn(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprintf("enabled=%v", enabled), func(t *testing.T) {
			store := memoryinvestigation.NewStore()
			row := validContextFabricInvestigationResult()
			row.ResultID = "result_stored_treemap1"
			themes := []string{"feature_delivery", "operational", "maintenance", "quality", "risk"}
			half, none := 0.5, 0.0
			row.ClaimedFacts = append(row.ClaimedFacts, v1.ContextFabricClaimedFact{
				ClaimID: "claim_stored_treemap1", Kind: v1.ContextFabricFactInvestment, Field: "attribution_basis",
				Subject: v1.ContextFabricSubjectRef{Kind: v1.ContextFabricSubjectRepository, CanonicalID: "repo:one", Label: "one"},
				Value:   v1.ContextFabricScalarValue{String: &themes[0]},
				Table:   &v1.ContextFabricClaimedFactTable{Field: "theme_breakdown", Shape: v1.ContextFabricFactTableShapeBreakdown, Key: []string{"theme"}, Measures: []string{"share"}},
				Rows: []v1.ContextFabricClaimedFactRow{
					{Fields: map[string]v1.ContextFabricScalarValue{"theme": {String: &themes[0]}, "share": {Number: &half}}},
					{Fields: map[string]v1.ContextFabricScalarValue{"theme": {String: &themes[1]}, "share": {Number: &half}}},
					{Fields: map[string]v1.ContextFabricScalarValue{"theme": {String: &themes[2]}, "share": {Number: &none}}},
					{Fields: map[string]v1.ContextFabricScalarValue{"theme": {String: &themes[3]}, "share": {Number: &none}}},
					{Fields: map[string]v1.ContextFabricScalarValue{"theme": {String: &themes[4]}, "share": {Number: &none}}},
				},
			})
			first, second, third, fourth, fifth := 0, 1, 2, 3, 4
			citing := func(index *int) v1.ContextFabricRenderPointSource {
				return v1.ContextFabricRenderPointSource{Kind: v1.ContextFabricRenderSourceClaimedFactRow, ClaimID: "claim_stored_treemap1", RowIndex: index, Field: "share"}
			}
			row.RenderShapes = []v1.ContextFabricRenderShape{{
				ShapeID: "rs_1", Kind: v1.ContextFabricRenderKindTreemap, SelectedBy: v1.ContextFabricRenderRuleInvestmentThemeShare,
				Title: "Effort share by theme", AxisKind: v1.ContextFabricRenderAxisCategory, AxisLabel: "Theme", ValueLabel: "Share of effort (ratio)",
				ValueUnit: v1.ContextFabricRenderValueUnitRatio,
				Series: []v1.ContextFabricRenderSeries{{Key: "share", Label: "Share of effort", Points: []v1.ContextFabricRenderPoint{
					{Label: "Feature Delivery", Value: half, Source: citing(&first)},
					{Label: "Operational / Support", Value: half, Source: citing(&second)},
					{Label: "Maintenance / Tech Debt", Value: none, Source: citing(&third)},
					{Label: "Quality / Reliability", Value: none, Source: citing(&fourth)},
					{Label: "Risk / Security", Value: none, Source: citing(&fifth)},
				}}},
			}}
			row.Completeness.ClaimedFactsCount, row.Completeness.RowsCount = len(row.ClaimedFacts), 5
			seedResult3355(t, store, callerOrgID, row)
			app, token := newParityHostedApp(t, investigatorFunc(func(context.Context, storage.Principal, cf.InvestigationRequest) (cf.InvestigationResult, error) {
				// The create door serves what the engine returns, here a stored row
				// as a reuse hit would. No reuse gate runs in this test.
				return row, nil
			}), store)
			app.config.ContextFabricInvestmentTreemapEnabled = enabled
			serveAsNarrowedUser(t, app)

			recorder := httptest.NewRecorder()
			app.Handler().ServeHTTP(recorder, investigationResultRequest(t, token, row.ResultID))
			if recorder.Code != http.StatusOK {
				t.Fatalf("read: %d %s", recorder.Code, recorder.Body.String())
			}
			if served := strings.Contains(recorder.Body.String(), `"investment_theme_share"`); served != enabled {
				t.Fatalf("by id: enabled=%v, served the treemap=%v", enabled, served)
			}

			created := httptest.NewRecorder()
			app.Handler().ServeHTTP(created, investigationRequest(t, token))
			if created.Code != http.StatusOK {
				t.Fatalf("create: %d %s", created.Code, created.Body.String())
			}
			if served := strings.Contains(created.Body.String(), `"investment_theme_share"`); served != enabled {
				t.Fatalf("create: enabled=%v, served the treemap=%v", enabled, served)
			}
		})
	}
}
