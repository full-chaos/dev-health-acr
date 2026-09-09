package contextfabric

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// AN EXECUTED PRODUCTION DRIVER FOR EVERY ENUMERATED MEMBER.
//
// The enumeration is only worth anything if each member is a string the
// production path ACTUALLY puts on the wire to resolution. These two subtests
// drive the two producers through Engine.Investigate and read the source off
// the request the engine handed to ResolveSubjects — not off the constant, and
// not off the producer's source code.
//
// Asserting through the captured request rather than the constant is the point:
// a change that registered a member and then emitted something else would pass
// any test written against the constant alone.
func TestEveryEnumeratedHintSourceHasAnExecutedProductionDriver(t *testing.T) {
	t.Parallel()
	driven := map[hintsource.Source]bool{}

	t.Run("prior_subject_receipt, through the receipt redemption", func(t *testing.T) {
		project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
		priorResult := validInvestigationResult()
		priorResult.ResultID = "result_prior_1"
		priorResult.SubjectResolution = SubjectResolution{
			Candidates: []SubjectCandidate{{
				ReceiptID: "receipt_abc12345", Subject: project, State: ResolutionCommitted,
				MatchReasons: []string{"Exact canonical subject hint matched the organization graph."}, Confidence: 1,
			}},
			Committed: []SubjectRef{project},
		}
		store := &staticResultStore{results: map[string]InvestigationResult{"result_prior_1": priorResult}}
		graph := &capturingGraphReader{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
			context: GraphContext{
				Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{}, FactRequirements: []FactRequirement{},
				EvidenceRefIDs: []string{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			},
		}
		engine := mustEngineForPriorReceiptTest(t, graph, store, &recordingTelemetry{})
		request := validInvestigationRequest()
		request.Question = "What about it now?"
		request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: "result_prior_1", ReceiptID: "receipt_abc12345"}}
		if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		sources := capturedHintSources(t, graph)
		t.Logf("sources reaching ResolveSubjects = %v", sources)
		if len(sources) != 1 || sources[0] != string(hintsource.PriorSubjectReceipt) {
			t.Fatalf("the receipt redemption put %v on the wire, want exactly [%q]",
				sources, hintsource.PriorSubjectReceipt)
		}
		driven[hintsource.PriorSubjectReceipt] = true
	})

	t.Run("answer_reuse_authorization_recheck, through the reuse recheck", func(t *testing.T) {
		project, candidate := reusableCandidate()
		graph := &capturingGraphReader{
			// EMPTY committed: the recheck then reports an authorization
			// miss, which is fine — this subtest measures the SOURCE the
			// recheck sent, not whether the reuse succeeded.
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
			context: GraphContext{
				Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{}, FactRequirements: []FactRequirement{},
				EvidenceRefIDs: []string{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			},
		}
		freshResult := validInvestigationResult()
		engine := mustReuseTestEngine(t, EngineDependencies{
			Graph: graph,
			Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
				return CanonicalFactBundle{}, nil
			}),
			Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
				return freshResult, nil
			}),
			Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
				return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
			}),
			Results:   &resultStoreStub{},
			Telemetry: &recordingTelemetry{},
			ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
				return candidate, true, nil
			}),
		})
		if _, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest()); err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		sources := capturedHintSources(t, graph)
		t.Logf("subject=%v sources reaching ResolveSubjects = %v", project.CanonicalID, sources)
		found := false
		for _, source := range sources {
			if source == string(hintsource.AnswerReuseAuthorizationRecheck) {
				found = true
			}
		}
		if !found {
			t.Fatalf("the reuse recheck put %v on the wire, none of which is %q — either the recheck did not "+
				"run in this fixture (then it measures nothing) or it emits a different string than the "+
				"registry holds", sources, hintsource.AnswerReuseAuthorizationRecheck)
		}
		driven[hintsource.AnswerReuseAuthorizationRecheck] = true
	})

	// THE CLOSURE CHECK. A member added to the registry with no driver above
	// is an unmeasured member, and the enumeration would then be describing
	// something no test has ever seen produced.
	for _, source := range hintsource.All() {
		if !driven[source] {
			t.Errorf("registry member %q has no executed production driver in this test", source)
		}
	}
}

func capturedHintSources(t *testing.T, graph *capturingGraphReader) []string {
	t.Helper()
	if len(graph.resolveRequests) == 0 {
		t.Fatal("ResolveSubjects was never called; the driver did not reach the producer")
	}
	sources := make([]string, 0, 2)
	for _, request := range graph.resolveRequests {
		for _, hint := range request.RequestedScope.SubjectHints {
			sources = append(sources, hint.Source)
		}
	}
	return sources
}
