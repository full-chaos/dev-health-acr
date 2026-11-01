package contextfabric

import (
	"context"
	"slices"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12348: the graph read's two non-cohort cut rows, through the real
// engine and the real model runtime adapters. The model draft says complete.
//
// What the rows do: the answer carries coverage.partial, the row and its reason.
// What they do not do: lower status. Only a floor in ApplyServedStatusFloors
// lowers a complete status, and these codes have none, so status stays the
// draft's, the same as endpoint_lookup_failed
// (TestAnotherGraphDegradationKeepsItsDraftStatus). The assertions below state
// that, so the day a floor is added it is a deliberate edit here.

func cutReadAnswer(t *testing.T, details ...CoverageDetail) InvestigationResult {
	t.Helper()
	project := acceptanceProject()
	graphContext := bootstrapGraphContext(project)
	graphContext.Coverage = Coverage{Sources: []SourceObservation{}, Partial: len(details) > 0, DegradedReasons: []string{}, Details: details}
	for _, detail := range details {
		graphContext.Coverage.DegradedReasons = append(graphContext.Coverage.DegradedReasons, detail.Raw)
	}
	graph := &acceptanceGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		context:    graphContext,
	}
	facts := factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
		return bootstrapFactBundle(project), nil
	})
	engine := buildAcceptanceEngineWithTelemetry(t, graph, facts, bootstrapInterpretation(), bootstrapDraft(project), nil, &recordingTelemetry{})
	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("served result does not validate: %v", err)
	}
	return result
}

func cutReadDetails() map[string]CoverageDetail {
	return map[string]CoverageDetail{
		"a cut hop walk": detailFixture(contractsv1.ContextFabricCoverageDetailGraphHopWalkTruncated, "context-fabric:graph", true, "hop_walk_truncated", nil),
		"undecided path endpoints": detailFixture(contractsv1.ContextFabricCoverageDetailGraphTeamNarrowingUndecidedPaths, "context-fabric:graph", true,
			"team_narrowing_undecided_paths:2", withCount(2)),
	}
}

func TestACutNonCohortReadServesPartialCoverageAndKeepsItsDraftStatus(t *testing.T) {
	t.Parallel()
	if control := cutReadAnswer(t); control.Status != InvestigationComplete || control.Coverage.Partial {
		t.Fatalf("fixture: with no row the answer is %q, partial=%v, want complete and not partial", control.Status, control.Coverage.Partial)
	}
	for name, detail := range cutReadDetails() {
		t.Run(name, func(t *testing.T) {
			result := cutReadAnswer(t, detail)
			if !result.Coverage.Partial {
				t.Fatal("coverage.partial = false: the read lost candidates")
			}
			if !slices.Contains(result.Coverage.DegradedReasons, detail.Raw) {
				t.Fatalf("degraded_reasons = %v, want %q", result.Coverage.DegradedReasons, detail.Raw)
			}
			served := false
			for _, row := range result.Coverage.Details {
				served = served || (row.Code == detail.Code && row.Degrading && row.Raw == detail.Raw && row.Label == detail.Label)
			}
			if !served {
				t.Fatalf("coverage details = %+v, want the %s row", result.Coverage.Details, detail.Code)
			}
			if result.Status != InvestigationComplete {
				t.Fatalf("status = %q: no floor reads this code, so the draft's complete stands", result.Status)
			}
		})
	}
}

func TestBothCutReadRowsPairWithTheirReasonsThroughTheMerge(t *testing.T) {
	t.Parallel()
	var rows []CoverageDetail
	for _, detail := range cutReadDetails() {
		rows = append(rows, detail)
	}
	result := cutReadAnswer(t, rows...)
	var degrading []string
	for _, row := range result.Coverage.Details {
		if row.Degrading {
			degrading = append(degrading, row.Raw)
		}
	}
	if len(degrading) != 2 || !slices.Equal(degrading, result.Coverage.DegradedReasons) {
		t.Fatalf("degrading rows %v, degraded_reasons %v, want both, paired in order", degrading, result.Coverage.DegradedReasons)
	}
}

// Neither row is a cohort terminal: it says nothing about a member set, so it
// must not turn a model no_match into a served cohort outcome.
func TestACutNonCohortReadIsNotACohortTerminal(t *testing.T) {
	t.Parallel()
	for name, detail := range cutReadDetails() {
		if cohortTerminalCoverageCode(detail.Code) {
			t.Errorf("%s: %s is a cohort terminal code", name, detail.Code)
		}
	}
}
