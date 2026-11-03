package answerprojection

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12626: the quadrant crosses into the projection with the claims it plots
// and its served line, or it is dropped, counted, and its line goes with it.

// canonicalWithQuadrant places two teams. No driver cites a claim: the quadrant is
// their citation.
func canonicalWithQuadrant(t *testing.T) contractsv1.ContextFabricInvestigationResult {
	t.Helper()
	xWord, yWord := comparisonWord(t, "churn_loc_30d"), comparisonWord(t, "delivery_units_30d")
	x := contractsv1.ContextFabricRenderSeries{Key: "churn_loc_30d", Label: xWord}
	y := contractsv1.ContextFabricRenderSeries{Key: "delivery_units_30d", Label: yWord}
	table := &contractsv1.ContextFabricClaimedFactTable{
		Field: "area_breakdown", Shape: contractsv1.ContextFabricFactTableShapeBreakdown, Key: []string{"as_of_day"},
		Measures: []string{"identity_count", "churn_loc_30d", "delivery_units_30d"},
	}
	result := richResult()
	result.ClaimedFacts = nil
	result.Drivers = nil
	for index, name := range []string{"platform", "payments"} {
		team := subject(contractsv1.ContextFabricSubjectTeam, "team:"+name, name)
		day, identities, churn, units := "2026-11-02", int64(3), int64(900+index), int64(12+index)
		rows := []contractsv1.ContextFabricClaimedFactRow{{Fields: map[string]contractsv1.ContextFabricScalarValue{
			"as_of_day": {String: &day}, "identity_count": {Integer: &identities},
			"churn_loc_30d": {Integer: &churn}, "delivery_units_30d": {Integer: &units},
		}}}
		for _, measure := range []struct {
			field  string
			value  *int64
			series *contractsv1.ContextFabricRenderSeries
		}{{"churn_loc_30d", &churn, &x}, {"delivery_units_30d", &units, &y}} {
			claim := "claim_quadrant_" + name + "_" + measure.field
			result.ClaimedFacts = append(result.ClaimedFacts, contractsv1.ContextFabricClaimedFact{
				ClaimID: claim, Kind: contractsv1.ContextFabricFactLandscape, Subject: team, Field: measure.field,
				Value: contractsv1.ContextFabricScalarValue{Integer: measure.value}, Rows: rows, Table: table,
			})
			measure.series.Points = append(measure.series.Points, contractsv1.ContextFabricRenderPoint{
				Label: name, Value: float64(*measure.value),
				Source: contractsv1.ContextFabricRenderPointSource{Kind: contractsv1.ContextFabricRenderSourceClaimedFactValue, ClaimID: claim, Field: measure.field},
			})
		}
	}
	result.RenderShapes = []contractsv1.ContextFabricRenderShape{{
		ShapeID: "rs_quadrant", Kind: contractsv1.ContextFabricRenderKindQuadrant, SelectedBy: contractsv1.ContextFabricRenderRuleLandscapePosition,
		Title: "Churn and delivery units, 30 days to Nov 2, 2026", AxisKind: contractsv1.ContextFabricRenderAxisCategory,
		AxisLabel: xWord, ValueLabel: yWord, Series: []contractsv1.ContextFabricRenderSeries{x, y},
	}}
	result.Limitations = append(result.Limitations, contractsv1.ContextFabricLandscapePositionLimitation)
	result.Completeness = contextfabric.ComputeAnswerCompleteness(result)
	if err := contractsv1.ValidateRenderShapesForResult(result); err != nil {
		t.Fatalf("the quadrant fixture must be a valid canonical shape: %v", err)
	}
	return result
}

func TestProjectionCarriesAQuadrantItsClaimsAndItsLine(t *testing.T) {
	t.Parallel()
	projection := Project(canonicalWithQuadrant(t), Budget{})
	if len(projection.RenderShapes) != 1 || len(projection.KeyFacts) != 4 || projection.ProjectionBudget.RenderShapesOmitted != 0 {
		t.Fatalf("shapes = %d, key facts = %d, omitted = %d; want the quadrant and its 4 claims", len(projection.RenderShapes), len(projection.KeyFacts), projection.ProjectionBudget.RenderShapesOmitted)
	}
	if !slices.Contains(projection.Limitations, contractsv1.ContextFabricLandscapePositionLimitation) {
		t.Fatalf("the quadrant's line is missing: %v", projection.Limitations)
	}
	if err := projection.Validate(); err != nil {
		t.Fatalf("the projection must validate: %v", err)
	}
}

// The budget keeps one team's claims. The quadrant cannot be checked, so it is
// dropped and counted, and its line, which speaks of it, goes too.
func TestProjectionDropsAQuadrantWithItsLine(t *testing.T) {
	t.Parallel()
	projection := Project(canonicalWithQuadrant(t), Budget{MaxFacts: 2})
	if len(projection.KeyFacts) != 2 {
		t.Fatalf("key facts = %d, want one team's 2 for this plant to mean anything", len(projection.KeyFacts))
	}
	if len(projection.RenderShapes) != 0 || projection.ProjectionBudget.RenderShapesOmitted != 1 {
		t.Fatalf("shapes = %d, omitted = %d; want the quadrant dropped and counted", len(projection.RenderShapes), projection.ProjectionBudget.RenderShapesOmitted)
	}
	if slices.Contains(projection.Limitations, contractsv1.ContextFabricLandscapePositionLimitation) {
		t.Fatalf("the line stayed with no quadrant: %v", projection.Limitations)
	}
	if err := projection.Validate(); err != nil {
		t.Fatalf("the projection must stay valid after the drop: %v", err)
	}
}

// Codex r1 on #1963 (executed): the projection bounds limitations before it
// knows which shapes it carries, and kept only the retrieval disclosure past a
// cut, so a carried quadrant could lose its line at the count or byte cap. Both
// required disclosures now survive either cap, and the line still goes when the
// quadrant does.
func TestTheQuadrantLineSurvivesTheLimitationCaps(t *testing.T) {
	t.Parallel()
	line := contractsv1.ContextFabricLandscapePositionLimitation
	// count caveats of size bytes each, then both required lines last, where a
	// prefix cut drops them first.
	withCaveats := func(t *testing.T, count, size int) contractsv1.ContextFabricInvestigationResult {
		result := canonicalWithQuadrant(t)
		caveats := make([]string, 0, count+2)
		for index := range count {
			caveat := fmt.Sprintf("Model caveat %03d about this answer.", index)
			for len(caveat) < size {
				caveat += " More words of the same caveat."
			}
			caveats = append(caveats, caveat)
		}
		result.Limitations = append(caveats, contractsv1.ContextFabricRetrievalDegradedLimitation, line)
		return result
	}
	for name, tc := range map[string]struct {
		count, size int
		budget      Budget
	}{
		// More entries than the projection holds.
		"the count cap": {contractsv1.ContextFabricProjectedNarrativeMaxCount, 40, Budget{}},
		// Fewer entries, more bytes than the limitations' share of the budget.
		"the byte cap": {60, 400, Budget{MaxSerializedBytes: 64 * 1024}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			projection := Project(withCaveats(t, tc.count, tc.size), tc.budget)
			if len(projection.RenderShapes) != 1 {
				t.Fatalf("shapes = %d (omitted %d), want the quadrant carried", len(projection.RenderShapes), projection.ProjectionBudget.RenderShapesOmitted)
			}
			if !slices.Contains(projection.Limitations, line) || !slices.Contains(projection.Limitations, contractsv1.ContextFabricRetrievalDegradedLimitation) {
				t.Fatalf("a carried quadrant lost a required line: %d limitations, quadrant line %v, retrieval line %v",
					len(projection.Limitations), slices.Contains(projection.Limitations, line), slices.Contains(projection.Limitations, contractsv1.ContextFabricRetrievalDegradedLimitation))
			}
			if projection.ProjectionBudget.LimitationsOmitted == 0 {
				t.Fatal("the cut dropped caveats and counted none")
			}
		})
	}
	// A quadrant the budget drops takes its line along, and the line takes no
	// caveat's place.
	projection := Project(withCaveats(t, contractsv1.ContextFabricProjectedNarrativeMaxCount, 40), Budget{MaxFacts: 2})
	if len(projection.RenderShapes) != 0 || slices.Contains(projection.Limitations, line) {
		t.Fatalf("dropped quadrant: shapes %d, line %v", len(projection.RenderShapes), slices.Contains(projection.Limitations, line))
	}
}

// The byte fitter places every required entry before any other, so a second
// required entry is not lost to the greedy fill.
func TestTheByteFitKeepsEveryRequiredEntry(t *testing.T) {
	t.Parallel()
	entry := func(text string) clampedNarrative { return clampedNarrative{value: text} }
	big, other := strings.Repeat("a", 97), strings.Repeat("b", 97)
	first, second := strings.Repeat("r", 47), strings.Repeat("s", 47)
	entries := []clampedNarrative{entry(big), entry(other), entry(first), entry(second)}
	budget := 1 + encodedNarrativeCost(first) + encodedNarrativeCost(big) + encodedNarrativeCost(second) - 1
	required := func(value string) bool { return value == first || value == second }
	kept, _ := fitNarrativeBytes(entries, budget, required)
	var values []string
	for _, each := range kept {
		values = append(values, each.value)
	}
	if !slices.Contains(values, first) || !slices.Contains(values, second) {
		t.Fatalf("kept %d entries: first %v, second %v; want both required entries", len(values), slices.Contains(values, first), slices.Contains(values, second))
	}
}

// Past the count cap, every required entry is rescued, each displacing the last
// entry that is not required.
func TestTheCountCutRescuesEveryRequiredEntry(t *testing.T) {
	t.Parallel()
	entry := func(text string) clampedNarrative { return clampedNarrative{value: text} }
	// One backing array, as the caller cuts it: the rescue must not write into
	// the dropped entries it is still reading.
	all := []clampedNarrative{entry("a"), entry("first"), entry("b"), entry("c"), entry("d"), entry("second"), entry("third")}
	kept, dropped := all[:4], all[4:]
	required := func(value string) bool { return value == "first" || value == "second" || value == "third" }
	var got []string
	for _, each := range retainPastTheCut(kept, dropped, required) {
		got = append(got, each.value)
	}
	if want := []string{"a", "first", "second", "third"}; !slices.Equal(got, want) {
		t.Fatalf("kept %v, want %v", got, want)
	}
	var still []string
	for _, each := range all {
		still = append(still, each.value)
	}
	if want := []string{"a", "first", "b", "c", "d", "second", "third"}; !slices.Equal(still, want) {
		t.Fatalf("the cut list was rewritten: %v", still)
	}
}
