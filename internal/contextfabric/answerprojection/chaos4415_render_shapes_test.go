package answerprojection

import (
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4415: what the bounded projection does with a canonical answer's
// render shapes.
//
// The rule is "admitted whole or dropped whole, and every drop declared".
// The failure it exists to prevent is subtle and would look fine in review:
// a shape carried across into a document whose cohort member or claimed
// fact the budget already cut. Its numbers would then cite content the
// reader cannot see, which is exactly the "chart beside a fact" this
// contract replaces -- and, because the projection re-validates, it would
// surface as a 500 on the answer surface rather than a missing chart.

func f64(v float64) *float64 { return &v }

func canonicalWithScoreShape(score float64) contractsv1.ContextFabricInvestigationResult {
	result := contractsv1.ContextFabricInvestigationResult{
		SchemaVersion:  contractsv1.ContextFabricInvestigationResultSchema,
		ResultID:       "res_render_shapes_test",
		RequestID:      "req_render_shapes_test",
		GeneratedAt:    time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC),
		Status:         contractsv1.ContextFabricInvestigationPartial,
		Question:       "Which teams are struggling right now?",
		DirectJudgment: "ops-team needs attention first.",
		Versions: contractsv1.ContextFabricVersionSet{
			ServiceVersion: "acr-v1", ContractVersion: contractsv1.ContextFabricInvestigationResultSchema, Backend: "graph",
			ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1",
			SynthesisVersion: "synthesis-v1", CanonicalServiceVersion: "ops-v1",
		},
		Cohort: &contractsv1.ContextFabricCohort{
			Kind:      contractsv1.ContextFabricSubjectTeam,
			Rationale: "Matched every team owning at least one repository in the org.",
			Members: []contractsv1.ContextFabricCohortMember{{
				Subject:          contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: "team:gh:ops-team", Label: "ops-team"},
				Rank:             2,
				InclusionReasons: []string{"in the census"},
				RankingComputed:  true,
				AttentionRank:    1,
				Score:            f64(score),
				RankingBasis:     []string{"readiness.coverage_gap"},
				DataCompleteness: contractsv1.ContextFabricCohortDataPartial,
				Outcome:          contractsv1.ContextFabricCohortOutcomeQualified,
			}},
		},
		RenderShapes: []contractsv1.ContextFabricRenderShape{{
			ShapeID: "rs_1", Kind: contractsv1.ContextFabricRenderKindSeries,
			Presentation: contractsv1.ContextFabricRenderPresentationBars,
			SelectedBy:   contractsv1.ContextFabricRenderRuleCohortAttentionScore,
			Title:        "Attention score by team", AxisKind: contractsv1.ContextFabricRenderAxisCategory,
			AxisLabel: "team", ValueLabel: "attention score",
			Series: []contractsv1.ContextFabricRenderSeries{{
				Key: "attention_score", Label: "Attention score",
				Points: []contractsv1.ContextFabricRenderPoint{{
					Label: "ops-team", Value: score,
					Source: contractsv1.ContextFabricRenderPointSource{
						Kind:               contractsv1.ContextFabricRenderSourceCohortMemberScore,
						SubjectCanonicalID: "team:gh:ops-team",
					},
				}},
			}},
		}},
	}
	result.Completeness = contextfabric.ComputeAnswerCompleteness(result)
	return result
}

// TestProjectionCarriesAShapeItsOwnDocumentCanCheck is the control.
func TestProjectionCarriesAShapeItsOwnDocumentCanCheck(t *testing.T) {
	t.Parallel()
	projection := Project(canonicalWithScoreShape(46.7), Budget{})
	if len(projection.RenderShapes) != 1 {
		t.Fatalf("projection carries %d render shapes, want the one the result carried", len(projection.RenderShapes))
	}
	if projection.ProjectionBudget.RenderShapesOmitted != 0 {
		t.Errorf("nothing was dropped, so render_shapes_omitted must be 0; got %d", projection.ProjectionBudget.RenderShapesOmitted)
	}
	if err := projection.Validate(); err != nil {
		t.Fatalf("the projection the surfaces actually return does not validate: %v", err)
	}
}

// TestProjectionDropsAShapeWhoseSourceTheBudgetCut plants the defect: a
// budget that keeps zero cohort members leaves the score shape citing a
// team the reader cannot see.
func TestProjectionDropsAShapeWhoseSourceTheBudgetCut(t *testing.T) {
	t.Parallel()
	// A budget of one member drops the SECOND team -- the one this
	// answer's shape happens to plot -- so the shape now cites a row the
	// reader cannot see.
	result := canonicalWithScoreShape(46.7)
	result.Cohort.Members = append([]contractsv1.ContextFabricCohortMember{{
		Subject:          contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: "team:gh:retained", Label: "retained"},
		Rank:             1,
		InclusionReasons: []string{"in the census"},
	}}, result.Cohort.Members...)
	projection := Project(result, Budget{MaxCohortMembers: 1})
	if len(projection.RenderShapes) != 0 {
		t.Fatalf("a shape citing a dropped cohort member survived into the projection: %+v", projection.RenderShapes)
	}
	if projection.ProjectionBudget.RenderShapesOmitted != 1 {
		t.Fatalf("render_shapes_omitted = %d, want 1 -- a silent drop is the failure this budget exists to prevent", projection.ProjectionBudget.RenderShapesOmitted)
	}
	if !projection.ProjectionBudget.Truncated {
		t.Error("truncated must be set whenever anything was dropped")
	}
	if err := projection.Validate(); err != nil {
		t.Fatalf("the projection must stay valid after dropping a shape: %v", err)
	}
}

// TestProjectionRemainsPureWithRenderShapes: Project is documented as a
// pure function of (result, budget), and the shape carry must not become
// the one place that mutates its input.
func TestProjectionRemainsPureWithRenderShapes(t *testing.T) {
	t.Parallel()
	result := canonicalWithScoreShape(46.7)
	before := result.RenderShapes[0].Series[0].Points[0].Value
	projection := Project(result, Budget{})
	projection.RenderShapes[0].Series[0].Points[0].Value = 99
	if result.RenderShapes[0].Series[0].Points[0].Value != before {
		t.Fatal("mutating the projection's shapes changed the canonical result's own")
	}
}
