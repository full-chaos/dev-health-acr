package contextfabric

import (
	"context"
	"errors"
	"fmt"
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
	saved         InvestigationResult
	savedSnapshot SourceWatermarkSnapshot
	savedEpoch    RebuildEpoch
}

func (s *resultStoreStub) Save(_ context.Context, _ storage.Principal, result InvestigationResult, reuseSnapshot SourceWatermarkSnapshot, reuseEpoch RebuildEpoch, _ string, _ ReuseRetrievalIdentity) error {
	s.saved = result
	s.savedSnapshot = reuseSnapshot
	s.savedEpoch = reuseEpoch
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

func (s *staticResultStore) Save(context.Context, storage.Principal, InvestigationResult, SourceWatermarkSnapshot, RebuildEpoch, string, ReuseRetrievalIdentity) error {
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
				EvidenceRefIDs: []string{}, ClaimedFacts: []ClaimedFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
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
	answerReuseOutcomes         []AnswerReuseOutcome
}

func (r *recordingTelemetry) RecordAnswerReuse(_ context.Context, _ storage.Principal, outcome AnswerReuseOutcome) {
	r.answerReuseOutcomes = append(r.answerReuseOutcomes, outcome)
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
				ClaimedFacts:        []ClaimedFact{},
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
// exact SubjectHint goes through -- see graphrank's
// TestNodeCandidateFiltersUnauthorizedNodesBeforeCandidates),
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

// TestEngineCapsCombinedSubjectHintsAtContractBound is the probe for Codex
// finding G4: a caller already at the v1 contract's RequestedScope bound of
// 50 SubjectHints, combined with Engine appending up to 20 more
// receipt-derived hints, previously produced up to 70 hints with nothing
// capping the total before GraphReader -- and GraphReader's exact-hint path
// itself did not cap to Options.MaxSubjectCandidates either (see the
// graph-adapter-level fix), so an entirely valid request (each part individually
// within its own bound) could still fail deep in the pipeline once
// SubjectResolution.Candidates exceeded the result contract's bound of 50.
// Engine must cap the combined total at the contract bound itself, dropping
// excess receipt-derived hints (never the caller's own explicit hints) and
// counting the drop in the same skip telemetry as any other unresolved
// receipt.
func TestEngineCapsCombinedSubjectHintsAtContractBound(t *testing.T) {
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
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
		context: GraphContext{
			Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{}, FactRequirements: []FactRequirement{},
			EvidenceRefIDs: []string{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
	}
	telemetry := &recordingTelemetry{}
	engine := mustEngineForPriorReceiptTest(t, graph, store, telemetry)

	request := validInvestigationRequest()
	hints := make([]SubjectHint, 0, 50)
	for i := 0; i < 50; i++ {
		hints = append(hints, SubjectHint{Kind: SubjectProject, ID: fmt.Sprintf("project_caller_%d", i), Label: fmt.Sprintf("Caller Project %d", i), Source: "workbench"})
	}
	request.RequestedScope.SubjectHints = hints
	request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: "result_prior_1", ReceiptID: "receipt_abc12345"}}

	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(graph.resolveRequests) != 1 {
		t.Fatalf("resolveRequests = %#v", graph.resolveRequests)
	}
	if got := len(graph.resolveRequests[0].RequestedScope.SubjectHints); got > 50 {
		t.Fatalf("combined subject hints sent to GraphReader = %d, want capped at the v1 contract bound of 50", got)
	}
	if len(telemetry.priorSubjectReceiptsSkipped) != 1 || telemetry.priorSubjectReceiptsSkipped[0] != 1 {
		t.Fatalf("telemetry = %#v, want the capped-out receipt hint counted as a skip", telemetry.priorSubjectReceiptsSkipped)
	}
}

func factKinds(requirements []FactRequirement) []FactKind {
	kinds := make([]FactKind, 0, len(requirements))
	for _, requirement := range requirements {
		kinds = append(kinds, requirement.Kind)
	}
	return kinds
}

// CHAOS-3781 replaces the H6 refusal (Codex adversarial review,
// CHAOS-3755). These tests are the inverse of the ones they succeeded: the
// axes that were refused are now ANSWERED, and what is refused instead is
// narrower -- bounds this service will not read.
//
// The refusal was correct while every layer below read current state only.
// It stopped being correct once the graph gained validity-window admission
// and the fact providers gained time bounds.

// historicalEngineProbe records which capabilities an investigation
// actually reached, so a test can prove a historical question does real
// work rather than being quietly short-circuited.
type historicalEngineProbe struct {
	graph              *countingGraphReader
	interpretedContext TimeContext
	factContext        TimeContext
	factsRead          bool
	synthesized        bool
}

// mustHistoricalEngine builds an engine whose interpreter returns the given
// time context, with `now` late enough that a 2026 as-of is in the past.
func mustHistoricalEngine(t *testing.T, interpretedTime TimeContext, now time.Time) (*Engine, *historicalEngineProbe) {
	t.Helper()
	probe := &historicalEngineProbe{graph: &countingGraphReader{}}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			probe.interpretedContext = interpretedTime
			return InterpretedQuestion{
				Shape: ShapeSingleSubject, RequestedJudgment: "status", TimeContext: interpretedTime,
				FactRequirements: []FactRequirement{{Kind: FactStatus}},
			}, nil
		}),
		Graph: probe.graph,
		Facts: factReaderFunc(func(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
			probe.factsRead = true
			probe.factContext = request.Question.TimeContext
			return CanonicalFactBundle{
				Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			probe.synthesized = true
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "It was on track then.", CurrentState: "Nominal at that time.",
				StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{},
				Paths: []RelationshipPath{}, Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
				ClaimedFacts:        []ClaimedFact{},
				Coverage:            Coverage{Sources: []SourceObservation{{Source: "test", State: SourceAvailable}}, DegradedReasons: []string{}},
				DeterministicAnswer: "It was on track then, based on available context.", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
	}, EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return now }, NewResultID: func() string { return "result_12345678" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine, probe
}

// TestEngineAnswersHistoricalTimeAxes is the direct inverse of the retired
// TestEngineRefusesNonCurrentTimeAxis: every axis that used to be refused
// now produces an answer (AC-3781-1), and that answer states the time it
// speaks for (AC-3781-2).
func TestEngineAnswersHistoricalTimeAxes(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	asOf := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		time TimeContext
	}{
		{"valid time", TimeContext{Axis: TemporalValidTime, AsOf: &asOf}},
		{"observed time", TimeContext{Axis: TemporalObservedTime, AsOf: &asOf}},
		{"range", TimeContext{Axis: TemporalRange, Start: &start, End: &asOf}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			engine, probe := mustHistoricalEngine(t, testCase.time, now)
			request := validInvestigationRequest()
			request.TimeContext = testCase.time

			result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
			if err != nil {
				t.Fatalf("Investigate() error = %v, want a historical question to be answered", err)
			}
			if result.Temporal == nil {
				t.Fatal("a historical answer must state the time it speaks for (AC-3781-2)")
			}
			if result.Temporal.Requested.Axis != testCase.time.Axis {
				t.Fatalf("labeled axis = %q, want %q", result.Temporal.Requested.Axis, testCase.time.Axis)
			}
			// The work must actually happen -- an answer produced by
			// skipping the reads would be no better than the refusal.
			if probe.graph.resolveCalls == 0 || probe.graph.discoverCalls == 0 {
				t.Fatalf("graph was not read (resolve=%d discover=%d)", probe.graph.resolveCalls, probe.graph.discoverCalls)
			}
			if !probe.factsRead || !probe.synthesized {
				t.Fatalf("facts read = %v, synthesized = %v, want both", probe.factsRead, probe.synthesized)
			}
			// And every layer must bind to the SAME time, or one of them
			// is answering a different question than the label claims.
			if probe.factContext.Axis != testCase.time.Axis {
				t.Fatalf("fact request axis = %q, want the interpreted axis %q", probe.factContext.Axis, testCase.time.Axis)
			}
			if len(result.Limitations) == 0 {
				t.Fatal("a historical answer must disclose that deleted subjects are unrecoverable")
			}
		})
	}
}

// TestEngineRefusesUnanswerableTimeBounds pins what DID survive the
// refusal's removal: a time in the future is a prediction (§19.8.3 puts
// those out of scope), and a range wider than this service reads is not
// answerable. Both must be caught before any capability runs, for the same
// reason the old refusal was: doing the work and discarding it still costs
// the caller and still hits the graph and the model.
func TestEngineRefusesUnanswerableTimeBounds(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	future := now.Add(48 * time.Hour)
	ancient := now.Add(-3000 * 24 * time.Hour)
	cases := []struct {
		name string
		time TimeContext
	}{
		{"as-of in the future", TimeContext{Axis: TemporalValidTime, AsOf: &future}},
		{"observed time in the future", TimeContext{Axis: TemporalObservedTime, AsOf: &future}},
		{"range ending in the future", TimeContext{Axis: TemporalRange, Start: &now, End: &future}},
		{"range wider than the supported window", TimeContext{Axis: TemporalRange, Start: &ancient, End: &now}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			engine, probe := mustHistoricalEngine(t, TimeContext{Axis: TemporalCurrent}, now)
			request := validInvestigationRequest()
			request.TimeContext = testCase.time

			_, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
			if !errors.Is(err, ErrInvalidTimeBound) {
				t.Fatalf("Investigate() error = %v, want ErrInvalidTimeBound", err)
			}
			if probe.graph.resolveCalls != 0 || probe.factsRead || probe.synthesized {
				t.Fatal("a rejected investigation must do no graph, fact, or synthesis work")
			}
		})
	}
}

// TestEngineAllowsCurrentTimeAxis is the over-blocking guard: rejecting
// everything would also satisfy the test above.
func TestEngineAllowsCurrentTimeAxis(t *testing.T) {
	t.Parallel()
	request := validInvestigationRequest()
	if request.TimeContext.Axis != TemporalCurrent {
		t.Fatalf("fixture axis = %q, want the shared fixture to be a current-time request", request.TimeContext.Axis)
	}
	engine := mustEngineForPriorReceiptTest(t, graphReaderStub{}, nil, nil)
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if errors.Is(err, ErrInvalidTimeBound) {
		t.Fatalf("Investigate() error = %v, want a current-time request to not be rejected", err)
	}
	if err == nil && result.Temporal != nil {
		t.Fatal("a current-axis answer must carry no temporal label")
	}
}

// TestEngineBindsTheInterpretedHistoricalAxis is the P2 probe (codex delta
// review, CHAOS-3755), inverted. The wire request says current; only the
// interpreter, reading the question itself, concludes the caller is asking
// about the past.
//
// Before CHAOS-3781 that combination was refused. Now it is answered -- and
// the thing that must hold is stronger: the INTERPRETED axis, not the wire
// axis, is what every layer below binds to and what the answer's label
// states. If the wire axis won instead, an "as of last month" question
// would silently be answered with current data under a current label,
// which is the same defect the refusal existed to prevent.
func TestEngineBindsTheInterpretedHistoricalAxis(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	asOf := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		time TimeContext
	}{
		{"interpreted as valid time", TimeContext{Axis: TemporalValidTime, AsOf: &asOf}},
		{"interpreted as observed time", TimeContext{Axis: TemporalObservedTime, AsOf: &asOf}},
		{"interpreted as a range", TimeContext{Axis: TemporalRange, Start: &start, End: &asOf}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			engine, probe := mustHistoricalEngine(t, testCase.time, now)
			request := validInvestigationRequest()
			if request.TimeContext.Axis != TemporalCurrent {
				t.Fatalf("fixture axis = %q, want current so this test exercises the interpretation path", request.TimeContext.Axis)
			}

			result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
			if err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			if result.Temporal == nil {
				t.Fatal("an interpreted-historical question produced an answer with no temporal label; that is the unlabeled historical answer this issue removes")
			}
			if result.Temporal.Requested.Axis != testCase.time.Axis {
				t.Fatalf("labeled axis = %q, want the INTERPRETED axis %q, not the wire axis", result.Temporal.Requested.Axis, testCase.time.Axis)
			}
			if probe.factContext.Axis != testCase.time.Axis {
				t.Fatalf("fact request axis = %q, want the interpreted axis %q", probe.factContext.Axis, testCase.time.Axis)
			}
		})
	}
}

// countingGraphReader records whether the engine reached the graph at all.
type countingGraphReader struct {
	resolveCalls  int
	discoverCalls int
}

// ResolveSubjects COMMITS a subject, which is load-bearing rather than
// incidental. The tests built on this double assert that the fact read and
// the synthesis actually ran, and under CHAOS-3810 an investigation that
// commits nothing terminates in clarification_required/no_match before
// reaching either. Committing nothing here used to reach the fact read
// anyway -- precisely the defect CHAOS-3810 removed -- so a double that
// commits nothing can no longer express "the engine did the work".
func (g *countingGraphReader) ResolveSubjects(context.Context, storage.Principal, InvestigationRequest, InterpretedQuestion) (SubjectResolution, error) {
	g.resolveCalls++
	return SubjectResolution{
		Candidates: []SubjectCandidate{},
		Committed:  []SubjectRef{{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}},
	}, nil
}

func (g *countingGraphReader) DiscoverContext(context.Context, storage.Principal, GraphDiscoveryRequest) (GraphContext, error) {
	g.discoverCalls++
	return GraphContext{}, nil
}
