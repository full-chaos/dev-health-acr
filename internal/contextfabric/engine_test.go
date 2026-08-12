package contextfabric

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type interpreterFunc func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error)

func (f interpreterFunc) Interpret(ctx context.Context, principal storage.Principal, request InvestigationRequest) (InterpretedQuestion, error) {
	return f(ctx, principal, request)
}

type graphReaderStub struct {
	resolution SubjectResolution
	context    GraphContext
}

func (g graphReaderStub) ResolveSubjects(context.Context, storage.Principal, InvestigationRequest, InterpretedQuestion) (SubjectResolution, error) {
	return g.resolution, nil
}

func (g graphReaderStub) DiscoverContext(context.Context, storage.Principal, GraphDiscoveryRequest) (GraphContext, error) {
	return g.context, nil
}

type factReaderFunc func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error)

func (f factReaderFunc) ReadFacts(ctx context.Context, principal storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
	return f(ctx, principal, request)
}

type synthesizerFunc func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error)

func (f synthesizerFunc) Synthesize(ctx context.Context, principal storage.Principal, input SynthesisInput) (InvestigationResult, error) {
	return f(ctx, principal, input)
}

type resultStoreStub struct {
	saved InvestigationResult
}

func (s *resultStoreStub) Save(_ context.Context, _ storage.Principal, result InvestigationResult) error {
	s.saved = result
	return nil
}

func (s *resultStoreStub) Get(context.Context, storage.Principal, string) (InvestigationResult, error) {
	return InvestigationResult{}, nil
}

// staticResultStore is a resultStoreStub with a keyed Get, for exercising
// prior-subject-receipt resolution.
type staticResultStore struct {
	results map[string]InvestigationResult
	gotIDs  []string
	getErr  error
}

func (s *staticResultStore) Save(context.Context, storage.Principal, InvestigationResult) error {
	return nil
}

func (s *staticResultStore) Get(_ context.Context, _ storage.Principal, resultID string) (InvestigationResult, error) {
	s.gotIDs = append(s.gotIDs, resultID)
	if s.getErr != nil {
		return InvestigationResult{}, s.getErr
	}
	result, ok := s.results[resultID]
	if !ok {
		return InvestigationResult{}, errors.New("investigation result not found")
	}
	return result, nil
}

// capturingGraphReader records every request ResolveSubjects/DiscoverContext
// were called with, so a test can assert what the Engine expanded the
// caller's request into (e.g. prior-subject-receipt hints) without the
// GraphReader itself needing to know about receipts.
type capturingGraphReader struct {
	resolution      SubjectResolution
	context         GraphContext
	resolveRequests []InvestigationRequest
	discoverHints   [][]SubjectHint
}

func (g *capturingGraphReader) ResolveSubjects(_ context.Context, _ storage.Principal, request InvestigationRequest, _ InterpretedQuestion) (SubjectResolution, error) {
	g.resolveRequests = append(g.resolveRequests, request)
	return g.resolution, nil
}

func (g *capturingGraphReader) DiscoverContext(_ context.Context, _ storage.Principal, request GraphDiscoveryRequest) (GraphContext, error) {
	g.discoverHints = append(g.discoverHints, request.Request.RequestedScope.SubjectHints)
	return g.context, nil
}

func TestEngineInvestigatesNovelQuestionThroughComposableCapabilities(t *testing.T) {
	t.Parallel()

	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	resolution := SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}
	interpretation := InterpretedQuestion{
		Shape: ShapeOpen, RequestedJudgment: "release_readiness_and_drivers", TimeContext: TimeContext{Axis: TemporalCurrent},
		FactRequirements: []FactRequirement{{Kind: FactStatus}, {Kind: FactReadiness}},
	}
	store := &resultStoreStub{}
	var observedFactRequest CanonicalFactRequest
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(_ context.Context, _ storage.Principal, request InvestigationRequest) (InterpretedQuestion, error) {
			if !strings.Contains(request.Question, "why can’t") {
				t.Fatalf("question = %q, want novel phrasing", request.Question)
			}
			return interpretation, nil
		}),
		Graph: graphReaderStub{
			resolution: resolution,
			context: GraphContext{
				Paths:            []RelationshipPath{},
				DriverCandidates: []DriverJudgment{},
				FactRequirements: []FactRequirement{{Kind: FactBlockers}, {Kind: FactReadiness}},
				EvidenceRefIDs:   []string{"evidence_project_status"},
				Coverage:         Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			},
		},
		Facts: factReaderFunc(func(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
			observedFactRequest = request
			return CanonicalFactBundle{
				Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, input SynthesisInput) (InvestigationResult, error) {
			if len(input.Graph.Resolution.Committed) != 1 || input.Graph.Resolution.Committed[0] != project {
				t.Fatalf("synthesis subjects = %#v, want %#v", input.Graph.Resolution.Committed, project)
			}
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Ask Dev is not ready to ship.",
				CurrentState: "Release-readiness blockers remain.", StrongestPressures: []string{},
				Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{},
				Paths: []RelationshipPath{}, Conflicts: []Finding{}, Limitations: []string{},
				EvidenceRefIDs: []string{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "Ask Dev is not ready to ship because release-readiness blockers remain.", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Results: store,
	}, EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(100, 0).UTC() }, NewResultID: func() string { return "result_12345678" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	request := validInvestigationRequest()
	request.Question = "Most of it is closed, so why can’t this thing actually ship?"
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.ResultID != "result_12345678" {
		t.Fatalf("ResultID = %q", result.ResultID)
	}
	if result.Versions.ServiceVersion != "acr-test" || result.Versions.CanonicalServiceVersion != "ops-v1" {
		t.Fatalf("versions = %#v", result.Versions)
	}
	if !reflect.DeepEqual(result, store.saved) {
		t.Fatal("saved result does not match returned result")
	}
	if !reflect.DeepEqual(observedFactRequest.Subjects, []SubjectRef{project}) {
		t.Fatalf("fact subjects = %#v", observedFactRequest.Subjects)
	}
	wantKinds := []FactKind{FactStatus, FactReadiness, FactBlockers}
	if got := factKinds(observedFactRequest.Requirements); !reflect.DeepEqual(got, wantKinds) {
		t.Fatalf("fact requirement kinds = %#v, want %#v", got, wantKinds)
	}
}

func TestNewEngineRequiresAllCoreCapabilities(t *testing.T) {
	t.Parallel()

	_, err := NewEngine(EngineDependencies{}, EngineOptions{ServiceVersion: "test", NewResultID: func() string { return "result_1" }})
	if err == nil || !strings.Contains(err.Error(), "requires interpreter, graph, facts, and synthesizer") {
		t.Fatalf("NewEngine() error = %v", err)
	}
}

// recordingTelemetry is a fake EngineTelemetry that records only the counts
// it was called with, so a test can assert a skip was observed without the
// production code ever having a content-bearing telemetry sink to leak
// through.
type recordingTelemetry struct {
	priorSubjectReceiptsSkipped []int
}

func (r *recordingTelemetry) RecordPriorSubjectReceiptsSkipped(_ context.Context, _ storage.Principal, skipped int) {
	r.priorSubjectReceiptsSkipped = append(r.priorSubjectReceiptsSkipped, skipped)
}

func mustEngineForPriorReceiptTest(t *testing.T, graph GraphReader, store InvestigationResultStore, telemetry EngineTelemetry) *Engine {
	t.Helper()
	interpretation := InterpretedQuestion{
		Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent},
		FactRequirements: []FactRequirement{{Kind: FactStatus}},
	}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return interpretation, nil
		}),
		Graph: graph,
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Ask Dev is on track.", CurrentState: "Nominal.",
				StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{},
				Paths: []RelationshipPath{}, Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
				Coverage:            Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "Ask Dev is on track based on available context.", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Results: store, Telemetry: telemetry,
	}, EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(200, 0).UTC() }, NewResultID: func() string { return "result_99999999" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

// TestEngineResolvesPriorSubjectReceiptIntoSubjectHint proves conversational
// follow-up binding: a PriorSubjectReceipts entry naming a subject a prior
// turn already resolved (e.g. "what about it now?") must reach GraphReader
// as an exact SubjectHint on both ResolveSubjects and DiscoverContext, so
// the same authorization/candidate path used for any other exact hint
// re-verifies it -- nothing about the receipt is trusted outright.
func TestEngineResolvesPriorSubjectReceiptIntoSubjectHint(t *testing.T) {
	t.Parallel()
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
	telemetry := &recordingTelemetry{}
	engine := mustEngineForPriorReceiptTest(t, graph, store, telemetry)

	request := validInvestigationRequest()
	request.Question = "What about it now?"
	request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: "result_prior_1", ReceiptID: "receipt_abc12345"}}

	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(store.gotIDs) != 1 || store.gotIDs[0] != "result_prior_1" {
		t.Fatalf("store.gotIDs = %#v", store.gotIDs)
	}
	if len(graph.resolveRequests) != 1 {
		t.Fatalf("resolveRequests = %#v", graph.resolveRequests)
	}
	hints := graph.resolveRequests[0].RequestedScope.SubjectHints
	if len(hints) != 1 || hints[0].Kind != SubjectProject || hints[0].ID != "project_ask_dev" || hints[0].Source != "prior_subject_receipt" {
		t.Fatalf("subject hints = %#v", hints)
	}
	if len(graph.discoverHints) != 1 || len(graph.discoverHints[0]) != 1 {
		t.Fatalf("DiscoverContext did not receive the same expanded hints: %#v", graph.discoverHints)
	}
	// The caller's own request must not be mutated by the expansion.
	if len(request.RequestedScope.SubjectHints) != 0 {
		t.Fatalf("caller request was mutated: %#v", request.RequestedScope.SubjectHints)
	}
	// The receipt survived graph resolution, so nothing was skipped.
	if len(telemetry.priorSubjectReceiptsSkipped) != 0 {
		t.Fatalf("telemetry = %#v, want no skip recorded when the receipt resolved successfully", telemetry.priorSubjectReceiptsSkipped)
	}
}

// TestEngineSkipsUnresolvablePriorSubjectReceiptWithoutFailing proves a
// stale or foreign receipt (its prior InvestigationResult cannot be loaded
// at all) degrades to "not bound" rather than failing the investigation or
// being trusted outright, and that the skip is still observable as a count
// via EngineTelemetry.
func TestEngineSkipsUnresolvablePriorSubjectReceiptWithoutFailing(t *testing.T) {
	t.Parallel()
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	store := &staticResultStore{getErr: errors.New("investigation result not found or unauthorized")}
	graph := &capturingGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		context: GraphContext{
			Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{}, FactRequirements: []FactRequirement{},
			EvidenceRefIDs: []string{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
	}
	telemetry := &recordingTelemetry{}
	engine := mustEngineForPriorReceiptTest(t, graph, store, telemetry)

	request := validInvestigationRequest()
	request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: "result_missing", ReceiptID: "receipt_missing1"}}

	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v, want an unresolvable receipt to degrade safely, not fail", err)
	}
	if len(graph.resolveRequests) != 1 || len(graph.resolveRequests[0].RequestedScope.SubjectHints) != 0 {
		t.Fatalf("resolveRequests = %#v, want no hint added for an unresolvable receipt", graph.resolveRequests)
	}
	if len(telemetry.priorSubjectReceiptsSkipped) != 1 || telemetry.priorSubjectReceiptsSkipped[0] != 1 {
		t.Fatalf("telemetry = %#v, want exactly one skip of count 1 recorded", telemetry.priorSubjectReceiptsSkipped)
	}
}

// TestEngineDoesNotLeakOrErrorWhenPriorSubjectReceiptFailsGraphAuthorization
// is the direct proof requested for the receipt path: a receipt that
// resolves to a real prior subject, but whose subject does not survive
// GraphReader's own authorization check (the same silent-drop behavior any
// exact SubjectHint goes through -- see
// zepgraph's TestResolveSubjectsFiltersUnauthorizedNodesBeforeCandidates),
// must not leak that subject into the investigation, must not error, and
// must still surface as a content-safe skip count.
func TestEngineDoesNotLeakOrErrorWhenPriorSubjectReceiptFailsGraphAuthorization(t *testing.T) {
	t.Parallel()
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
	// The graph reader represents authorization now rejecting the
	// receipt-derived subject: an empty resolution, exactly what
	// GraphReader.ResolveSubjects returns when its own exact-hint
	// authorization check silently drops an unauthorized hint.
	graph := &capturingGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
		context: GraphContext{
			Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{}, FactRequirements: []FactRequirement{},
			EvidenceRefIDs: []string{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
	}
	telemetry := &recordingTelemetry{}
	engine := mustEngineForPriorReceiptTest(t, graph, store, telemetry)

	request := validInvestigationRequest()
	request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: "result_prior_1", ReceiptID: "receipt_abc12345"}}

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v, want an authorization-denied receipt to degrade safely, not fail", err)
	}
	if len(result.SubjectResolution.Committed) != 0 {
		t.Fatalf("result subject resolution = %#v, want the denied subject to never leak into the result", result.SubjectResolution)
	}
	if len(telemetry.priorSubjectReceiptsSkipped) != 1 || telemetry.priorSubjectReceiptsSkipped[0] != 1 {
		t.Fatalf("telemetry = %#v, want exactly one skip of count 1 recorded", telemetry.priorSubjectReceiptsSkipped)
	}
}

func factKinds(requirements []FactRequirement) []FactKind {
	kinds := make([]FactKind, 0, len(requirements))
	for _, requirement := range requirements {
		kinds = append(kinds, requirement.Kind)
	}
	return kinds
}
