package contextfabric

import (
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12608 amendment 2: the server-attached forecast summary, the curve's
// summary clauses, and the P50/P85/P95 table drawn when no curve is.

func summaryFact(scope, state string, p50, p85 *int64) CanonicalFact {
	fields := map[string]FactValue{
		"basis":                         StringFactValue("capacity_forecast"),
		"computed_at":                   StringFactValue("2026-11-01 04:00:00.000"),
		"completion_distribution_state": StringFactValue(state),
		"simulation_count":              IntegerFactValue(10000),
		"horizon_days":                  IntegerFactValue(365),
		"forecast_p50_date":             StringFactValue("2026-11-13"),
	}
	if scope != "" {
		fields["work_scope_id"] = StringFactValue(scope)
	}
	if p50 != nil {
		fields["forecast_p50_days"] = IntegerFactValue(*p50)
	}
	if p85 != nil {
		fields["forecast_p85_days"] = IntegerFactValue(*p85)
	}
	return CanonicalFact{Kind: FactWorkload, Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "team:chaos", Label: "CHAOS"}, Fields: fields}
}

func days(n int64) *int64 { return &n }

func TestTheForecastSummaryIsDisclosedFromTheGroundingFact(t *testing.T) {
	fact := summaryFact("scope-a", "not_stored", days(12), days(30))
	claim := forecastClaim(forecastTestClaim, nil)
	claim.Rows, claim.Table, claim.TimeSeriesRows, claim.TimeSeriesTable = nil, nil, nil, nil
	claim.CompletionForecast = forecastSummary(contractsv1.ContextFabricCompletionDistributionAvailable, 99)
	claims := discloseCompletionForecast([]ClaimedFact{claim}, []CanonicalFact{fact})
	got := claims[0].CompletionForecast
	if got == nil || got.State != contractsv1.ContextFabricCompletionDistributionNotStored || *got.P50Days != 12 || *got.P85Days != 30 || got.P95Days != nil {
		t.Fatalf("summary = %+v, want the fact's own (a draft's summary overwritten, an absent p95 absent)", got)
	}
	if got.WorkScopeID != "scope-a" || got.P50Date != "2026-11-13" || *got.HorizonDays != 365 || got.UnfinishedRuns != nil {
		t.Fatalf("summary = %+v", got)
	}
	// The fact's ClickHouse text is served as an RFC 3339 UTC instant.
	if got.ComputedAt != "2026-11-01T04:00:00Z" {
		t.Fatalf("computed_at = %q, want the RFC 3339 UTC instant", got.ComputedAt)
	}
	// A fact that does not serve the distribution (switch off) gives none.
	off := summaryFact("scope-a", "", days(12), nil)
	delete(off.Fields, "completion_distribution_state")
	if claims := discloseCompletionForecast([]ClaimedFact{forecastClaim(forecastTestClaim, nil)}, []CanonicalFact{off}); claims[0].CompletionForecast != nil {
		t.Fatalf("switch off: summary = %+v, want none", claims[0].CompletionForecast)
	}
}

func TestTheCurveNeedsAnAvailableSummaryAndHoldsTheHorizonLast(t *testing.T) {
	daysAxis := []int64{5, 12, 365}
	result := forecastResult(forecastClaim(forecastTestClaim, forecastDistributionRows(daysAxis, []float64{0.1, 0.8, 0.8})))
	shapes, _ := selectRenderShapes(result, forecastFrame(ObligationCompletion), forecastOn)
	good := forecastsOf(shapes)[0]
	good.ShapeID = "rs_1"
	check := func(name string, mutate func(r *InvestigationResult, s *contractsv1.ContextFabricRenderShape), want string) {
		t.Run(name, func(t *testing.T) {
			probe := result
			probe.ClaimedFacts = append([]ClaimedFact(nil), result.ClaimedFacts...)
			summary := *probe.ClaimedFacts[0].CompletionForecast
			probe.ClaimedFacts[0].CompletionForecast = &summary
			shape := good
			shape.Series = []contractsv1.ContextFabricRenderSeries{{Key: good.Series[0].Key, Label: good.Series[0].Label, Points: append([]contractsv1.ContextFabricRenderPoint(nil), good.Series[0].Points...)}}
			mutate(&probe, &shape)
			probe.RenderShapes = []contractsv1.ContextFabricRenderShape{shape}
			err := contractsv1.ValidateRenderShapesForResult(probe)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("Validate = %v, want %q", err, want)
			}
		})
	}
	check("a state other than available", func(r *InvestigationResult, _ *contractsv1.ContextFabricRenderShape) {
		r.ClaimedFacts[0].CompletionForecast.State = contractsv1.ContextFabricCompletionDistributionNoRunWithinHorizon
	}, "does not state an available completion distribution")
	check("no summary", func(r *InvestigationResult, _ *contractsv1.ContextFabricRenderShape) {
		r.ClaimedFacts[0].CompletionForecast = nil
	}, "does not state an available completion distribution")
	check("the horizon before the last point", func(r *InvestigationResult, _ *contractsv1.ContextFabricRenderShape) {
		horizon := int64(12)
		r.ClaimedFacts[0].CompletionForecast.HorizonDays = &horizon
	}, "at or past the horizon and is not the last point")
	check("every share 0", func(r *InvestigationResult, s *contractsv1.ContextFabricRenderShape) {
		rows := forecastDistributionRows(daysAxis, []float64{0, 0, 0})
		r.ClaimedFacts[0].Rows = rows
		for index := range s.Series[0].Points {
			s.Series[0].Points[index].Value = 0
		}
	}, "every share is 0")
}

func percentileTablesOf(shapes []contractsv1.ContextFabricRenderShape) []contractsv1.ContextFabricRenderShape {
	var out []contractsv1.ContextFabricRenderShape
	for _, shape := range shapes {
		if shape.SelectedBy == contractsv1.ContextFabricRenderRuleCompletionForecastPercentiles {
			out = append(out, shape)
		}
	}
	return out
}

func percentilesSkipOf(t *testing.T, event RenderShapeSelectionEvent) RenderShapeSkipReason {
	t.Helper()
	if err := event.Accounted(); err != nil {
		t.Fatalf("Accounted: %v", err)
	}
	for _, skip := range event.Skipped {
		if skip.Rule == contractsv1.ContextFabricRenderRuleCompletionForecastPercentiles {
			return skip.Reason
		}
	}
	return ""
}

func TestWithNoCurveTheClaimedScopesGetTheirPercentiles(t *testing.T) {
	rows := forecastDistributionRows([]int64{5, 12}, []float64{0.5, 1})
	second := forecastClaim("claim_forecast_second", rows)
	second.CompletionForecast = forecastSummary(contractsv1.ContextFabricCompletionDistributionAvailable, 20, 44)
	second.CompletionForecast.WorkScopeID = "scope-b"
	noDistribution := forecastClaim("claim_forecast_none", nil)
	noDistribution.Rows, noDistribution.Table, noDistribution.TimeSeriesRows, noDistribution.TimeSeriesTable = nil, nil, nil, nil
	noDistribution.CompletionForecast = forecastSummary(contractsv1.ContextFabricCompletionDistributionNotStored, 7)
	noDistribution.CompletionForecast.WorkScopeID = "scope-c"

	t.Run("two scopes with a curve each: the table, both rows", func(t *testing.T) {
		result := forecastResult(forecastClaim(forecastTestClaim, rows), second)
		shapes, event := selectRenderShapes(result, forecastFrame(ObligationCompletion), forecastOn)
		if len(forecastsOf(shapes)) != 0 || forecastSkipOf(t, event) != RenderShapeSkipMoreThanOneScope {
			t.Fatal("a curve was drawn for two scopes")
		}
		tables := percentileTablesOf(shapes)
		if len(tables) != 1 || len(tables[0].Series) != 2 || len(tables[0].Series[0].Points) != 3 || len(tables[0].Series[1].Points) != 2 {
			t.Fatalf("tables = %+v", tables)
		}
		result.RenderShapes = shapes
		for index := range result.RenderShapes {
			result.RenderShapes[index].ShapeID = "rs_" + string(rune('1'+index))
		}
		if err := contractsv1.ValidateRenderShapesForResult(result); err != nil {
			t.Fatalf("the table: %v", err)
		}
	})
	t.Run("one scope with no distribution: its row, no curve", func(t *testing.T) {
		result := forecastResult(noDistribution)
		shapes, event := selectRenderShapes(result, forecastFrame(ObligationCompletion), forecastOn)
		if len(forecastsOf(shapes)) != 0 || forecastSkipOf(t, event) != RenderShapeSkipNoCompletionDistribution {
			t.Fatal("a curve was drawn without a distribution")
		}
		tables := percentileTablesOf(shapes)
		if len(tables) != 1 || len(tables[0].Series) != 1 || tables[0].Series[0].Label != "scope-c" || len(tables[0].Series[0].Points) != 1 || tables[0].Series[0].Points[0].Value != 7 {
			t.Fatalf("tables = %+v", tables)
		}
	})
	t.Run("a curve: no table", func(t *testing.T) {
		result := forecastResult(forecastClaim(forecastTestClaim, rows))
		shapes, event := selectRenderShapes(result, forecastFrame(ObligationCompletion), forecastOn)
		if len(forecastsOf(shapes)) != 1 || len(percentileTablesOf(shapes)) != 0 || percentilesSkipOf(t, event) != RenderShapeSkipForecastCurveDrawn {
			t.Fatal("want the curve and no table")
		}
	})
	t.Run("no percentiles served: nothing", func(t *testing.T) {
		bare := noDistribution
		bare.CompletionForecast = forecastSummary(contractsv1.ContextFabricCompletionDistributionNotStored)
		shapes, event := selectRenderShapes(forecastResult(bare), forecastFrame(ObligationCompletion), forecastOn)
		if len(percentileTablesOf(shapes)) != 0 || percentilesSkipOf(t, event) != RenderShapeSkipNoForecastPercentiles {
			t.Fatal("a table with no percentile")
		}
	})
	t.Run("a state question: nothing", func(t *testing.T) {
		shapes, event := selectRenderShapes(forecastResult(noDistribution), forecastFrame(ObligationState), forecastOn)
		if len(percentileTablesOf(shapes)) != 0 || percentilesSkipOf(t, event) != RenderShapeSkipNotForecastIntent {
			t.Fatal("a table without forecast intent")
		}
	})
}

func TestTheContractHoldsThePercentilesTableToItsSummaries(t *testing.T) {
	noDistribution := forecastClaim("claim_forecast_none", nil)
	noDistribution.Rows, noDistribution.Table, noDistribution.TimeSeriesRows, noDistribution.TimeSeriesTable = nil, nil, nil, nil
	noDistribution.CompletionForecast = forecastSummary(contractsv1.ContextFabricCompletionDistributionNotStored, 7, 9)
	result := forecastResult(noDistribution)
	shapes, _ := selectRenderShapes(result, forecastFrame(ObligationCompletion), forecastOn)
	good := percentileTablesOf(shapes)[0]
	good.ShapeID = "rs_1"
	for _, tc := range []struct {
		name   string
		mutate func(s *contractsv1.ContextFabricRenderShape)
		want   string
	}{
		{"a label that is not the percentile's word", func(s *contractsv1.ContextFabricRenderShape) { s.Series[0].Points[0].Label = "P50" }, "not the served word"},
		{"a value that is not the summary's", func(s *contractsv1.ContextFabricRenderShape) { s.Series[0].Points[0].Value = 8 }, "claims"},
		{"a percentile the summary does not carry", func(s *contractsv1.ContextFabricRenderShape) {
			s.Series[0].Points[0].Source.Field = contractsv1.ContextFabricForecastSummaryP95Days
		}, "does not carry"},
		{"cited through a row", func(s *contractsv1.ContextFabricRenderShape) {
			s.Series[0].Points[0].Source.Kind = contractsv1.ContextFabricRenderSourceClaimedFactValue
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shape := good
			shape.Series = []contractsv1.ContextFabricRenderSeries{{Key: good.Series[0].Key, Label: good.Series[0].Label, Points: append([]contractsv1.ContextFabricRenderPoint(nil), good.Series[0].Points...)}}
			tc.mutate(&shape)
			probe := result
			probe.RenderShapes = []contractsv1.ContextFabricRenderShape{shape}
			err := contractsv1.ValidateRenderShapesForResult(probe)
			if err == nil || (tc.want != "" && !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("Validate = %v, want %q", err, tc.want)
			}
		})
	}
}
