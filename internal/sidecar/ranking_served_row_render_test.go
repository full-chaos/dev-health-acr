package sidecar_test

import (
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/answerprojection"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

func TestServedProjectRankingRowRendersItsLabelOnce(t *testing.T) {
	t.Parallel()
	score := 46.7
	result := contractsv1.ContextFabricInvestigationResult{
		SchemaVersion:  contractsv1.ContextFabricInvestigationResultSchema,
		ResultID:       "res_served_row",
		RequestID:      "req_served_row",
		GeneratedAt:    time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC),
		Status:         contractsv1.ContextFabricInvestigationPartial,
		Question:       "Which projects need attention?",
		DirectJudgment: "Payments needs attention first.",
		Versions: contractsv1.ContextFabricVersionSet{
			ServiceVersion: "acr-v1", ContractVersion: contractsv1.ContextFabricInvestigationResultSchema, Backend: "graph",
			ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1",
			SynthesisVersion: "synthesis-v1", CanonicalServiceVersion: "ops-v1",
		},
		Cohort: &contractsv1.ContextFabricCohort{
			Kind:      contractsv1.ContextFabricSubjectProject,
			Rationale: "Matched every project in the org.",
			Members: []contractsv1.ContextFabricCohortMember{{
				Subject:          contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: "project:p1", Label: "Payments"},
				Rank:             1,
				InclusionReasons: []string{"in the census"},
				RankingComputed:  true,
				AttentionRank:    1,
				Score:            &score,
				RankingBasis:     []string{"readiness.coverage_gap"},
				DataCompleteness: contractsv1.ContextFabricCohortDataPartial,
				Outcome:          contractsv1.ContextFabricCohortOutcomeQualified,
			}},
		},
	}
	result.Completeness = contextfabric.ComputeAnswerCompleteness(result)
	projection := answerprojection.Project(result, answerprojection.Budget{})
	markdown, _ := sidecar.RenderAnswerProjectionMarkdown(projection, 1<<20)
	if !strings.Contains(markdown, "project canonical id: project:p1; project label: Payments") {
		t.Fatalf("served project row not rendered from subject keys:\n%s", markdown)
	}
	if n := strings.Count(markdown, "project label:"); n != 1 {
		t.Errorf("label printed %d times, want once:\n%s", n, markdown)
	}
	if strings.Contains(markdown, "team label") || strings.Contains(markdown, "team canonical id") {
		t.Errorf("deprecated team keys leaked into the render:\n%s", markdown)
	}
}
