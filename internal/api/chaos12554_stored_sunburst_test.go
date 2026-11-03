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

// storedSunburst is a claim carrying a subcategory_breakdown table and the
// sunburst that plots it, as an answer stored with the switch on holds them.
func storedSunburst(claimID string) (v1.ContextFabricClaimedFact, v1.ContextFabricRenderShape) {
	shares := map[string]float64{"feature_delivery.customer": 0.4, "quality.bugfix": 0.35, "risk.security": 0.25}
	vocabulary := v1.ContextFabricInvestmentSubcategoryVocabulary()
	totals := map[string]float64{}
	for _, subcategory := range vocabulary {
		theme, _ := v1.ContextFabricInvestmentSubcategoryTheme(subcategory)
		totals[theme] += shares[subcategory]
	}
	claim := v1.ContextFabricClaimedFact{
		ClaimID: claimID, Kind: v1.ContextFabricFactInvestment, Field: "subcategory_breakdown", Value: v1.ContextFabricScalarValue{Null: true},
		Subject: v1.ContextFabricSubjectRef{Kind: v1.ContextFabricSubjectRepository, CanonicalID: "repo:one", Label: "one"},
		Table: &v1.ContextFabricClaimedFactTable{Field: "subcategory_breakdown", Shape: v1.ContextFabricFactTableShapeBreakdown, Key: []string{"subcategory"},
			Measures: []string{"share", "theme_share"}, Observations: []string{"theme"}},
	}
	inner := v1.ContextFabricRenderSeries{Key: "themes", Label: "Theme share, sum of subcategories"}
	rings := map[string]*v1.ContextFabricRenderSeries{}
	var order []string
	for index, subcategory := range vocabulary {
		theme, _ := v1.ContextFabricInvestmentSubcategoryTheme(subcategory)
		key, token, share, total, row := subcategory, theme, shares[subcategory], totals[theme], index
		claim.Rows = append(claim.Rows, v1.ContextFabricClaimedFactRow{Fields: map[string]v1.ContextFabricScalarValue{
			"subcategory": {String: &key}, "theme": {String: &token}, "share": {Number: &share}, "theme_share": {Number: &total},
		}})
		word, _ := v1.ContextFabricInvestmentThemeLabel(theme)
		ring, known := rings[theme]
		if !known {
			order = append(order, theme)
			ring = &v1.ContextFabricRenderSeries{Key: theme, Label: word}
			rings[theme] = ring
			inner.Points = append(inner.Points, v1.ContextFabricRenderPoint{Label: word, Value: total, Source: v1.ContextFabricRenderPointSource{
				Kind: v1.ContextFabricRenderSourceClaimedFactRow, ClaimID: claimID, RowIndex: &row, Field: "theme_share"}})
		}
		label, _ := v1.ContextFabricVocabularyLabel(v1.ContextFabricVocabularySubcategory, subcategory)
		ring.Points = append(ring.Points, v1.ContextFabricRenderPoint{Label: label, Value: share, Source: v1.ContextFabricRenderPointSource{
			Kind: v1.ContextFabricRenderSourceClaimedFactRow, ClaimID: claimID, RowIndex: &row, Field: "share"}})
	}
	series := []v1.ContextFabricRenderSeries{inner}
	for _, theme := range order {
		series = append(series, *rings[theme])
	}
	return claim, v1.ContextFabricRenderShape{
		ShapeID: "rs_1", Kind: v1.ContextFabricRenderKindSunburst, SelectedBy: v1.ContextFabricRenderRuleInvestmentSubcategoryShare,
		Title: "Effort share by theme and subcategory", AxisKind: v1.ContextFabricRenderAxisCategory, AxisLabel: "Subcategory",
		ValueLabel: "Share of effort (ratio)", ValueUnit: v1.ContextFabricRenderValueUnitRatio, Series: series,
	}
}

// CHAOS-12554: the switch protects a consumer on a pin without the rule, so
// neither door hands it a sunburst stored while the switch was on.
func TestAStoredSunburstIsServedOnlyWithTheSwitchOn(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprintf("enabled=%v", enabled), func(t *testing.T) {
			store := memoryinvestigation.NewStore()
			row := validContextFabricInvestigationResult()
			row.ResultID = "result_stored_sunburst1"
			claim, shape := storedSunburst("claim_stored_sunburst1")
			row.ClaimedFacts = append(row.ClaimedFacts, claim)
			row.RenderShapes = []v1.ContextFabricRenderShape{shape}
			row.Completeness.ClaimedFactsCount, row.Completeness.RowsCount = len(row.ClaimedFacts), len(claim.Rows)
			if err := v1.ValidateRenderShapesForResult(row); err != nil {
				t.Fatalf("the stored sunburst is not valid: %v", err)
			}
			seedResult3355(t, store, callerOrgID, row)
			app, token := newParityHostedApp(t, investigatorFunc(func(context.Context, storage.Principal, cf.InvestigationRequest) (cf.InvestigationResult, error) {
				return row, nil
			}), store)
			app.config.ContextFabricInvestmentSunburstEnabled = enabled
			serveAsNarrowedUser(t, app)

			recorder := httptest.NewRecorder()
			app.Handler().ServeHTTP(recorder, investigationResultRequest(t, token, row.ResultID))
			if recorder.Code != http.StatusOK {
				t.Fatalf("read: %d %s", recorder.Code, recorder.Body.String())
			}
			if served := strings.Contains(recorder.Body.String(), `"investment_subcategory_share"`); served != enabled {
				t.Fatalf("by id: enabled=%v, served the sunburst=%v", enabled, served)
			}
			created := httptest.NewRecorder()
			app.Handler().ServeHTTP(created, investigationRequest(t, token))
			if created.Code != http.StatusOK {
				t.Fatalf("create: %d %s", created.Code, created.Body.String())
			}
			if served := strings.Contains(created.Body.String(), `"investment_subcategory_share"`); served != enabled {
				t.Fatalf("create: enabled=%v, served the sunburst=%v", enabled, served)
			}
		})
	}
}
