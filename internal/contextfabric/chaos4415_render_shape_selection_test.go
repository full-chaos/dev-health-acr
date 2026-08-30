package contextfabric

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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
		// Enough of a real answer to pass the FULL result validator, not
		// only the render-shape rule: codex round 1 P3 caught this fixture
		// asserting against a document the contract would have rejected,
		// which proves nothing about a real answer.
		SchemaVersion:  contractsv1.ContextFabricInvestigationResultSchema,
		ResultID:       "res_chaos4415_fixture",
		RequestID:      "req_chaos4415_fixture",
		GeneratedAt:    time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC),
		Status:         contractsv1.ContextFabricInvestigationPartial,
		Question:       "Which teams are struggling right now, and why?",
		DirectJudgment: "ops-team needs attention first.",
		Versions: contractsv1.ContextFabricVersionSet{
			ServiceVersion: "acr-v1", ContractVersion: contractsv1.ContextFabricInvestigationResultSchema,
			Backend: "graph", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
			InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
			CanonicalServiceVersion: "ops-v1",
		},
		Interpretation: InterpretedQuestion{Shape: contractsv1.ContextFabricShapeDiscoveredCohort},
		Cohort: &Cohort{
			Kind: contractsv1.ContextFabricSubjectTeam,
			Members: []CohortMember{{
				Subject:         SubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: "team:gh:ops-team", Label: "ops-team"},
				Rank:            1,
				RankingComputed: true,
				AttentionRank:   1,
				// 46.6, not the 46.7 chris's screenshot showed: the
				// contract requires a member's drivers to SUM to its score
				// (validate_context_fabric_result.go, "cohort member
				// drivers do not sum to score"), and 20.0+13.3+13.3 is
				// 46.6. A fixture carrying 46.7 beside those drivers is a
				// document the real validator rejects, so it could not
				// prove anything about a real answer (codex round 1, P3).
				Score: renderFloat(46.6),
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

// TestCohortAnswerSelectsScoreBarsAndContributionStack is the direct
// red-first proof of the reported defect: the cohort shapes chris named must
// be selected for the answer he asked about.
//
// He named three. Two are asserted here; the third, a dated trend, was
// WITHDRAWN by CHAOS-4616 because a row table cannot say which of its
// columns are measures, so any trend drawn from one was a claim resting on a
// guess. That capability returns through CHAOS-4627, not through this test.
func TestCohortAnswerSelectsScoreBarsAndContributionStack(t *testing.T) {
	t.Parallel()
	shapes, event := SelectRenderShapes(chrisTeamsAnswer())
	// Two, not three: the dated trend was WITHDRAWN by CHAOS-4616 -- see
	// chaos4616_trend_scope_test.go for why the rule could not be made
	// honest from the row table alone.
	if len(shapes) != 2 {
		t.Fatalf("SelectRenderShapes() selected %d shapes, want the score bars and the driver contribution stack; skipped=%+v", len(shapes), event.Skipped)
	}

	score := shapeByRule(shapes, contractsv1.ContextFabricRenderRuleCohortAttentionScore)
	if score == nil {
		t.Fatal("no attention-score shape was selected for a cohort answer whose member carries a score")
	}
	if score.Presentation != contractsv1.ContextFabricRenderPresentationBars || score.AxisKind != contractsv1.ContextFabricRenderAxisCategory {
		t.Errorf("attention score shape = %s/%s, want bars over a category axis", score.Presentation, score.AxisKind)
	}
	if got := score.Series[0].Points[0].Value; got != 46.6 {
		t.Errorf("plotted score = %v, want the member's own 46.6 verbatim", got)
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

	if len(event.Selected) != 2 {
		t.Errorf("telemetry recorded %d selections for 2 shapes", len(event.Selected))
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
	if len(shapes) != 0 {
		t.Errorf("a single-subject question got %d shapes; rich views are conditional on intent, never on the data happening to allow one", len(shapes))
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
		t.Fatalf("every rule must record why it declined (including the withdrawn trend rule); skipped=%+v", event.Skipped)
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

// TestSelectorReproducesTheGoldenExample proves the selector's output on a
// document the WHOLE contract accepts, not only on a hand-built fixture.
//
// codex round 1 P3 was right that a fixture the real validator would reject
// proves nothing about a real answer. The published golden example is that
// real answer: it is emitted by this very selector, carries a complete and
// internally consistent cohort, and `Validate()` is run over it in
// internal/contracts/v1. Re-running selection on it here closes the loop
// from this side — the selector must reproduce, byte for byte, the shapes
// the committed document carries, so a change to a rule that quietly alters
// a shipped answer fails here.
func TestSelectorReproducesTheGoldenExample(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "examples", "v1", "context_fabric_investigation_result_render_shapes.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result InvestigationResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("the published example is not a valid result: %v", err)
	}
	published := result.RenderShapes
	if len(published) == 0 {
		t.Fatal("the published example carries no render shapes")
	}
	result.RenderShapes = nil
	reselected, _ := SelectRenderShapes(result)
	if !reflect.DeepEqual(reselected, published) {
		t.Fatalf("re-running selection on the published example produced different shapes.\n got: %+v\nwant: %+v", reselected, published)
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

// codex round 1, P2: two distinct labels sharing their first 256 bytes
// collapse into one axis position after clamping, and the whole result is
// then rejected by its own validator.
func TestRedLongMemberLabelsDoNotCollideAfterClamping(t *testing.T) {
	prefix := strings.Repeat("a", 300)
	result := chrisTeamsAnswer()
	result.Cohort.Members[0].Subject.Label = prefix + "one"
	second := result.Cohort.Members[0]
	second.Subject = SubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: "team:gh:second", Label: prefix + "two"}
	second.AttentionRank = 2
	second.Rank = 2
	result.Cohort.Members = append(result.Cohort.Members, second)
	shapes, _ := SelectRenderShapes(result)
	result.RenderShapes = shapes
	if err := contractsv1.ValidateRenderShapesForResult(result); err != nil {
		t.Fatalf("two distinct long labels produced an invalid shape: %v", err)
	}
}

// codex round 1, P2: a cohort may carry 250 ranked members but a series is
// capped at 64 points, and the drop was silent.
func TestRedTruncatedCohortMembersAreReported(t *testing.T) {
	result := chrisTeamsAnswer()
	base := result.Cohort.Members[0]
	for i := 0; i < 80; i++ {
		member := base
		member.Subject = SubjectRef{
			Kind:        contractsv1.ContextFabricSubjectTeam,
			CanonicalID: "team:gh:filler-" + string(rune('a'+i%26)) + string(rune('a'+i/26)),
			Label:       "filler-" + string(rune('a'+i%26)) + string(rune('a'+i/26)),
		}
		member.AttentionRank = i + 2
		member.Rank = i + 2
		result.Cohort.Members = append(result.Cohort.Members, member)
	}
	_, event := SelectRenderShapes(result)
	if event.MembersTruncated == 0 {
		t.Fatal("ranked members were dropped from the chart with no count recorded; a silent drop is undiagnosable from the run's own artifacts")
	}
}
