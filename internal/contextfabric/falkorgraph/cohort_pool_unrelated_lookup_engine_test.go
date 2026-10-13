package falkorgraph

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type twoCommittedGraphReader struct {
	*Adapter
	committed []contextfabric.SubjectRef
}

func (g twoCommittedGraphReader) ResolveSubjects(
	_ context.Context, _ storage.Principal, _ contextfabric.InvestigationRequest, _ contextfabric.InterpretedQuestion,
	_ contextfabric.ResolvedGraphBinding, _ *contextfabric.ConfirmedExpectedKind, _ *contextfabric.ConfirmedAnchorSelection,
	_ *contextfabric.QuestionFrame, _ contextfabric.SubjectKind,
) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	bases := contextfabric.CommitBasisSet{}
	candidates := []contextfabric.SubjectCandidate{}
	for _, subject := range g.committed {
		candidates = append(candidates, contextfabric.SubjectCandidate{
			ReceiptID: "receipt_" + subject.CanonicalID, Subject: subject, State: contextfabric.ResolutionCommitted,
			MatchedTerms: []string{"Platform"}, MatchReasons: []string{"matched"}, Confidence: 1, EvidenceRefIDs: []string{},
		})
		bases.Record(subject, contextfabric.CommitBasisAuthoritativeIdentity)
	}
	return contextfabric.SubjectResolution{Candidates: candidates, Committed: g.committed},
		contextfabric.StructureOfferMaterial{}, bases, nil, nil
}

type anchoredInterpreter struct{ framedInterpreter }

func (a anchoredInterpreter) Interpret(ctx context.Context, p storage.Principal, r contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.QuestionFamilyOutcome, error) {
	interpreted, outcome, err := a.framedInterpreter.Interpret(ctx, p, r)
	outcome.WinningSample.ScopeAnchorKind = contextfabric.SubjectTeam
	return interpreted, outcome, err
}

type capturingSynthesizer struct {
	countingSynthesizer
	seen *contextfabric.GraphContext
}

func (c capturingSynthesizer) Synthesize(ctx context.Context, p storage.Principal, in contextfabric.SynthesisInput) (contextfabric.InvestigationResult, error) {
	*c.seen = in.Graph
	return c.countingSynthesizer.Synthesize(ctx, p, in)
}

func TestServedAnchoredCohortIsCompleteWhileTheUnrelatedFailedLookupStaysDisclosed(t *testing.T) {
	t.Parallel()
	fixtures, _ := teamWalkFixtures()
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, withUnrelatedFailingWalk(fixtures[TeamAnchorWalkMembers]), telemetry)
	graph := twoCommittedGraphReader{Adapter: adapter, committed: []contextfabric.SubjectRef{
		{Kind: contextfabric.SubjectTeam, CanonicalID: "team:platform", Label: "Platform"},
		{Kind: contextfabric.SubjectProject, CanonicalID: unrelatedSubjectID, Label: "Unrelated"},
	}}
	var synthesisGraph contextfabric.GraphContext
	frame := repositoriesOfAnchorFrame("Platform")
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: anchoredInterpreter{framedInterpreter{
			interpreted: contextfabric.InterpretedQuestion{
				Shape: contextfabric.ShapeDiscoveredCohort, RequestedJudgment: "repositories",
				TimeContext:      contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
				FactRequirements: []contextfabric.FactRequirement{},
			},
			frame: frame,
		}},
		Graph: graph, Facts: emptyFactReader{}, Synthesizer: capturingSynthesizer{seen: &synthesisGraph}, Results: discardingResultStore{},
		Requirements: productionRequirementDeriver{},
	}, contextfabric.EngineOptions{ServiceVersion: "acr-test", NewResultID: func() string { return "result_unrelated_lookup" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_unrelated_lookup",
		Question: "which repositories does Platform own",
		TimeContext: contextfabric.TimeContext{
			Axis:           contextfabric.TemporalCurrent,
			EvidenceWindow: &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D},
		},
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
		},
		Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
	})
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Cohort == nil || len(result.Cohort.Members) != 10 {
		t.Fatalf("served cohort = %+v status=%s lim=%v ans=%q res=%+v", result.Cohort, result.Status, result.Limitations, result.DeterministicAnswer, result.SubjectResolution)
	}
	if result.Cohort.Truncated || !result.Cohort.Complete {
		t.Errorf("served cohort Truncated=%v Complete=%v, want a complete cohort", result.Cohort.Truncated, result.Cohort.Complete)
	}
	if !synthesisGraph.Coverage.Partial || !strings.Contains(strings.Join(synthesisGraph.Coverage.DegradedReasons, ","), "endpoint_lookup_failed:1") {
		t.Errorf("graph coverage handed to synthesis = %+v, want Partial with endpoint_lookup_failed:1", synthesisGraph.Coverage)
	}
	if len(telemetry.neighborLookupFailures) != 1 || telemetry.neighborLookupFailures[0].originCanonicalID != unrelatedSubjectID {
		t.Errorf("failure lines = %+v, want one naming the unrelated subject", telemetry.neighborLookupFailures)
	}
}
