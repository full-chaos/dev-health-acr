package contextfabric

import (
	"math"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4415: the DETERMINISTIC selection rules.
//
// The defect this file pins is chris's 2026-08-29 19:59 PDT report: the
// teams answer rendered the ranked-teams table and nothing else, although
// the answer carried a per-team attention score (46.7), a per-driver
// contribution breakdown (readiness 20.0 / operational 13.3 / workload
// 13.3) and dated readiness records (2026-08-03, 08-18, 08-30). The
// fixtures below are those numbers.
//
// The mirror-image defect matters just as much and is tested beside it:
// North Star check 10 makes rich views conditional on intent, so an answer
// carrying cohort data for a SINGLE-SUBJECT question must get no chart. A
// selector that drew one whenever the data allowed would answer a question
// nobody asked (check 1).

func renderFloat(value float64) *float64 { return &value }

func renderScalarNumber(value float64) ScalarValue {
	return ScalarValue{Number: &value}
}

func renderScalarString(value string) ScalarValue {
	return ScalarValue{String: &value}
}

// chrisTeamsAnswer reproduces the shape of the live teams answer: a
// discovered cohort with one ranked member scoring 46.7 out of three
// available signal families, plus a dated readiness fact.
func chrisTeamsAnswer() InvestigationResult {
	return InvestigationResult{
		Interpretation: InterpretedQuestion{Shape: contractsv1.ContextFabricShapeDiscoveredCohort},
		Cohort: &Cohort{
			Kind: contractsv1.ContextFabricSubjectTeam,
			Members: []CohortMember{{
				Subject:         SubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: "team:gh:ops-team", Label: "ops-team"},
				Rank:            1,
				RankingComputed: true,
				AttentionRank:   1,
				Score:           renderFloat(46.7),
				Drivers: []contractsv1.ContextFabricCohortMemberDriver{
					{Signal: "readiness.coverage_gap", Value: 1, Weight: 20, WeightContributed: 20},
					{Signal: "operational_deficiencies.severity", Value: 0.665, Weight: 20, WeightContributed: 13.3},
					{Signal: "workload.forecast_pressure", Value: 0.665, Weight: 20, WeightContributed: 13.3},
				},
			}},
		},
		ClaimedFacts: []ClaimedFact{{
			ClaimID: "claim_readiness_trend",
			Kind:    "readiness",
			Subject: SubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: "team:gh:ops-team", Label: "ops-team"},
			Field:   "coverage_ratio",
			Rows: []ClaimedFactRow{
				{Fields: map[string]ScalarValue{"day": renderScalarString("2026-08-03"), "coverage_ratio": renderScalarNumber(0.41)}},
				{Fields: map[string]ScalarValue{"day": renderScalarString("2026-08-18"), "coverage_ratio": renderScalarNumber(0.52)}},
				{Fields: map[string]ScalarValue{"day": renderScalarString("2026-08-30"), "coverage_ratio": renderScalarNumber(0.6)}},
			},
		}},
	}
}

func shapeByRule(shapes []contractsv1.ContextFabricRenderShape, rule contractsv1.ContextFabricRenderShapeRule) *contractsv1.ContextFabricRenderShape {
	for i := range shapes {
		if shapes[i].SelectedBy == rule {
			return &shapes[i]
		}
	}
	return nil
}

// TestCohortAnswerSelectsScoreBarsContributionStackAndTrend is the direct
// red-first proof of the reported defect: all three shapes chris named must
// be selected for the answer he asked about.
func TestCohortAnswerSelectsScoreBarsContributionStackAndTrend(t *testing.T) {
	t.Parallel()
	shapes, event := SelectRenderShapes(chrisTeamsAnswer())
	if len(shapes) != 3 {
		t.Fatalf("SelectRenderShapes() selected %d shapes, want the score bars, the driver contribution stack and the dated trend; skipped=%+v", len(shapes), event.Skipped)
	}

	score := shapeByRule(shapes, contractsv1.ContextFabricRenderRuleCohortAttentionScore)
	if score == nil {
		t.Fatal("no attention-score shape was selected for a cohort answer whose member carries a score")
	}
	if score.Presentation != contractsv1.ContextFabricRenderPresentationBars || score.AxisKind != contractsv1.ContextFabricRenderAxisCategory {
		t.Errorf("attention score shape = %s/%s, want bars over a category axis", score.Presentation, score.AxisKind)
	}
	if got := score.Series[0].Points[0].Value; got != 46.7 {
		t.Errorf("plotted score = %v, want the member's own 46.7 verbatim", got)
	}

	contribution := shapeByRule(shapes, contractsv1.ContextFabricRenderRuleCohortDriverContribution)
	if contribution == nil {
		t.Fatal("no driver-contribution shape was selected; a bare score is exactly what North Star check 8 forbids")
	}
	if contribution.Presentation != contractsv1.ContextFabricRenderPresentationStackedBars {
		t.Errorf("contribution shape presentation = %s, want stacked_bars", contribution.Presentation)
	}
	if len(contribution.Series) != 3 {
		t.Fatalf("contribution shape carries %d series, want one per available driver family", len(contribution.Series))
	}
	// The stack must SUM to the score it breaks down. This is the property
	// that makes a stacked bar an honest claim rather than three bars in a
	// column, and it holds only because every segment is copied verbatim.
	total := 0.0
	for _, series := range contribution.Series {
		total += series.Points[0].Value
	}
	// Compared with a tolerance, unlike every value-copy assertion in this
	// file: this is the one place a SUM is computed, and 20.0+13.3+13.3 is
	// not exactly representable in binary floating point. The copies
	// themselves are still compared exactly -- by the contract validator,
	// in TestSelectedShapesValidateAgainstTheirOwnAnswer below.
	if math.Abs(total-46.6) > 1e-9 {
		t.Errorf("contribution segments sum to %v, want the 20.0+13.3+13.3 the member's own drivers carry", total)
	}

	trend := shapeByRule(shapes, contractsv1.ContextFabricRenderRuleDatedFactTrend)
	if trend == nil {
		t.Fatal("no trend shape was selected although the claimed fact carries three dated records")
	}
	if trend.Presentation != contractsv1.ContextFabricRenderPresentationLine || trend.AxisKind != contractsv1.ContextFabricRenderAxisTime {
		t.Errorf("trend shape = %s/%s, want a line over a time axis", trend.Presentation, trend.AxisKind)
	}
	labels := []string{}
	for _, point := range trend.Series[0].Points {
		labels = append(labels, point.Label)
	}
	if len(labels) != 3 || labels[0] != "2026-08-03" || labels[2] != "2026-08-30" {
		t.Errorf("trend points = %v, want the three dated records in chronological order", labels)
	}
	if len(event.Selected) != 3 {
		t.Errorf("telemetry recorded %d selections for 3 shapes", len(event.Selected))
	}
}

// TestSelectedShapesValidateAgainstTheirOwnAnswer closes the loop: the
// selector's output must survive the contract's resolve-and-compare rule
// against the very result it was built from. A selector that computed a
// number instead of copying one would pass every assertion above and fail
// here.
func TestSelectedShapesValidateAgainstTheirOwnAnswer(t *testing.T) {
	t.Parallel()
	result := chrisTeamsAnswer()
	shapes, _ := SelectRenderShapes(result)
	result.RenderShapes = shapes
	if err := contractsv1.ValidateRenderShapesForResult(result); err != nil {
		t.Fatalf("shapes the selector built do not resolve against the answer they came from: %v", err)
	}
}

// TestSingleSubjectAnswerCarryingCohortDataSelectsNoCohortShape is the
// mirror-image guard. The pinned canonical example proves intent and data
// come apart -- it carries a ranked team cohort under a single_subject
// interpretation -- and a selector keyed on data alone would chart it.
func TestSingleSubjectAnswerCarryingCohortDataSelectsNoCohortShape(t *testing.T) {
	t.Parallel()
	result := chrisTeamsAnswer()
	result.Interpretation.Shape = contractsv1.ContextFabricShapeSingleSubject
	shapes, event := SelectRenderShapes(result)
	for _, shape := range shapes {
		if shape.SelectedBy != contractsv1.ContextFabricRenderRuleDatedFactTrend {
			t.Errorf("a single-subject question got a %s shape; rich views are conditional on intent, never on the data happening to allow one", shape.SelectedBy)
		}
	}
	if !skipRecorded(event, contractsv1.ContextFabricRenderRuleCohortAttentionScore, RenderShapeSkipNotCohortIntent) {
		t.Errorf("the cohort rule declined silently; skipped=%+v", event.Skipped)
	}
}

// TestAnswerWithNoChartableContentSelectsNothing pins the common case. Q1
// on the rig is a single-subject answer with no dated rows, and it must
// stay chartless -- with telemetry saying so, because "no shape warranted"
// and "the selector never ran" must not look the same in a log.
func TestAnswerWithNoChartableContentSelectsNothing(t *testing.T) {
	t.Parallel()
	shapes, event := SelectRenderShapes(InvestigationResult{
		Interpretation: InterpretedQuestion{Shape: contractsv1.ContextFabricShapeSingleSubject},
	})
	if shapes != nil {
		t.Fatalf("SelectRenderShapes() drew %d shapes for an answer with nothing to plot", len(shapes))
	}
	if len(event.Skipped) != 3 {
		t.Fatalf("every rule must record why it declined; skipped=%+v", event.Skipped)
	}
}

// TestUnrankedCohortMemberIsNotPlottedAsZero: "insufficient evidence" and
// "scored zero" are different states (check 12), and a bar of height zero
// says the second.
func TestUnrankedCohortMemberIsNotPlottedAsZero(t *testing.T) {
	t.Parallel()
	result := chrisTeamsAnswer()
	result.Cohort.Members = append(result.Cohort.Members, CohortMember{
		Subject:          SubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: "team:gh:no-data", Label: "no-data"},
		Rank:             2,
		InclusionReasons: []string{"in the census"},
	})
	shapes, _ := SelectRenderShapes(result)
	score := shapeByRule(shapes, contractsv1.ContextFabricRenderRuleCohortAttentionScore)
	if score == nil {
		t.Fatal("expected an attention-score shape")
	}
	for _, point := range score.Series[0].Points {
		if point.Label == "no-data" {
			t.Fatal("an unranked member was plotted; a zero-height bar claims it was measured and scored zero")
		}
	}
}

// TestTrendRuleRefusesAnUnusableDateAxis observes each clause of the
// date-axis rule failing. Every one of these row sets would produce a chart
// that claims more than the data says.
func TestTrendRuleRefusesAnUnusableDateAxis(t *testing.T) {
	t.Parallel()
	for name, rows := range map[string][]ClaimedFactRow{
		"a repeated date is two values at one position": {
			{Fields: map[string]ScalarValue{"day": renderScalarString("2026-08-03"), "v": renderScalarNumber(1)}},
			{Fields: map[string]ScalarValue{"day": renderScalarString("2026-08-03"), "v": renderScalarNumber(2)}},
		},
		"a missing date degrades the axis to index spacing": {
			{Fields: map[string]ScalarValue{"day": renderScalarString("2026-08-03"), "v": renderScalarNumber(1)}},
			{Fields: map[string]ScalarValue{"v": renderScalarNumber(2)}},
		},
		"mixed date and timestamp shapes make one elapsed scale ill-defined": {
			{Fields: map[string]ScalarValue{"day": renderScalarString("2026-08-03"), "v": renderScalarNumber(1)}},
			{Fields: map[string]ScalarValue{"day": renderScalarString("2026-08-18T09:00:00Z"), "v": renderScalarNumber(2)}},
		},
		"a value that is not a real calendar date": {
			{Fields: map[string]ScalarValue{"day": renderScalarString("2026-02-30"), "v": renderScalarNumber(1)}},
			{Fields: map[string]ScalarValue{"day": renderScalarString("2026-03-01"), "v": renderScalarNumber(2)}},
		},
		"no numeric column to plot": {
			{Fields: map[string]ScalarValue{"day": renderScalarString("2026-08-03"), "v": renderScalarString("a")}},
			{Fields: map[string]ScalarValue{"day": renderScalarString("2026-08-18"), "v": renderScalarString("b")}},
		},
		"a hole in the series would have to be bridged or broken": {
			{Fields: map[string]ScalarValue{"day": renderScalarString("2026-08-03"), "v": renderScalarNumber(1)}},
			{Fields: map[string]ScalarValue{"day": renderScalarString("2026-08-18")}},
			{Fields: map[string]ScalarValue{"day": renderScalarString("2026-08-30"), "v": renderScalarNumber(3)}},
		},
	} {
		result := InvestigationResult{
			Interpretation: InterpretedQuestion{Shape: contractsv1.ContextFabricShapeSingleSubject},
			ClaimedFacts: []ClaimedFact{{
				ClaimID: "claim_x", Kind: "metrics",
				Subject: SubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: "team:x", Label: "x"},
				Field:   "v", Rows: rows,
			}},
		}
		if shapes, _ := SelectRenderShapes(result); len(shapes) != 0 {
			t.Errorf("%s: a trend was drawn anyway (%d shapes)", name, len(shapes))
		}
	}
}

// TestSelectionIsDeterministic states the property the whole design rests
// on. Go map iteration is randomized per run, so a selector that read one
// without sorting would produce a different chart on the same answer.
func TestSelectionIsDeterministic(t *testing.T) {
	t.Parallel()
	first, _ := SelectRenderShapes(chrisTeamsAnswer())
	for i := 0; i < 32; i++ {
		next, _ := SelectRenderShapes(chrisTeamsAnswer())
		if len(next) != len(first) {
			t.Fatalf("run %d selected %d shapes, first run selected %d", i, len(next), len(first))
		}
		for s := range first {
			if next[s].ShapeID != first[s].ShapeID || next[s].SelectedBy != first[s].SelectedBy {
				t.Fatalf("run %d shape %d = %s/%s, first run = %s/%s", i, s, next[s].ShapeID, next[s].SelectedBy, first[s].ShapeID, first[s].SelectedBy)
			}
			for k := range first[s].Series {
				if next[s].Series[k].Key != first[s].Series[k].Key {
					t.Fatalf("run %d shape %d series %d = %q, first run = %q", i, s, k, next[s].Series[k].Key, first[s].Series[k].Key)
				}
			}
		}
	}
}

func skipRecorded(event RenderShapeSelectionEvent, rule contractsv1.ContextFabricRenderShapeRule, reason RenderShapeSkipReason) bool {
	for _, skip := range event.Skipped {
		if skip.Rule == rule && skip.Reason == reason {
			return true
		}
	}
	return false
}
