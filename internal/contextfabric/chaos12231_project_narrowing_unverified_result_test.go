package contextfabric

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12231: the graph reader (falkorgraph DiscoverContext) discloses a
// requested project narrowing it could not verify as a degrading
// read_population_unverified detail, source context-fabric:graph, raw
// "project_narrowing_unverified", with no count
// (falkorgraph/chaos12231_project_narrowing_unverified_test.go pins that
// shape). This test carries the same detail through Investigate and proves
// the served answer keeps it, says partial, and validates against the
// existing contract, so the disclosure needs no contract change.
func TestAnUnverifiedProjectNarrowingReachesTheServedAnswer(t *testing.T) {
	t.Parallel()
	project := acceptanceProject()
	raw := "project_narrowing_unverified"
	graphContext := bootstrapGraphContext(project)
	graphContext.Coverage = Coverage{
		Sources:         []SourceObservation{},
		Partial:         true,
		DegradedReasons: []string{raw},
		Details:         []CoverageDetail{detailFixture(contractsv1.ContextFabricCoverageDetailReadPopulationUnverified, "context-fabric:graph", true, raw, nil)},
	}
	graph := &acceptanceGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		context:    graphContext,
	}
	facts := factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
		return bootstrapFactBundle(project), nil
	})
	engine := buildAcceptanceEngineWithTelemetry(t, graph, facts, bootstrapInterpretation(), bootstrapDraft(project), nil, &recordingTelemetry{})
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestedScope.ProjectIDs = []string{"proj-1"}

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("served result does not validate against the existing contract: %v", err)
	}
	if !result.Coverage.Partial {
		t.Fatal("Coverage.Partial = false, want true: the requested project narrowing was not verified")
	}
	if len(result.Coverage.Details) != 1 {
		t.Fatalf("Coverage.Details = %#v, want the one unverified-narrowing detail", result.Coverage.Details)
	}
	detail := result.Coverage.Details[0]
	if detail.Code != contractsv1.ContextFabricCoverageDetailReadPopulationUnverified || !detail.Degrading || detail.Raw != raw || detail.Label == "" {
		t.Fatalf("Coverage.Details[0] = %#v, want the degrading read_population_unverified detail with raw %q and a label", detail, raw)
	}
	if len(result.Coverage.DegradedReasons) != 1 || result.Coverage.DegradedReasons[0] != raw {
		t.Fatalf("Coverage.DegradedReasons = %v, want [%q]", result.Coverage.DegradedReasons, raw)
	}
}
