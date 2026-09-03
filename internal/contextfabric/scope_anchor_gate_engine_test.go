package contextfabric

// The refusal gate pinned WHERE IT ACTUALLY RUNS, not only as a pure function.
//
// Round 1 found this: `TestScopeAnchorRetrievalKind_RefusalPaths` tests the
// function in isolation, and every existing GraphReader double discards the
// new trailing parameter, so replacing `ScopeAnchorRetrievalKind(frame, kind)`
// with the raw `familyOutcome.WinningSample.ScopeAnchorKind` at BOTH engine
// call sites left the whole suite green. A refusal that only holds in a unit
// test is not a refusal: the two call sites are the population that matters,
// and nothing observed what they passed.
//
// These drive Engine.Investigate for real and read what the graph reader
// RECEIVED, so the gate cannot be bypassed at either site without a failure.

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// anchorKindCapturingGraph records the scope-anchor kind the engine passed on
// every ResolveSubjects call. It is a separate double from capturingGraphReader
// deliberately: the shared one discards this parameter, and widening it would
// touch dozens of unrelated tests.
type anchorKindCapturingGraph struct {
	resolution   SubjectResolution
	context      GraphContext
	anchorKinds  []SubjectKind
	frames       []*QuestionFrame
	resolveCalls int
}

func (g *anchorKindCapturingGraph) ResolveSubjects(_ context.Context, _ storage.Principal, _ InvestigationRequest, _ InterpretedQuestion, _ ResolvedGraphBinding, _ *ConfirmedExpectedKind, _ *ConfirmedAnchorSelection, frame *QuestionFrame, anchorKind SubjectKind) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	g.resolveCalls++
	g.anchorKinds = append(g.anchorKinds, anchorKind)
	g.frames = append(g.frames, frame)
	return g.resolution, StructureOfferMaterial{}, CommitBasisSet{}, CommitDecisionDigestSet{}, nil
}

func (g *anchorKindCapturingGraph) DiscoverContext(context.Context, storage.Principal, GraphDiscoveryRequest) (GraphContext, error) {
	return g.context, nil
}

func (g *anchorKindCapturingGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (ResolvedGraphBinding, error) {
	return ResolvedGraphBinding{GraphKey: "anchor-gate-key", Epoch: 0}, nil
}

// familyInterpreter is the family-aware double the pure-function tests could
// not use: it returns a QuestionFamilyOutcome carrying BOTH a frame and a
// WinningSample whose ScopeAnchorKind is set, which is exactly the pair the
// gate reads at the engine call sites.
type familyInterpreter struct {
	interpreted InterpretedQuestion
	outcome     QuestionFamilyOutcome
}

func (f familyInterpreter) Interpret(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error) {
	return f.interpreted, f.outcome, nil
}

func anchorGateOutcome(frame *QuestionFrame, anchorKind SubjectKind) QuestionFamilyOutcome {
	return QuestionFamilyOutcome{
		Frame:              frame,
		Family:             QuestionFamilyUnclassified,
		Source:             QuestionFamilySourceModel,
		WinningSampleIndex: 0,
		WinningSample:      FamilySample{ScopeAnchorKind: anchorKind},
	}
}

func runAnchorGateEngine(t *testing.T, frame *QuestionFrame, anchorKind SubjectKind) *anchorKindCapturingGraph {
	t.Helper()
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	graph := &anchorKindCapturingGraph{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		context: GraphContext{
			Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
			FactRequirements: []FactRequirement{{Kind: FactStatus}},
			EvidenceRefIDs:   []string{"evidence_project_status"},
			Coverage:         Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
	}
	interpreted := InterpretedQuestion{
		Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent},
		SubjectTerms: []string{"platform"}, FactRequirements: []FactRequirement{{Kind: FactStatus}},
	}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: familyInterpreter{interpreted: interpreted, outcome: anchorGateOutcome(frame, anchorKind)},
		Graph:       graph,
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "ok",
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			}, nil
		}),
		Results: &resultStoreStub{},
	}, EngineOptions{ServiceVersion: "acr-test", NewResultID: func() string { return "result_12345678" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	_, _ = engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"},
		InvestigationRequest{
			SchemaVersion: InvestigationRequestSchemaV1, RequestID: "request_12345678",
			Question: "Which repositories does the platform team own?", TimeContext: TimeContext{Axis: TemporalCurrent},
			Options:  InvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50, MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true},
			Consumer: ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
		})
	if graph.resolveCalls == 0 {
		t.Fatal("the engine never called ResolveSubjects; this test proves nothing")
	}
	return graph
}

// A REFUSED frame must reach the graph reader as "". Without the gate at the
// call site the raw receipt kind arrives instead, and this fails.
func TestEngine_RefusedAnchorKindNeverReachesRetrieval(t *testing.T) {
	t.Parallel()
	// grouped_members declares no scope anchor: the gate refuses it, and the
	// nil-Scoped guard alone cannot, because Scoped is populated here.
	malformed := &QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{
			Kind:    SubjectExpressionGroupedMembers,
			Grouped: &GroupedSetExpression{GroupKind: SubjectTeam, MemberKind: SubjectProject},
			Scoped:  &ScopedSetExpression{AnchorTerms: []string{"platform"}, MemberKind: SubjectRepository},
		},
	}
	graph := runAnchorGateEngine(t, malformed, SubjectTeam)
	for i, got := range graph.anchorKinds {
		if got != "" {
			t.Errorf("ResolveSubjects call %d received anchor kind %q, want \"\" — the engine bypassed the refusal gate", i, got)
		}
	}
}

// THE POSITIVE CONTROL. Without it every assertion above passes vacuously if
// the engine simply never forwards an anchor kind at all.
func TestEngine_AdmittedAnchorKindReachesRetrieval(t *testing.T) {
	t.Parallel()
	scoped := &QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{
			Kind:   SubjectExpressionChildrenOfScope,
			Scoped: &ScopedSetExpression{AnchorTerms: []string{"platform"}, MemberKind: SubjectRepository},
		},
	}
	graph := runAnchorGateEngine(t, scoped, SubjectTeam)
	sawTeam := false
	for _, got := range graph.anchorKinds {
		if got == SubjectTeam {
			sawTeam = true
		}
	}
	if !sawTeam {
		t.Fatalf("anchor kinds received = %v, want at least one %q — the admitted case never reaches retrieval, so the refusal test above is vacuous", graph.anchorKinds, SubjectTeam)
	}
}

// THE OFFERS-ONLY CALL SITE. Round 2 found that the tests above reach only the
// DECISIVE resolve (engine.go:1502): a ShapeOpen interpretation infers no
// default window, so the class-default gate never fires and the offers-only
// resolve (chaos4234_offers_only.go:146) is never driven. Bypassing the gate
// at THAT site therefore survived everything.
//
// That is the same finding class as the decisive-path one, at the second of
// the two application sites — my own sweep named both sites and then closed
// only one, because the recording double reached one path and the
// parameter-discarding double covered the other. The shared
// acceptanceGraphReader now records the parameter, which is what makes this
// path assertable at all.
func TestEngine_OffersOnlyPathAlsoAppliesTheRefusalGate(t *testing.T) {
	t.Parallel()
	malformed := &QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{
			Kind:    SubjectExpressionGroupedMembers,
			Grouped: &GroupedSetExpression{GroupKind: SubjectTeam, MemberKind: SubjectProject},
			Scoped:  &ScopedSetExpression{AnchorTerms: []string{"platform"}, MemberKind: SubjectRepository},
		},
	}
	interpreter := &countingInterpreter{
		interpretation: bootstrapInterpretation(),
		family:         anchorGateOutcome(malformed, SubjectTeam),
	}
	graph := chaos4234GatedGraph()
	engine := buildWindowGateEngineWithTelemetry(t, interpreter, graph,
		&staticResultStore{results: map[string]InvestigationResult{}}, &recordingTelemetry{})

	// No EvidenceWindow: the class-default gate applies, so the ONLY resolve
	// is the offers-only one.
	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationClarificationRequired {
		t.Fatalf("Status = %q, want clarification_required — the window gate must still hold, or this is not the offers-only path", result.Status)
	}
	// NON-VACUITY: prove this drove the offers-only resolve specifically.
	if graph.resolveCalls != 1 {
		t.Fatalf("resolveCalls = %d, want exactly 1 (the offers-only resolve under the gate); this test is not on the path it claims", graph.resolveCalls)
	}
	if len(graph.receivedAnchorKinds) != 1 {
		t.Fatalf("receivedAnchorKinds = %v, want exactly one recorded call", graph.receivedAnchorKinds)
	}
	if got := graph.receivedAnchorKinds[0]; got != "" {
		t.Errorf("the offers-only resolve received anchor kind %q, want \"\" — the gate is bypassed at chaos4234_offers_only.go", got)
	}
}

// Positive control for the offers-only path: an ADMITTED frame must reach it,
// or the refusal assertion above passes because nothing is ever forwarded.
func TestEngine_OffersOnlyPathForwardsAnAdmittedAnchorKind(t *testing.T) {
	t.Parallel()
	scoped := &QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{
			Kind:   SubjectExpressionChildrenOfScope,
			Scoped: &ScopedSetExpression{AnchorTerms: []string{"platform"}, MemberKind: SubjectRepository},
		},
	}
	interpreter := &countingInterpreter{
		interpretation: bootstrapInterpretation(),
		family:         anchorGateOutcome(scoped, SubjectTeam),
	}
	graph := chaos4234GatedGraph()
	engine := buildWindowGateEngineWithTelemetry(t, interpreter, graph,
		&staticResultStore{results: map[string]InvestigationResult{}}, &recordingTelemetry{})

	if _, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequest()); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(graph.receivedAnchorKinds) == 0 {
		t.Fatal("the offers-only resolve was never called; the refusal test above would be vacuous")
	}
	if got := graph.receivedAnchorKinds[0]; got != SubjectTeam {
		t.Fatalf("offers-only resolve received %q, want %q — an admitted anchor kind must reach this path too", got, SubjectTeam)
	}
}
