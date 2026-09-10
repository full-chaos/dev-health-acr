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

	t.Run("cohort_group_authorization, through the grouped turn's group read", func(t *testing.T) {
		// Driven through a REAL grouped turn, not by handing the hint to the
		// port directly: the claim the registry makes is that this string is
		// produced by a production path, and only a turn that actually
		// groups, authorizes and reads can show that.
		recorder := &groupReadRecorder{facts: func(CanonicalFactRequest) CanonicalFactBundle {
			bundle := emptyFactBundle()
			bundle.Facts = groupReadMemberFacts()
			bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
			return bundle
		}}
		engine, request := groupReadEngineFixture(t, &recordingTelemetry{}, recorder)
		if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		graph, ok := engine.graph.(*groupAuthorizingGraph)
		if !ok {
			t.Fatalf("fixture defect: the group-read fixture's graph is %T, so no hint can be read back", engine.graph)
		}
		sources := make([]string, 0, 2)
		for _, hints := range graph.hinted {
			for _, hint := range hints {
				sources = append(sources, hint.Source)
			}
		}
		t.Logf("sources reaching ResolveSubjects = %v", sources)
		found := false
		for _, source := range sources {
			if source == string(hintsource.CohortGroupAuthorization) {
				found = true
			}
		}
		if !found {
			t.Fatalf("the grouped turn's group authorization put %v on the wire, none of which is %q -- either the group read did not run in this fixture (then it measures nothing) or it emits a different string than the registry holds",
				sources, hintsource.CohortGroupAuthorization)
		}
		driven[hintsource.CohortGroupAuthorization] = true
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

// callShapeCapturingReader records the SCOPE ARGUMENTS of every ResolveSubjects
// call — the confirmed kind, the frame and the anchor kind — which is what
// decides whether a contest scope can refuse anything at all.
type callShapeCapturingReader struct {
	resolution      SubjectResolution
	context         GraphContext
	confirmedKinds  []*ConfirmedExpectedKind
	frames          []*QuestionFrame
	anchorKinds     []SubjectKind
	requestedHints  [][]SubjectHint
	resolveCallSeen bool
}

func (r *callShapeCapturingReader) ResolveInvestigationBinding(context.Context, storage.Principal) (ResolvedGraphBinding, error) {
	return ResolvedGraphBinding{GraphKey: "call-shape-key", Epoch: 0}, nil
}

func (r *callShapeCapturingReader) ResolveSubjects(_ context.Context, _ storage.Principal, request InvestigationRequest, _ InterpretedQuestion, _ ResolvedGraphBinding, confirmedKind *ConfirmedExpectedKind, _ *ConfirmedAnchorSelection, frame *QuestionFrame, anchorKind SubjectKind) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	r.resolveCallSeen = true
	r.confirmedKinds = append(r.confirmedKinds, confirmedKind)
	r.frames = append(r.frames, frame)
	r.anchorKinds = append(r.anchorKinds, anchorKind)
	r.requestedHints = append(r.requestedHints, request.RequestedScope.SubjectHints)
	return r.resolution, StructureOfferMaterial{}, nil, nil, nil
}

func (r *callShapeCapturingReader) DiscoverContext(context.Context, storage.Principal, GraphDiscoveryRequest) (GraphContext, error) {
	return r.context, nil
}

// r1 FINDING 2, PERMANENT PIN, and the pin it REPLACES was vacuous.
//
// The answer-reuse recheck's hint is contest-exempt, and that exemption was
// argued from the recheck being unable to reach a refusing scope: its call site
// passes no confirmed kind and no frame. The first version of this pin asserted
// that by calling decideContestScope directly with nils — which proves a
// property of decideContestScope and NOTHING about the call site. Reproduced:
// with the production call at answer_reuse.go mutated to pass a refusing frame
// AND a matching confirmed kind, every PR-B pin still passed, including that
// one.
//
// This asserts the ARGUMENTS THE PRODUCTION CALL ACTUALLY PASSES, captured at
// the graph reader. If that call ever starts supplying a frame or a confirmed
// kind, the exemption starts deciding something real and this fails loudly.
func TestTheReuseRecheckPassesNoScopeToResolution(t *testing.T) {
	t.Parallel()
	_, candidate := reusableCandidate()
	reader := &callShapeCapturingReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
		context: GraphContext{
			Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{}, FactRequirements: []FactRequirement{},
			EvidenceRefIDs: []string{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
	}
	freshResult := validInvestigationResult()
	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: reader,
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
	if !reader.resolveCallSeen {
		t.Fatal("ResolveSubjects was never called; this fixture never reached the recheck and measures nothing")
	}
	// THE CONTROL that makes the assertion mean something: the call being
	// examined must be the recheck's, identified by the source IT mints.
	recheckCall := -1
	for index, hints := range reader.requestedHints {
		for _, hint := range hints {
			if hint.Source == string(hintsource.AnswerReuseAuthorizationRecheck) {
				recheckCall = index
			}
		}
	}
	if recheckCall < 0 {
		t.Fatalf("no ResolveSubjects call carried the recheck's own hint source; hints seen = %v",
			reader.requestedHints)
	}
	t.Logf("recheck call #%d: confirmedKind=%v frame=%v anchorKind=%q",
		recheckCall, reader.confirmedKinds[recheckCall], reader.frames[recheckCall], reader.anchorKinds[recheckCall])
	if reader.confirmedKinds[recheckCall] != nil {
		t.Errorf("the reuse recheck passed a confirmed kind (%+v) to resolution. The recheck's hint is exempt "+
			"from the contest, and that exemption was argued from this call being unable to reach a refusing "+
			"scope. It now can", reader.confirmedKinds[recheckCall])
	}
	if reader.frames[recheckCall] != nil {
		t.Errorf("the reuse recheck passed a frame (%+v) to resolution, for the same reason as above",
			reader.frames[recheckCall])
	}
	if reader.anchorKinds[recheckCall] != "" {
		t.Errorf("the reuse recheck passed a scope anchor kind (%q) to resolution", reader.anchorKinds[recheckCall])
	}
}
