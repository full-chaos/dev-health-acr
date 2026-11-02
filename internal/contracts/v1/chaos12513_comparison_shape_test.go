package v1

import (
	"strings"
	"testing"
)

// CHAOS-12513: the wire shape of a side-by-side comparison. A table carries the
// series payload: one series per subject, one point per measure, each point a
// verbatim copy of one claim's own value. Each refusal plants the defect one
// clause exists to catch.

const (
	comparisonRule   ContextFabricRenderShapeRule       = "comparison_side_by_side"
	comparisonSource ContextFabricRenderPointSourceKind = "claimed_fact_value"

	comparisonSubjectA = "repository:acme-api"
	comparisonSubjectB = "repository:acme-web"
	comparisonSubjectC = "repository:acme-ops"
	comparisonMedian   = "window_pr_cycle_hours_median"
	comparisonCount    = "window_pr_count"
)

func comparisonLabel(t *testing.T, field string) string {
	t.Helper()
	word, ok := ContextFabricVocabularyLabel(ContextFabricVocabularyFactField, field)
	if !ok {
		t.Fatalf("field %q has no word", field)
	}
	return word
}

func comparisonClaim(subject, field string, value ContextFabricScalarValue) ContextFabricClaimedFact {
	return ContextFabricClaimedFact{
		ClaimID: "claim_" + subject + "_" + field, Kind: "metrics", Field: field, Value: value,
		Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectRepository, CanonicalID: subject, Label: subject},
	}
}

func comparisonPoint(label string, value float64, claim ContextFabricClaimedFact) ContextFabricRenderPoint {
	return ContextFabricRenderPoint{Label: label, Value: value, Source: ContextFabricRenderPointSource{
		Kind: comparisonSource, ClaimID: claim.ClaimID, Field: claim.Field,
	}}
}

// comparisonFixture is two subjects and two measures, every cell present.
func comparisonFixture(t *testing.T) (ContextFabricRenderShape, ContextFabricInvestigationResult) {
	t.Helper()
	median, count := comparisonLabel(t, comparisonMedian), comparisonLabel(t, comparisonCount)
	claims := []ContextFabricClaimedFact{
		comparisonClaim(comparisonSubjectA, comparisonMedian, ContextFabricScalarValue{Number: float64Ptr(1.95)}),
		comparisonClaim(comparisonSubjectA, comparisonCount, ContextFabricScalarValue{Integer: int64Ptr(48)}),
		comparisonClaim(comparisonSubjectB, comparisonMedian, ContextFabricScalarValue{Number: float64Ptr(1.66)}),
		comparisonClaim(comparisonSubjectB, comparisonCount, ContextFabricScalarValue{Integer: int64Ptr(82)}),
	}
	shape := ContextFabricRenderShape{
		ShapeID: "rs_comparison", Kind: ContextFabricRenderKindTable, SelectedBy: comparisonRule,
		Title: "Side by side", AxisKind: ContextFabricRenderAxisCategory, AxisLabel: "Measure", ValueLabel: "Value",
		Series: []ContextFabricRenderSeries{
			{Key: comparisonSubjectA, Label: "acme-api", Points: []ContextFabricRenderPoint{comparisonPoint(median, 1.95, claims[0]), comparisonPoint(count, 48, claims[1])}},
			{Key: comparisonSubjectB, Label: "acme-web", Points: []ContextFabricRenderPoint{comparisonPoint(median, 1.66, claims[2]), comparisonPoint(count, 82, claims[3])}},
		},
	}
	return shape, ContextFabricInvestigationResult{ClaimedFacts: claims}
}

func validateComparison(shape ContextFabricRenderShape, result ContextFabricInvestigationResult) error {
	return validateRenderShapes([]ContextFabricRenderShape{shape}, renderShapeSourcesFromResult(result))
}

func TestASideBySideComparisonIsAccepted(t *testing.T) {
	t.Parallel()
	shape, result := comparisonFixture(t)
	if err := validateComparison(shape, result); err != nil {
		t.Fatalf("a comparison of one measure per side is valid: %v", err)
	}
}

// A measure one side lacks has no point. The gap is empty, never zero, so the
// table stays valid while one measure still stands on every side.
func TestAGapInAComparisonStaysEmpty(t *testing.T) {
	t.Parallel()
	shape, result := comparisonFixture(t)
	shape.Series[1].Points = shape.Series[1].Points[:1]
	if err := validateComparison(shape, result); err != nil {
		t.Fatalf("a real gap is valid: %v", err)
	}
}

func TestAComparisonOfThreeSubjectsIsAccepted(t *testing.T) {
	t.Parallel()
	shape, result := comparisonFixture(t)
	third := comparisonClaim(comparisonSubjectC, comparisonMedian, ContextFabricScalarValue{Number: float64Ptr(0.89)})
	result.ClaimedFacts = append(result.ClaimedFacts, third)
	shape.Series = append(shape.Series, ContextFabricRenderSeries{Key: comparisonSubjectC, Label: "acme-ops", Points: []ContextFabricRenderPoint{
		comparisonPoint(comparisonLabel(t, comparisonMedian), 0.89, third),
	}})
	if err := validateComparison(shape, result); err != nil {
		t.Fatalf("three subjects are valid: %v", err)
	}
}

func TestAComparisonIsRefused(t *testing.T) {
	t.Parallel()
	source := func(s *ContextFabricRenderShape, series, point int) *ContextFabricRenderPointSource {
		return &s.Series[series].Points[point].Source
	}
	for name, plant := range map[string]struct {
		edit func(*ContextFabricRenderShape, *ContextFabricInvestigationResult)
		want string
	}{
		"a table under another rule": {func(s *ContextFabricRenderShape, _ *ContextFabricInvestigationResult) {
			s.SelectedBy = ContextFabricRenderRuleDatedFactTrend
		}, "selected_by"},
		"the rule on another kind": {func(s *ContextFabricRenderShape, _ *ContextFabricInvestigationResult) {
			s.Kind, s.Presentation = ContextFabricRenderKindSeries, ContextFabricRenderPresentationBars
		}, "selected_by"},
		"a time axis": {func(s *ContextFabricRenderShape, _ *ContextFabricInvestigationResult) {
			s.AxisKind = ContextFabricRenderAxisTime
		}, "category"},
		"one series": {func(s *ContextFabricRenderShape, _ *ContextFabricInvestigationResult) {
			s.Series = s.Series[:1]
		}, "2 or more"},
		"a declared unit": {func(s *ContextFabricRenderShape, _ *ContextFabricInvestigationResult) {
			s.ValueUnit = ContextFabricRenderValueUnitRatio
		}, "value_unit"},
		"a row source": {func(s *ContextFabricRenderShape, r *ContextFabricInvestigationResult) {
			index := 0
			r.ClaimedFacts[0].Rows = []ContextFabricClaimedFactRow{{Fields: map[string]ContextFabricScalarValue{comparisonMedian: r.ClaimedFacts[0].Value}}}
			*source(s, 0, 0) = ContextFabricRenderPointSource{Kind: ContextFabricRenderSourceClaimedFactRow, ClaimID: r.ClaimedFacts[0].ClaimID, RowIndex: &index, Field: comparisonMedian}
		}, "must cite"},
		"a series that is not its subject": {func(s *ContextFabricRenderShape, _ *ContextFabricInvestigationResult) {
			s.Series[0].Key = "repository:someone-else"
		}, "canonical id"},
		"a column holding another subject's number": {func(s *ContextFabricRenderShape, _ *ContextFabricInvestigationResult) {
			s.Series[0].Points[0], s.Series[1].Points[0] = s.Series[1].Points[0], s.Series[0].Points[0]
		}, "canonical id"},
		"subjects of two kinds": {func(_ *ContextFabricRenderShape, r *ContextFabricInvestigationResult) {
			r.ClaimedFacts[2].Subject.Kind = ContextFabricSubjectTeam
			r.ClaimedFacts[3].Subject.Kind = ContextFabricSubjectTeam
		}, "one kind"},
		"a label that is not its field's word": {func(s *ContextFabricRenderShape, _ *ContextFabricInvestigationResult) {
			s.Series[0].Points[0].Label, s.Series[1].Points[0].Label = "Median cycle time (hours)", "Median cycle time (hours)"
		}, "served word"},
		"a field with no word": {func(s *ContextFabricRenderShape, r *ContextFabricInvestigationResult) {
			r.ClaimedFacts[1].Field = "a_future_measure"
			source(s, 0, 1).Field = "a_future_measure"
		}, "served word"},
		"one label over two fact kinds": {func(_ *ContextFabricRenderShape, r *ContextFabricInvestigationResult) {
			r.ClaimedFacts[2].Kind = "pull_requests"
		}, "fact kind"},
		"no measure on every side": {func(s *ContextFabricRenderShape, _ *ContextFabricInvestigationResult) {
			s.Series[0].Points = s.Series[0].Points[:1]
			s.Series[1].Points = s.Series[1].Points[1:]
		}, "every side"},
		"a tampered number": {func(s *ContextFabricRenderShape, _ *ContextFabricInvestigationResult) {
			s.Series[0].Points[0].Value = 2
		}, "never re-derived"},
		"a status word": {func(_ *ContextFabricRenderShape, r *ContextFabricInvestigationResult) {
			word := "open"
			r.ClaimedFacts[0].Value = ContextFabricScalarValue{String: &word}
		}, "not a number"},
		"a derived claim": {func(_ *ContextFabricRenderShape, r *ContextFabricInvestigationResult) {
			r.ClaimedFacts[0].DerivedFrom = []ContextFabricDerivationInput{{FactID: "fact_one", Field: comparisonMedian}}
		}, "derived"},
		"an integer too large to plot": {func(s *ContextFabricRenderShape, r *ContextFabricInvestigationResult) {
			big := ContextFabricRenderPointExactIntegerBound + 1
			r.ClaimedFacts[1].Value = ContextFabricScalarValue{Integer: &big}
			s.Series[0].Points[1].Value = float64(big)
		}, "too large"},
		"a negative integer too large to plot": {func(s *ContextFabricRenderShape, r *ContextFabricInvestigationResult) {
			big := -ContextFabricRenderPointExactIntegerBound - 1
			r.ClaimedFacts[1].Value = ContextFabricScalarValue{Integer: &big}
			s.Series[0].Points[1].Value = float64(big)
		}, "too large"},
		"a row index on a value source": {func(s *ContextFabricRenderShape, _ *ContextFabricInvestigationResult) {
			index := 0
			source(s, 0, 0).RowIndex = &index
		}, "wrong address"},
		"a subject id on a value source": {func(s *ContextFabricRenderShape, _ *ContextFabricInvestigationResult) {
			source(s, 0, 0).SubjectCanonicalID = comparisonSubjectA
		}, "wrong address"},
		"a signal on a value source": {func(s *ContextFabricRenderShape, _ *ContextFabricInvestigationResult) {
			source(s, 0, 0).Signal = "health.compounding_risk"
		}, "wrong address"},
		"a value source with no claim": {func(s *ContextFabricRenderShape, _ *ContextFabricInvestigationResult) {
			source(s, 0, 0).ClaimID = ""
		}, "wrong address"},
		"a value source with no field": {func(s *ContextFabricRenderShape, _ *ContextFabricInvestigationResult) {
			source(s, 0, 0).Field = ""
		}, "wrong address"},
		"a value source naming another field": {func(s *ContextFabricRenderShape, _ *ContextFabricInvestigationResult) {
			source(s, 0, 0).Field = comparisonCount
		}, "own field"},
		"a claim the answer does not carry": {func(s *ContextFabricRenderShape, _ *ContextFabricInvestigationResult) {
			source(s, 0, 0).ClaimID = "claim_not_in_this_answer"
		}, "does not carry"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			shape, result := comparisonFixture(t)
			plant.edit(&shape, &result)
			err := validateComparison(shape, result)
			if err == nil || !strings.Contains(err.Error(), plant.want) {
				t.Fatalf("err = %v, want it to name %q", err, plant.want)
			}
		})
	}
}

// The projection resolves the same source from its own key facts, so a shape it
// keeps is one a reader of that document can check.
func TestAProjectionResolvesAClaimedValue(t *testing.T) {
	t.Parallel()
	shape, result := comparisonFixture(t)
	facts := make([]ContextFabricProjectedFact, 0, len(result.ClaimedFacts))
	for _, claim := range result.ClaimedFacts {
		facts = append(facts, ContextFabricProjectedFact{ClaimID: claim.ClaimID, Kind: claim.Kind, Subject: claim.Subject, Field: claim.Field, Value: claim.Value})
	}
	projection := ContextFabricAnswerProjection{KeyFacts: facts}
	if err := validateRenderShapes([]ContextFabricRenderShape{shape}, renderShapeSourcesFromProjection(projection)); err != nil {
		t.Fatalf("the projection holds every cell: %v", err)
	}
	projection.KeyFacts = facts[1:]
	if err := validateRenderShapes([]ContextFabricRenderShape{shape}, renderShapeSourcesFromProjection(projection)); err == nil {
		t.Fatal("a shape citing a fact the projection cut must not validate")
	}
}

// A comparison reads a label as one measure, which holds only while no two fields
// share a word.
func TestEveryFactFieldHasItsOwnWord(t *testing.T) {
	t.Parallel()
	labels := ContextFabricFactFieldLabels()
	if len(labels) == 0 {
		t.Fatal("no field was read")
	}
	seen := make(map[string]string, len(labels))
	for field, word := range labels {
		if other, taken := seen[word]; taken {
			t.Errorf("fields %q and %q share the word %q", field, other, word)
		}
		seen[word] = field
	}
}
