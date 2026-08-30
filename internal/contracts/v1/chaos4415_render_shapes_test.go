package v1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// CHAOS-4415: the render-shape contract's own guards.
//
// The property under test in this file is ONE sentence: a chart number is
// never authored, re-derived, rounded or aggregated -- it is a verbatim
// copy of a number the same document already carries, and validation
// resolves it back to prove that. Every test below plants the specific
// defect one clause of validateRenderShapes exists to catch, so each guard
// is observed FAILING rather than merely present (AGENTS.md verification
// rule 2).

func float64Ptr(value float64) *float64 { return &value }
func intPtr(value int) *int             { return &value }

// renderShapeResultFixture is a minimal answer carrying one ranked cohort
// member with one driver family and one dated claimed fact -- the three
// source kinds a render point may address.
func renderShapeResultFixture(t *testing.T) ContextFabricInvestigationResult {
	t.Helper()
	return ContextFabricInvestigationResult{
		Cohort: &ContextFabricCohort{
			Kind: ContextFabricSubjectTeam,
			Members: []ContextFabricCohortMember{{
				Subject:         ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team:gh:ops-team", Label: "ops-team"},
				Rank:            1,
				RankingComputed: true,
				AttentionRank:   1,
				Score:           float64Ptr(46.7),
				Drivers: []ContextFabricCohortMemberDriver{
					{Signal: "readiness.coverage_gap", Value: 1, Weight: 20, WeightContributed: 20},
				},
			}},
		},
		ClaimedFacts: []ContextFabricClaimedFact{{
			ClaimID: "claim_readiness_trend",
			Kind:    "readiness",
			Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team:gh:ops-team", Label: "ops-team"},
			Field:   "coverage_ratio",
			Rows: []ContextFabricClaimedFactRow{
				{Fields: map[string]ContextFabricScalarValue{"coverage_ratio": {Number: float64Ptr(0.41)}}},
			},
		}},
	}
}

func scoreShape(value float64) ContextFabricRenderShape {
	return ContextFabricRenderShape{
		ShapeID:      "rs_1",
		Kind:         ContextFabricRenderKindSeries,
		Presentation: ContextFabricRenderPresentationBars,
		SelectedBy:   ContextFabricRenderRuleCohortAttentionScore,
		Title:        "Attention score by team",
		AxisKind:     ContextFabricRenderAxisCategory,
		AxisLabel:    "team",
		ValueLabel:   "attention score",
		Series: []ContextFabricRenderSeries{{
			Key: "attention_score", Label: "Attention score",
			Points: []ContextFabricRenderPoint{{
				Label: "ops-team",
				Value: value,
				Source: ContextFabricRenderPointSource{
					Kind:               ContextFabricRenderSourceCohortMemberScore,
					SubjectCanonicalID: "team:gh:ops-team",
				},
			}},
		}},
	}
}

// TestRenderShapeAcceptsAVerbatimCopyOfEverySourceKind is the control. It
// must pass for the rejection tests below to mean anything: a validator
// that rejected everything would "catch" every tamper without proving it
// can tell a copy from a forgery.
func TestRenderShapeAcceptsAVerbatimCopyOfEverySourceKind(t *testing.T) {
	t.Parallel()
	result := renderShapeResultFixture(t)
	shapes := []ContextFabricRenderShape{
		scoreShape(46.7),
		{
			ShapeID: "rs_2", Kind: ContextFabricRenderKindSeries,
			Presentation: ContextFabricRenderPresentationStackedBars,
			SelectedBy:   ContextFabricRenderRuleCohortDriverContribution,
			Title:        "Score contribution by driver, per team",
			AxisKind:     ContextFabricRenderAxisCategory, AxisLabel: "team", ValueLabel: "points contributed",
			Series: []ContextFabricRenderSeries{{
				Key: "readiness.coverage_gap", Label: "Readiness.coverage gap",
				Points: []ContextFabricRenderPoint{{
					Label: "ops-team", Value: 20,
					Source: ContextFabricRenderPointSource{Kind: ContextFabricRenderSourceCohortDriverWeight, SubjectCanonicalID: "team:gh:ops-team", Signal: "readiness.coverage_gap"},
				}},
			}},
		},
		{
			ShapeID: "rs_3", Kind: ContextFabricRenderKindSeries,
			Presentation: ContextFabricRenderPresentationLine,
			SelectedBy:   ContextFabricRenderRuleDatedFactTrend,
			Title:        "Coverage ratio over time — ops-team",
			AxisKind:     ContextFabricRenderAxisTime, AxisLabel: "day", ValueLabel: "Coverage ratio",
			Series: []ContextFabricRenderSeries{{
				Key: "coverage_ratio", Label: "Coverage ratio",
				Points: []ContextFabricRenderPoint{{
					Label: "2026-08-03", Value: 0.41,
					Source: ContextFabricRenderPointSource{Kind: ContextFabricRenderSourceClaimedFactRow, ClaimID: "claim_readiness_trend", RowIndex: intPtr(0), Field: "coverage_ratio"},
				}},
			}},
		},
	}
	if err := validateRenderShapes(shapes, renderShapeSourcesFromResult(result)); err != nil {
		t.Fatalf("validateRenderShapes() rejected verbatim copies of the result's own numbers: %v", err)
	}
}

// TestRenderShapeRejectsATamperedNumber is the ticket's own requirement --
// "a chart is a claimed fact; numbers are never model-authored" -- reduced
// to the smallest observable defect. 46.7 is the score the cohort actually
// carries; 46.8 is a number nothing in the answer says.
//
// The mutation that proves this guard is real: delete the
// `resolved != point.Value` comparison in validateRenderShapes and this
// test passes while TestRenderShapeAcceptsAVerbatimCopyOfEverySourceKind
// also passes -- i.e. the pair distinguishes a validator that CHECKS from
// one that merely walks.
func TestRenderShapeRejectsATamperedNumber(t *testing.T) {
	t.Parallel()
	result := renderShapeResultFixture(t)
	err := validateRenderShapes([]ContextFabricRenderShape{scoreShape(46.8)}, renderShapeSourcesFromResult(result))
	if err == nil {
		t.Fatal("validateRenderShapes() accepted a point whose value does not equal its cited source -- a chart number was allowed to disagree with the fact behind it")
	}
	if !strings.Contains(err.Error(), "never re-derived") {
		t.Fatalf("error does not name the invariant it enforces: %v", err)
	}
}

// TestRenderShapeRejectsARoundedNumber closes the tempting middle ground.
// Rounding is the change a well-meaning renderer makes, and it is exactly
// as wrong as inventing one: the answer says 46.7, and a chart claiming 47
// is a chart of a number nothing measured.
func TestRenderShapeRejectsARoundedNumber(t *testing.T) {
	t.Parallel()
	result := renderShapeResultFixture(t)
	if err := validateRenderShapes([]ContextFabricRenderShape{scoreShape(47)}, renderShapeSourcesFromResult(result)); err == nil {
		t.Fatal("validateRenderShapes() accepted a rounded score; there is no tolerance, because there is no legitimate arithmetic for a shape to do")
	}
}

// TestRenderShapeRejectsAPointCitingAbsentContent covers the other half of
// "the reader can check it": a source that names a team, driver or claim
// the document does not carry.
func TestRenderShapeRejectsAPointCitingAbsentContent(t *testing.T) {
	t.Parallel()
	result := renderShapeResultFixture(t)
	for name, source := range map[string]ContextFabricRenderPointSource{
		"unknown member": {Kind: ContextFabricRenderSourceCohortMemberScore, SubjectCanonicalID: "team:gh:nobody"},
		"unknown driver": {Kind: ContextFabricRenderSourceCohortDriverWeight, SubjectCanonicalID: "team:gh:ops-team", Signal: "workload.forecast_pressure"},
		"unknown claim":  {Kind: ContextFabricRenderSourceClaimedFactRow, ClaimID: "claim_does_not_exist", RowIndex: intPtr(0), Field: "coverage_ratio"},
		"row past end":   {Kind: ContextFabricRenderSourceClaimedFactRow, ClaimID: "claim_readiness_trend", RowIndex: intPtr(9), Field: "coverage_ratio"},
		"absent field":   {Kind: ContextFabricRenderSourceClaimedFactRow, ClaimID: "claim_readiness_trend", RowIndex: intPtr(0), Field: "not_a_column"},
		"unknown kind":   {Kind: "invented_source"},
	} {
		shape := scoreShape(46.7)
		shape.Series[0].Points[0].Source = source
		if err := validateRenderShapes([]ContextFabricRenderShape{shape}, renderShapeSourcesFromResult(result)); err == nil {
			t.Errorf("%s: validateRenderShapes() accepted a point citing content the answer does not carry", name)
		}
	}
}

// TestRenderShapeRejectsAPresentationOnANonSeriesKind keeps a consumer's
// switch total: there is no such thing as a "stacked_bars" sankey, and a
// document asserting one has to be rejected rather than interpreted.
func TestRenderShapeRejectsAPresentationOnANonSeriesKind(t *testing.T) {
	t.Parallel()
	result := renderShapeResultFixture(t)
	shape := scoreShape(46.7)
	shape.Kind = ContextFabricRenderKindSankey
	if err := validateRenderShapes([]ContextFabricRenderShape{shape}, renderShapeSourcesFromResult(result)); err == nil {
		t.Fatal("validateRenderShapes() accepted a presentation on a kind that carries no series payload")
	}
	shape.Presentation = ""
	if err := validateRenderShapes([]ContextFabricRenderShape{shape}, renderShapeSourcesFromResult(result)); err != nil {
		t.Fatalf("a non-series kind without a presentation must still validate: %v", err)
	}
	series := scoreShape(46.7)
	series.Presentation = ""
	if err := validateRenderShapes([]ContextFabricRenderShape{series}, renderShapeSourcesFromResult(result)); err == nil {
		t.Fatal("validateRenderShapes() accepted a series shape with no presentation; a consumer would not know how to draw it")
	}
}

// TestRenderShapeRejectsARepeatedPointLabelWithinASeries: one axis
// position, one value. Two points at the same label is a table, drawn as a
// chart that silently overwrites itself.
func TestRenderShapeRejectsARepeatedPointLabelWithinASeries(t *testing.T) {
	t.Parallel()
	result := renderShapeResultFixture(t)
	shape := scoreShape(46.7)
	shape.Series[0].Points = append(shape.Series[0].Points, shape.Series[0].Points[0])
	if err := validateRenderShapes([]ContextFabricRenderShape{shape}, renderShapeSourcesFromResult(result)); err == nil {
		t.Fatal("validateRenderShapes() accepted two points at the same axis position within one series")
	}
}

// TestRenderShapeRejectsAMalformedSourceAddress proves shape and
// resolution are checked separately: an address carrying fields its kind
// does not use is malformed, whatever it happens to resolve to.
func TestRenderShapeRejectsAMalformedSourceAddress(t *testing.T) {
	t.Parallel()
	result := renderShapeResultFixture(t)
	shape := scoreShape(46.7)
	shape.Series[0].Points[0].Source.Signal = "readiness.coverage_gap"
	if err := validateRenderShapes([]ContextFabricRenderShape{shape}, renderShapeSourcesFromResult(result)); err == nil {
		t.Fatal("validateRenderShapes() accepted a cohort_member_score source carrying a driver signal")
	}
}

// TestRenderShapeGoldenExampleValidates proves the published fixture is a
// document THIS code accepts, not one hand-authored to satisfy the JSON
// Schema alone. contractcheck only proves the example matches the schema;
// the schema cannot express the resolve-and-compare rule at all, so
// without this the fixture could carry a forged number and every gate
// would still be green.
func TestRenderShapeGoldenExampleValidates(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"context_fabric_investigation_result_render_shapes.v1.json",
		"context_fabric_answer_projection_render_shapes.v1.json",
	} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "examples", "v1", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.Contains(name, "answer_projection") {
			var projection ContextFabricAnswerProjection
			if err := json.Unmarshal(raw, &projection); err != nil {
				t.Fatalf("decode %s: %v", name, err)
			}
			if len(projection.RenderShapes) == 0 {
				t.Fatalf("%s carries no render shapes, so it proves nothing about the field it exists to demonstrate", name)
			}
			if err := projection.Validate(); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			// The projection deliberately DROPS a shape whose numbers it
			// no longer carries (the stacked driver breakdown cites every
			// signal family, and the projected ranking table surfaces only
			// each member's top two). The drop must be declared, never
			// silent -- that is the whole point of the budget.
			if projection.ProjectionBudget.RenderShapesOmitted == 0 || !projection.ProjectionBudget.Truncated {
				t.Fatalf("%s dropped a render shape without declaring it: budget = %+v", name, projection.ProjectionBudget)
			}
			continue
		}
		var result ContextFabricInvestigationResult
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatalf("decode %s: %v", name, err)
		}
		if len(result.RenderShapes) == 0 {
			t.Fatalf("%s carries no render shapes, so it proves nothing about the field it exists to demonstrate", name)
		}
		if err := result.Validate(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

// TestRenderShapeGoldenExampleTamperIsRejected is the fixture's own
// mutation proof: flip one digit of one plotted number in the published
// example and the document must stop validating. Without this, the example
// tests only that the file parses.
func TestRenderShapeGoldenExampleTamperIsRejected(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "examples", "v1", "context_fabric_investigation_result_render_shapes.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result ContextFabricInvestigationResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	result.RenderShapes[0].Series[0].Points[0].Value += 0.1
	if err := result.Validate(); err == nil {
		t.Fatal("Validate() accepted the published example with one plotted number altered")
	}
}
