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

// CHAOS-12626: the flag protects a consumer on a pin without the rule, so neither
// door may hand it a quadrant, or the quadrant's line, stored while the flag was
// on.
func TestAStoredQuadrantIsServedOnlyWithTheFlagOn(t *testing.T) {
	line := v1.ContextFabricLandscapePositionLimitation
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprintf("enabled=%v", enabled), func(t *testing.T) {
			store := memoryinvestigation.NewStore()
			row := validContextFabricInvestigationResult()
			row.ResultID = "result_stored_quadrant01"
			xWord, xOK := v1.ContextFabricVocabularyLabel(v1.ContextFabricVocabularyFactField, "churn_loc_30d")
			yWord, yOK := v1.ContextFabricVocabularyLabel(v1.ContextFabricVocabularyFactField, "delivery_units_30d")
			if !xOK || !yOK {
				t.Fatal("a field has no word")
			}
			x := v1.ContextFabricRenderSeries{Key: "churn_loc_30d", Label: xWord}
			y := v1.ContextFabricRenderSeries{Key: "delivery_units_30d", Label: yWord}
			table := &v1.ContextFabricClaimedFactTable{
				Field: "area_breakdown", Shape: v1.ContextFabricFactTableShapeBreakdown, Key: []string{"as_of_day"},
				Measures: []string{"identity_count", "churn_loc_30d", "delivery_units_30d"},
			}
			for index, name := range []string{"one", "two"} {
				day, identities, churn, units := "2026-11-02", int64(3), int64(900+index), int64(12+index)
				subject := v1.ContextFabricSubjectRef{Kind: v1.ContextFabricSubjectTeam, CanonicalID: "team:" + name, Label: name}
				rows := []v1.ContextFabricClaimedFactRow{{Fields: map[string]v1.ContextFabricScalarValue{
					"as_of_day": {String: &day}, "identity_count": {Integer: &identities},
					"churn_loc_30d": {Integer: &churn}, "delivery_units_30d": {Integer: &units},
				}}}
				for _, measure := range []struct {
					field  string
					value  *int64
					series *v1.ContextFabricRenderSeries
				}{{"churn_loc_30d", &churn, &x}, {"delivery_units_30d", &units, &y}} {
					claim := "claim_stored_quadrant_" + name + "_" + measure.field
					row.ClaimedFacts = append(row.ClaimedFacts, v1.ContextFabricClaimedFact{
						ClaimID: claim, Kind: v1.ContextFabricFactLandscape, Field: measure.field, Subject: subject,
						Value: v1.ContextFabricScalarValue{Integer: measure.value}, Rows: rows, Table: table,
					})
					measure.series.Points = append(measure.series.Points, v1.ContextFabricRenderPoint{
						Label: name, Value: float64(*measure.value),
						Source: v1.ContextFabricRenderPointSource{Kind: v1.ContextFabricRenderSourceClaimedFactValue, ClaimID: claim, Field: measure.field},
					})
				}
			}
			row.RenderShapes = []v1.ContextFabricRenderShape{{
				ShapeID: "rs_1", Kind: v1.ContextFabricRenderKindQuadrant, SelectedBy: v1.ContextFabricRenderRuleLandscapePosition,
				Title: "Churn and delivery units, 30 days to Nov 2, 2026", AxisKind: v1.ContextFabricRenderAxisCategory,
				AxisLabel: xWord, ValueLabel: yWord, Series: []v1.ContextFabricRenderSeries{x, y},
			}}
			row.Limitations = append(row.Limitations, line)
			row.Completeness.ClaimedFactsCount = len(row.ClaimedFacts)
			row.Completeness.RowsCount = len(row.ClaimedFacts)
			if err := row.Validate(); err != nil {
				t.Fatalf("the stored row does not validate: %v", err)
			}
			seedResult3355(t, store, callerOrgID, row)
			app, token := newParityHostedApp(t, investigatorFunc(func(context.Context, storage.Principal, cf.InvestigationRequest) (cf.InvestigationResult, error) {
				return row, nil
			}), store)
			app.config.ContextFabricLandscapeQuadrantEnabled = enabled
			serveAsNarrowedUser(t, app)

			recorder := httptest.NewRecorder()
			app.Handler().ServeHTTP(recorder, investigationResultRequest(t, token, row.ResultID))
			if recorder.Code != http.StatusOK {
				t.Fatalf("read: %d %s", recorder.Code, recorder.Body.String())
			}
			body := recorder.Body.String()
			if served := strings.Contains(body, `"landscape_position"`); served != enabled {
				t.Fatalf("by id: enabled=%v, served the quadrant=%v", enabled, served)
			}
			if served := strings.Contains(body, line); served != enabled {
				t.Fatalf("by id: enabled=%v, served the line=%v", enabled, served)
			}

			created := httptest.NewRecorder()
			app.Handler().ServeHTTP(created, investigationRequest(t, token))
			if created.Code != http.StatusOK {
				t.Fatalf("create: %d %s", created.Code, created.Body.String())
			}
			body = created.Body.String()
			if served := strings.Contains(body, `"landscape_position"`); served != enabled {
				t.Fatalf("create: enabled=%v, served the quadrant=%v", enabled, served)
			}
			if served := strings.Contains(body, line); served != enabled {
				t.Fatalf("create: enabled=%v, served the line=%v", enabled, served)
			}
		})
	}
}
