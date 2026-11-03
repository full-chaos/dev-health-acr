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

// CHAOS-12582: the sankey switch protects a consumer on a pin without the rule,
// so neither door hands it a sankey stored while the switch was on: a by-id read
// and a create served from a stored row (a reuse hit). The treemap switch is on
// in both runs, so only the sankey's own switch decides.
func TestAStoredSankeyIsServedOnlyWithItsSwitchOn(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprintf("enabled=%v", enabled), func(t *testing.T) {
			store := memoryinvestigation.NewStore()
			row := validContextFabricInvestigationResult()
			row.ResultID = "result_stored_sankey1"
			const claim = "claim_stored_sankey1"
			type link struct {
				repository, word, theme string
				effort, total           float64
			}
			links := []link{
				{"repository:aaa", "acme/api", "feature_delivery", 6, 7.5},
				{"repository:bbb", "acme/web", "feature_delivery", 1.5, 7.5},
			}
			fact := v1.ContextFabricClaimedFact{
				ClaimID: claim, Kind: v1.ContextFabricFactInvestment, Field: v1.ContextFabricRenderRepositoryFlowTableField,
				Value:   v1.ContextFabricScalarValue{Null: true},
				Subject: v1.ContextFabricSubjectRef{Kind: v1.ContextFabricSubjectTeam, CanonicalID: "team:platform", Label: "Platform"},
				Table: &v1.ContextFabricClaimedFactTable{
					Field: v1.ContextFabricRenderRepositoryFlowTableField, Shape: v1.ContextFabricFactTableShapeBreakdown,
					Key: []string{"repository", "theme"}, Measures: []string{"weighted_effort", "theme_weighted_effort"}, Observations: []string{"repository_label"},
				},
			}
			series := v1.ContextFabricRenderSeries{Key: "feature_delivery", Label: "Feature Delivery"}
			for index, l := range links {
				repository, word, theme, effort, total := l.repository, l.word, l.theme, l.effort, l.total
				fact.Rows = append(fact.Rows, v1.ContextFabricClaimedFactRow{Fields: map[string]v1.ContextFabricScalarValue{
					"repository": {String: &repository}, "repository_label": {String: &word}, "theme": {String: &theme},
					"weighted_effort": {Number: &effort}, "theme_weighted_effort": {Number: &total},
				}})
				rowIndex := index
				series.Points = append(series.Points, v1.ContextFabricRenderPoint{Label: word, Value: effort, Source: v1.ContextFabricRenderPointSource{
					Kind: v1.ContextFabricRenderSourceClaimedFactRow, ClaimID: claim, RowIndex: &rowIndex, Field: "weighted_effort",
				}})
			}
			row.ClaimedFacts = append(row.ClaimedFacts, fact)
			row.RenderShapes = []v1.ContextFabricRenderShape{{
				ShapeID: "rs_1", Kind: v1.ContextFabricRenderKindSankey, SelectedBy: v1.ContextFabricRenderRuleInvestmentRepositoryFlow,
				Title: "Where the team's effort went, by repository", AxisKind: v1.ContextFabricRenderAxisCategory, AxisLabel: "Repository",
				ValueLabel: "Weighted effort", Series: []v1.ContextFabricRenderSeries{series},
			}}
			row.Completeness.ClaimedFactsCount, row.Completeness.RowsCount = len(row.ClaimedFacts), len(links)
			seedResult3355(t, store, callerOrgID, row)
			app, token := newParityHostedApp(t, investigatorFunc(func(context.Context, storage.Principal, cf.InvestigationRequest) (cf.InvestigationResult, error) {
				return row, nil
			}), store)
			app.config.ContextFabricInvestmentTreemapEnabled = true
			app.config.ContextFabricInvestmentSankeyEnabled = enabled
			serveAsNarrowedUser(t, app)

			recorder := httptest.NewRecorder()
			app.Handler().ServeHTTP(recorder, investigationResultRequest(t, token, row.ResultID))
			if recorder.Code != http.StatusOK {
				t.Fatalf("read: %d %s", recorder.Code, recorder.Body.String())
			}
			if served := strings.Contains(recorder.Body.String(), `"investment_repository_flow"`); served != enabled {
				t.Fatalf("by id: enabled=%v, served the sankey=%v", enabled, served)
			}

			created := httptest.NewRecorder()
			app.Handler().ServeHTTP(created, investigationRequest(t, token))
			if created.Code != http.StatusOK {
				t.Fatalf("create: %d %s", created.Code, created.Body.String())
			}
			if served := strings.Contains(created.Body.String(), `"investment_repository_flow"`); served != enabled {
				t.Fatalf("create: enabled=%v, served the sankey=%v", enabled, served)
			}
		})
	}
}
