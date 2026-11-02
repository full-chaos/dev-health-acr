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

// CHAOS-12513: the flag protects a consumer on a pin without the rule, so neither
// door may hand it a table stored while the flag was on.
func TestAStoredTableIsServedOnlyWithTheFlagOn(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprintf("enabled=%v", enabled), func(t *testing.T) {
			store := memoryinvestigation.NewStore()
			row := validContextFabricInvestigationResult()
			row.ResultID = "result_stored_table01"
			word, ok := v1.ContextFabricVocabularyLabel(v1.ContextFabricVocabularyFactField, "window_pr_count")
			if !ok {
				t.Fatal("the field has no word")
			}
			series := make([]v1.ContextFabricRenderSeries, 0, 2)
			for index, name := range []string{"one", "two"} {
				count := int64(48 + index)
				claim := "claim_stored_table_" + name
				subject := v1.ContextFabricSubjectRef{Kind: v1.ContextFabricSubjectRepository, CanonicalID: "repo:" + name, Label: name}
				row.ClaimedFacts = append(row.ClaimedFacts, v1.ContextFabricClaimedFact{
					ClaimID: claim, Kind: v1.ContextFabricFactMetrics, Field: "window_pr_count", Subject: subject,
					Value: v1.ContextFabricScalarValue{Integer: &count},
				})
				series = append(series, v1.ContextFabricRenderSeries{Key: subject.CanonicalID, Label: subject.Label, Points: []v1.ContextFabricRenderPoint{{
					Label: word, Value: float64(count),
					Source: v1.ContextFabricRenderPointSource{Kind: v1.ContextFabricRenderSourceClaimedFactValue, ClaimID: claim, Field: "window_pr_count"},
				}}})
			}
			row.RenderShapes = []v1.ContextFabricRenderShape{{
				ShapeID: "rs_1", Kind: v1.ContextFabricRenderKindTable, SelectedBy: v1.ContextFabricRenderRuleComparisonSideBySide,
				Title: "Side by side", AxisKind: v1.ContextFabricRenderAxisCategory, AxisLabel: "Measure", ValueLabel: "Value", Series: series,
			}}
			row.Completeness.ClaimedFactsCount = len(row.ClaimedFacts)
			seedResult3355(t, store, callerOrgID, row)
			app, token := newParityHostedApp(t, investigatorFunc(func(context.Context, storage.Principal, cf.InvestigationRequest) (cf.InvestigationResult, error) {
				return row, nil
			}), store)
			app.config.ContextFabricComparisonTableEnabled = enabled
			serveAsNarrowedUser(t, app)

			recorder := httptest.NewRecorder()
			app.Handler().ServeHTTP(recorder, investigationResultRequest(t, token, row.ResultID))
			if recorder.Code != http.StatusOK {
				t.Fatalf("read: %d %s", recorder.Code, recorder.Body.String())
			}
			if served := strings.Contains(recorder.Body.String(), `"comparison_side_by_side"`); served != enabled {
				t.Fatalf("by id: enabled=%v, served the table=%v", enabled, served)
			}

			created := httptest.NewRecorder()
			app.Handler().ServeHTTP(created, investigationRequest(t, token))
			if created.Code != http.StatusOK {
				t.Fatalf("create: %d %s", created.Code, created.Body.String())
			}
			if served := strings.Contains(created.Body.String(), `"comparison_side_by_side"`); served != enabled {
				t.Fatalf("create: enabled=%v, served the table=%v", enabled, served)
			}
		})
	}
}
