package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type interpreterFunc func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error)

// Interpret adapts the 2-return test double to QuestionInterpreter's
// 3-return contract (CHAOS-4634 S4), uniformly reporting
// QuestionFamilySourceNone/unclassified -- honest for a double that does
// no family resolution at all, and safe by construction: unclassified's
// ApplicableAxes is every axis, so GateOffersByFamily never restricts
// anything for it, which is exactly byte-identical to every one of this
// double's dozens of pre-S4 callers. A test that needs to exercise real
// family gating uses countingInterpreter (chaos4040_window_gate_test.go)
// or RuntimeQuestionInterpreter directly instead.
func (f interpreterFunc) Interpret(ctx context.Context, principal storage.Principal, request InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error) {
	question, err := f(ctx, principal, request)
	return question, QuestionFamilyOutcome{Family: QuestionFamilyUnclassified, Source: QuestionFamilySourceNone}, err
}

type graphReaderStub struct {
	resolution SubjectResolution
	context    GraphContext
	// material (CHAOS-3900 P1.C) is the StructureOfferMaterial
	// ResolveSubjects returns alongside resolution -- zero value by
	// default (every pre-P1.C caller of this stub gets an empty block,
	// byte-identical to before this field existed), settable per test
	// that specifically exercises structure-offer composition.
	material StructureOfferMaterial
	// bases (CHAOS-4085) is the CommitBasisSet this double reports for what
	// it commits. Nil means CommitBasisUnknown for every subject, which is
	// the STRICT reading: the commit-affirmation gate then requires the
	// synthesized answer to support each committed subject.
	//
	// A test whose subject matter is NOT the commit gate sets this to
	// provenCommitBases(...) so its fixture keeps committing what it always
	// committed. That is deliberately EXPLICIT at each such site rather
	// than defaulted here: a silent proven default would switch the gate
	// off across the whole package, and the next test to need real gate
	// behavior would get it silently wrong.
	bases CommitBasisSet
}

// provenCommitBases marks every listed subject as committed on a proven
// identity (CommitBasisCallerCanonicalID -- the caller named it by
// canonical id), exempting it from CHAOS-4085's affirmation gate.
//
// For fixtures that model a graph which authoritatively returns THE
// subject rather than a ranked, scored guess. It is the honest basis for
// such a double: nothing about these harnesses involves a relevance
// comparison, which is the only thing the affirmation gate is about.
func provenCommitBases(subjects ...SubjectRef) CommitBasisSet {
	bases := make(CommitBasisSet, len(subjects))
	for _, subject := range subjects {
		bases.Record(subject, CommitBasisCallerCanonicalID)
	}
	return bases
}

func (g graphReaderStub) ResolveInvestigationBinding(context.Context, storage.Principal) (ResolvedGraphBinding, error) {
	return ResolvedGraphBinding{GraphKey: "stub-key", Epoch: 0}, nil
}

func (g graphReaderStub) ResolveSubjects(context.Context, storage.Principal, InvestigationRequest, InterpretedQuestion, ResolvedGraphBinding, *ConfirmedExpectedKind, *ConfirmedAnchorSelection) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	// CHAOS-4085: g.bases, nil unless the fixture set it -- see the field's
	// own doc comment. Nil reads back as CommitBasisUnknown for every
	// subject, the strict (must-be-affirmed) treatment. CHAOS-4087: nil
	// CommitDecisionDigestSet, the identical nil-is-safe convention.
	return g.resolution, g.material, g.bases, nil, nil
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

func (s *resultStoreStub) Save(_ context.Context, _ storage.Principal, result InvestigationResult, reuseSnapshot SourceWatermarkSnapshot, reuseEpoch RebuildEpoch, _ string, _ ReuseRetrievalIdentity, _ ReusePromptVersions, _ ReuseVersionAuthorities, _ int64) error {
	s.saved = result
	s.savedSnapshot = reuseSnapshot
	s.savedEpoch = reuseEpoch
	return nil
}

func (s *resultStoreStub) Get(context.Context, storage.Principal, string) (StoredInvestigationResult, error) {
	return StoredInvestigationResult{}, nil
}

// staticResultStore is a resultStoreStub with a keyed Get, for exercising
// prior-subject-receipt resolution.
type staticResultStore struct {
	results map[string]InvestigationResult
	gotIDs  []string
	getErr  error
	// graphEpoch (CHAOS-3898 §2.4), when non-nil, is the GraphEpoch every
	// Get response carries. Defaults to 0 when left nil at construction --
	// see the zero-value fallback in Get below -- matching every
	// GraphReader test fake's own default ResolveInvestigationBinding
	// epoch, so existing prior-subject-receipt tests keep passing the
	// CHAOS-3898 §2.2 ingress taint gate unchanged. A test exercising the
	// taint strip itself sets this to a different value explicitly.
	graphEpoch *int64
	// saved (codex round 2, CHAOS-4040): records the LAST result Save
	// received, so a test exercising a gate/terminal that saves alongside
	// its keyed Get (prior-subject-receipt resolution) can assert
	// persistence actually happened, not merely that Save returned no
	// error -- Save previously discarded its argument entirely.
	saved *InvestigationResult
}

func (s *staticResultStore) Save(_ context.Context, _ storage.Principal, result InvestigationResult, _ SourceWatermarkSnapshot, _ RebuildEpoch, _ string, _ ReuseRetrievalIdentity, _ ReusePromptVersions, _ ReuseVersionAuthorities, _ int64) error {
	s.saved = &result
	return nil
}

func (s *staticResultStore) Get(_ context.Context, _ storage.Principal, resultID string) (StoredInvestigationResult, error) {
	s.gotIDs = append(s.gotIDs, resultID)
	if s.getErr != nil {
		return StoredInvestigationResult{}, s.getErr
	}
	result, ok := s.results[resultID]
	if !ok {
		return StoredInvestigationResult{}, errors.New("investigation result not found")
	}
	epoch := s.graphEpoch
	if epoch == nil {
		zero := int64(0)
		epoch = &zero
	}
	return StoredInvestigationResult{Result: result, GraphEpoch: epoch}, nil
}

// capturingGraphReader records every request ResolveSubjects/DiscoverContext
// were called with, so a test can assert what the Engine expanded the
// caller's request into (e.g. prior-subject-receipt hints) without the
// GraphReader itself needing to know about receipts.
type capturingGraphReader struct {
	resolution       SubjectResolution
	context          GraphContext
	resolveRequests  []InvestigationRequest
	confirmedAnchors []*ConfirmedAnchorSelection
	discoverHints    [][]SubjectHint
	// bindingEpochs (CHAOS-3898 §5b), when non-empty, is consumed one
	// value per ResolveInvestigationBinding call, in order -- the LAST
	// value repeats once exhausted -- so a test can simulate a build/flip
	// happening BETWEEN Engine's request-start binding resolution and its
	// later re-resolution at Save (recordBindingEpochDelta). Left empty,
	// every call returns epoch 0, matching every other test's existing
	// expectation.
	bindingEpochs      []int64
	bindingCallCount   int
	bindingCallCountMu sync.Mutex
}

func (g *capturingGraphReader) ResolveInvestigationBinding(context.Context, storage.Principal) (ResolvedGraphBinding, error) {
	if len(g.bindingEpochs) == 0 {
		return ResolvedGraphBinding{GraphKey: "capturing-key", Epoch: 0}, nil
	}
	g.bindingCallCountMu.Lock()
	index := g.bindingCallCount
	if index >= len(g.bindingEpochs) {
		index = len(g.bindingEpochs) - 1
	}
	g.bindingCallCount++
	g.bindingCallCountMu.Unlock()
	return ResolvedGraphBinding{GraphKey: "capturing-key", Epoch: g.bindingEpochs[index]}, nil
}

func (g *capturingGraphReader) ResolveSubjects(_ context.Context, _ storage.Principal, request InvestigationRequest, _ InterpretedQuestion, _ ResolvedGraphBinding, _ *ConfirmedExpectedKind, confirmedAnchor *ConfirmedAnchorSelection) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	g.resolveRequests = append(g.resolveRequests, request)
	g.confirmedAnchors = append(g.confirmedAnchors, confirmedAnchor)
	// CHAOS-4085: nil CommitBasisSet -- every commit this double returns reads
	// back as CommitBasisUnknown, the strict (must-be-affirmed) treatment.
	return g.resolution, StructureOfferMaterial{}, nil, nil, nil
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
	// CHAOS-4364: a bare FactStatus requirement for a project subject now
	// composes (statusCategoryFactKindComposition's first project entry) to
	// every kind whose Capability supports SubjectProject -- health,
	// workload, readiness, investment (CHAOS-4363), flow, landscape
	// (CHAOS-4364; codex R2 fixed an earlier hand-merge that only unioned
	// flow+landscape into this entry) -- the same composition team subjects
	// already got under CHAOS-4347.
	wantKinds := []FactKind{FactFlow, FactHealth, FactInvestment, FactLandscape, FactReadiness, FactWorkload, FactBlockers}
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

	// subjectlessTerminalReasons, priorSubjectReceiptSkipReasons, and
	// answerReuseServedRequestIDs (CHAOS-3888) record every call's
	// arguments verbatim, mirroring falkorgraph's own recordingTelemetry
	// (vector_test.go) -- a slice/struct list, not just a count, so a test
	// can assert the EXACT reason/id values reported.
	subjectlessTerminalReasons []string
	// factScopeExpansions (CHAOS-4099) records every scope-expansion event
	// verbatim, same list-not-count discipline as the fields around it: a
	// test asserts the EXACT closed-vocabulary outcome and the exact counts,
	// because "an event happened" is precisely the assertion that let
	// CHAOS-4085's fields ship unread.
	factScopeExpansions []FactScopeExpansionEvent
	// questionFamilyResolutions (CHAOS-4632) records every family
	// resolution event verbatim, same list-not-count discipline as the
	// fields around it.
	questionFamilyResolutions []QuestionFamilyResolutionEvent
	planNarrowings            []PlanNarrowingEvent
	// groupedCohortCompletenesses (CHAOS-4733) records every grouped-cohort
	// completeness fold verbatim, same list-not-count discipline.
	groupedCohortCompletenesses []GroupedCohortCompletenessEvent
	commitAffirmations          int
	// categoryFactCompositions (CHAOS-4347) records every status-category
	// composition event verbatim, same list-not-count discipline.
	categoryFactCompositions       []CategoryFactCompositionEvent
	priorSubjectReceiptSkipReasons []priorSubjectReceiptSkipReasonRecord
	answerReuseServedRequestIDs    []answerReuseServedRequestIDRecord
	bindingEpochDeltas             []bindingEpochDeltaRecord
	// windowBinderOutcomes/windowCanonicalizationOutcomes (CHAOS-3900 W1)
	// mirror the pair above's own list-not-count discipline.
	windowBinderOutcomes           []WindowBindReason
	windowCanonicalizationOutcomes []WindowCanonicalizationOutcome
	// structureNeedsDisclosed/structureOfferCounts/structureReceipts
	// (CHAOS-3900 P1.F) mirror the SAME list-not-count discipline.
	structureNeedsDisclosed []contractsv1.ContextFabricStructureNeedKind
	gatedOfferResolutions   []GatedOfferResolutionOutcome
	cohortStructureGates    []cohortStructureGateRecord
	structureOfferCounts    []structureOfferCountRecord
	structureReceipts       []structureReceiptRecord
	// structureExplicit (CHAOS-3972 P3) mirrors structureReceipts' own
	// list-not-count discipline.
	structureExplicit []structureExplicitRecord
	// synthesisStatusOverrides (CHAOS-4098) mirrors the SAME
	// list-not-count discipline: a test asserts the exact from/to/reason
	// triple and the committed count, never merely that something fired.
	synthesisStatusOverrides []SynthesisStatusOverrideOutcome
	// priorConsulted/priorDegradations (CHAOS-3977 P5) mirror the SAME
	// list-not-count discipline.
	priorConsulted    []priorConsultedRecord
	priorDegradations []PriorDegradationState
	// offerPhrasingOutcomes (CHAOS-4171 PR2) mirrors the SAME
	// list-not-count discipline.
	offerPhrasingOutcomes []OfferPhrasingOutcome
	// projectedRowsCounts (CHAOS-4355) mirrors the SAME list-not-count
	// discipline: a test asserts the exact count/truncated pair, never
	// merely that something fired.
	projectedRowsCounts []projectedRowsCountRecord
	// projectedRowsByFactKind (CHAOS-4418) mirrors the SAME
	// list-not-count discipline: a test asserts the exact map recorded
	// for a given call, never merely that something fired.
	projectedRowsByFactKind []map[FactKind]int
	// windowGateOfferDisclosures/windowExpandOfferRedemptions (CHAOS-4314)
	// mirror the SAME list-not-count discipline as windowCanonicalizationOutcomes
	// above.
	windowGateOfferDisclosures []bool
	windowExpandOfferRedeemed  int
	// windowCarries (CHAOS-4360) mirrors the SAME list-not-count discipline:
	// a test asserts the exact outcome/chain-depth pair, never merely that
	// something fired.
	windowCarries []windowCarryRecord
	// modelRowsStripped (CHAOS-4355 follow-up) mirrors the SAME
	// list-not-count discipline.
	modelRowsStripped []int
	// cohortRanked (CHAOS-4398) mirrors the SAME list-not-count discipline.
	renderShapeSelections []RenderShapeSelectionEvent
	cohortRanked          []CohortRankedEvent
	// cohortDriverNarrations (CHAOS-4398 PR3b) mirrors the SAME
	// list-not-count discipline.
	cohortDriverNarrations []CohortDriverNarrationEvent
	// evidenceLabelFallbacks (CHAOS-4690) mirrors modelRowsStripped's own
	// list-not-count discipline: a test asserts the exact count recorded
	// for a given call, never merely that something fired.
	evidenceLabelFallbacks []int
	// coverageDisclosurePhrasings (CHAOS-4690 Commit F) mirrors the SAME
	// list-not-count discipline: a test asserts the exact
	// outcome/phrased/total triple recorded for a given call, never
	// merely that something fired.
	coverageDisclosurePhrasings []coverageDisclosurePhrasingRecord
}

// coverageDisclosurePhrasingRecord (CHAOS-4690 Commit F) mirrors
// bindingEpochDeltaRecord's own shape one field triple over.
type coverageDisclosurePhrasingRecord struct {
	outcome   CoverageDisclosureOutcome
	violation CoverageDisclosureViolation
	phrased   int
	total     int
}

// windowCarryRecord (CHAOS-4360) mirrors priorSubjectReceiptSkipReasonRecord's
// own shape one field pair over.
type windowCarryRecord struct {
	outcome    WindowCarryOutcome
	chainDepth int
}

type priorConsultedRecord struct {
	member  contractsv1.ContextFabricStructureNeedKind
	outcome PriorConsultedOutcome
}

type structureOfferCountRecord struct {
	member contractsv1.ContextFabricStructureNeedKind
	source contractsv1.ContextFabricStructureOfferSource
	count  int
}

type structureReceiptRecord struct {
	member  contractsv1.ContextFabricStructureNeedKind
	outcome StructureReceiptOutcome
}

type structureExplicitRecord struct {
	member  contractsv1.ContextFabricStructureNeedKind
	outcome StructureExplicitOutcome
}

type priorSubjectReceiptSkipReasonRecord struct {
	reason     string
	count      int
	epochDelta int64
}

type answerReuseServedRequestIDRecord struct {
	servedRequestID   string
	requestIDMismatch bool
}

type bindingEpochDeltaRecord struct {
	flipped bool
	delta   int64
}

type projectedRowsCountRecord struct {
	count     int
	truncated bool
}

func (r *recordingTelemetry) RecordAnswerReuse(_ context.Context, _ storage.Principal, outcome AnswerReuseOutcome) {
	r.answerReuseOutcomes = append(r.answerReuseOutcomes, outcome)
}

func (r *recordingTelemetry) RecordPriorSubjectReceiptsSkipped(_ context.Context, _ storage.Principal, skipped int) {
	r.priorSubjectReceiptsSkipped = append(r.priorSubjectReceiptsSkipped, skipped)
}

func (r *recordingTelemetry) RecordSubjectlessTerminal(_ context.Context, _ storage.Principal, reason string) {
	r.subjectlessTerminalReasons = append(r.subjectlessTerminalReasons, reason)
}

func (r *recordingTelemetry) RecordSynthesisStatusOverride(_ context.Context, _ storage.Principal, outcome SynthesisStatusOverrideOutcome) {
	r.synthesisStatusOverrides = append(r.synthesisStatusOverrides, outcome)
}

func (r *recordingTelemetry) RecordFactScopeExpansion(_ context.Context, _ storage.Principal, event FactScopeExpansionEvent) {
	r.factScopeExpansions = append(r.factScopeExpansions, event)
}

func (r *recordingTelemetry) RecordCategoryFactComposition(_ context.Context, _ storage.Principal, event CategoryFactCompositionEvent) {
	r.categoryFactCompositions = append(r.categoryFactCompositions, event)
}

func (r *recordingTelemetry) RecordPriorSubjectReceiptSkipReason(_ context.Context, _ storage.Principal, reason string, count int, epochDelta int64) {
	r.priorSubjectReceiptSkipReasons = append(r.priorSubjectReceiptSkipReasons, priorSubjectReceiptSkipReasonRecord{reason, count, epochDelta})
}

func (r *recordingTelemetry) RecordAnswerReuseServedRequestID(_ context.Context, _ storage.Principal, servedRequestID string, requestIDMismatch bool) {
	r.answerReuseServedRequestIDs = append(r.answerReuseServedRequestIDs, answerReuseServedRequestIDRecord{servedRequestID, requestIDMismatch})
}

func (r *recordingTelemetry) RecordBindingEpochDelta(_ context.Context, _ storage.Principal, flipped bool, delta int64) {
	r.bindingEpochDeltas = append(r.bindingEpochDeltas, bindingEpochDeltaRecord{flipped, delta})
}

func (r *recordingTelemetry) RecordWindowBinderOutcome(_ context.Context, _ storage.Principal, reason WindowBindReason) {
	r.windowBinderOutcomes = append(r.windowBinderOutcomes, reason)
}

func (r *recordingTelemetry) RecordWindowCanonicalization(_ context.Context, _ storage.Principal, outcome WindowCanonicalizationOutcome) {
	r.windowCanonicalizationOutcomes = append(r.windowCanonicalizationOutcomes, outcome)
}

func (r *recordingTelemetry) RecordWindowCarry(_ context.Context, _ storage.Principal, outcome WindowCarryOutcome, chainDepth int) {
	r.windowCarries = append(r.windowCarries, windowCarryRecord{outcome, chainDepth})
}

func (r *recordingTelemetry) RecordStructureNeedsDisclosed(_ context.Context, _ storage.Principal, member contractsv1.ContextFabricStructureNeedKind) {
	r.structureNeedsDisclosed = append(r.structureNeedsDisclosed, member)
}

func (r *recordingTelemetry) RecordGatedOfferResolution(_ context.Context, _ storage.Principal, outcome GatedOfferResolutionOutcome) {
	r.gatedOfferResolutions = append(r.gatedOfferResolutions, outcome)
}

// cohortStructureGateRecord (CHAOS-4579/CHAOS-4531) keeps BOTH arguments
// RecordCohortStructureGate reports -- a test asserting only the outcome
// could not tell "applied on a discovered_cohort question" from "applied
// on some other shape", which is the whole decision this event exists to
// make auditable.
type cohortStructureGateRecord struct {
	outcome CohortStructureGateOutcome
	shape   InvestigationShape
}

// RecordQuestionFamilyResolution (CHAOS-4632) records the whole event, not
// merely the resolved family: the per-sample rows, the downgrade count and
// the divergence count are the fields that make a split consensus
// diagnosable, and a double that kept only the outcome would be exactly
// the kind of test that cannot observe them going missing.
func (r *recordingTelemetry) RecordQuestionFamilyResolution(_ context.Context, _ storage.Principal, event QuestionFamilyResolutionEvent) {
	r.questionFamilyResolutions = append(r.questionFamilyResolutions, event)
}

// RecordPlanNarrowing (CHAOS-4636) records the whole event for the same
// reason the family resolution above does: the stage, the basis and the
// measured numbers are what make an over-budget answer diagnosable, and a
// double keeping only a count could not observe any of them going missing.
func (r *recordingTelemetry) RecordPlanNarrowing(_ context.Context, _ storage.Principal, event PlanNarrowingEvent) {
	r.planNarrowings = append(r.planNarrowings, event)
}

// RecordGroupedCohortCompleteness (CHAOS-4733) records the whole event, same
// list-not-count discipline as RecordPlanNarrowing above.
func (r *recordingTelemetry) RecordGroupedCohortCompleteness(_ context.Context, _ storage.Principal, event GroupedCohortCompletenessEvent) {
	r.groupedCohortCompletenesses = append(r.groupedCohortCompletenesses, event)
}

// RecordCommitAffirmationRetraction (CHAOS-4085) is COUNTED here so a retry
// that emitted the same retraction twice is observable. That double-count is
// codex round 2's finding 1: the assembly runs twice on a retry and the first
// pass's answer is discarded, so a retraction emitted from inside it never
// happened as far as the caller is concerned.
func (r *recordingTelemetry) RecordCommitAffirmationRetraction(_ context.Context, _ storage.Principal, _ CommitAffirmationOutcome) {
	r.commitAffirmations++
}

func (r *recordingTelemetry) RecordCohortStructureGate(_ context.Context, _ storage.Principal, outcome CohortStructureGateOutcome, shape InvestigationShape) {
	r.cohortStructureGates = append(r.cohortStructureGates, cohortStructureGateRecord{outcome, shape})
}

func (r *recordingTelemetry) RecordWindowGateOfferDisclosure(_ context.Context, _ storage.Principal, offered bool) {
	r.windowGateOfferDisclosures = append(r.windowGateOfferDisclosures, offered)
}

func (r *recordingTelemetry) RecordWindowExpandOfferRedeemed(_ context.Context, _ storage.Principal) {
	r.windowExpandOfferRedeemed++
}

func (r *recordingTelemetry) RecordStructureOfferCount(_ context.Context, _ storage.Principal, member contractsv1.ContextFabricStructureNeedKind, source contractsv1.ContextFabricStructureOfferSource, count int) {
	r.structureOfferCounts = append(r.structureOfferCounts, structureOfferCountRecord{member, source, count})
}

func (r *recordingTelemetry) RecordStructureReceipt(_ context.Context, _ storage.Principal, member contractsv1.ContextFabricStructureNeedKind, outcome StructureReceiptOutcome) {
	r.structureReceipts = append(r.structureReceipts, structureReceiptRecord{member, outcome})
}

func (r *recordingTelemetry) RecordStructureExplicit(_ context.Context, _ storage.Principal, member contractsv1.ContextFabricStructureNeedKind, outcome StructureExplicitOutcome) {
	r.structureExplicit = append(r.structureExplicit, structureExplicitRecord{member, outcome})
}

func (r *recordingTelemetry) RecordPriorConsulted(_ context.Context, _ storage.Principal, member contractsv1.ContextFabricStructureNeedKind, outcome PriorConsultedOutcome) {
	r.priorConsulted = append(r.priorConsulted, priorConsultedRecord{member, outcome})
}

func (r *recordingTelemetry) RecordPriorDegradation(_ context.Context, _ storage.Principal, state PriorDegradationState) {
	r.priorDegradations = append(r.priorDegradations, state)
}

func (r *recordingTelemetry) RecordOfferPhrasing(_ context.Context, _ storage.Principal, outcome OfferPhrasingOutcome) {
	r.offerPhrasingOutcomes = append(r.offerPhrasingOutcomes, outcome)
}

func (r *recordingTelemetry) RecordProjectedRowsCount(_ context.Context, _ storage.Principal, count int, truncated bool) {
	r.projectedRowsCounts = append(r.projectedRowsCounts, projectedRowsCountRecord{count: count, truncated: truncated})
}

func (r *recordingTelemetry) RecordProjectedRowsByFactKind(_ context.Context, _ storage.Principal, byKind map[FactKind]int) {
	r.projectedRowsByFactKind = append(r.projectedRowsByFactKind, byKind)
}

func (r *recordingTelemetry) RecordModelRowsStripped(_ context.Context, _ storage.Principal, claims int) {
	r.modelRowsStripped = append(r.modelRowsStripped, claims)
}

func (r *recordingTelemetry) RecordRenderShapeSelection(_ context.Context, _ storage.Principal, event RenderShapeSelectionEvent) {
	r.renderShapeSelections = append(r.renderShapeSelections, event)
}

func (r *recordingTelemetry) RecordCohortRanked(_ context.Context, _ storage.Principal, event CohortRankedEvent) {
	r.cohortRanked = append(r.cohortRanked, event)
}

func (r *recordingTelemetry) RecordCohortDriverNarration(_ context.Context, _ storage.Principal, event CohortDriverNarrationEvent) {
	r.cohortDriverNarrations = append(r.cohortDriverNarrations, event)
}

func (r *recordingTelemetry) RecordEvidenceLabelFallback(_ context.Context, _ storage.Principal, count int) {
	r.evidenceLabelFallbacks = append(r.evidenceLabelFallbacks, count)
}

func (r *recordingTelemetry) RecordCoverageDisclosurePhrasing(_ context.Context, _ storage.Principal, outcome CoverageDisclosureOutcome, violation CoverageDisclosureViolation, phrased, total int) {
	r.coverageDisclosurePhrasings = append(r.coverageDisclosurePhrasings, coverageDisclosurePhrasingRecord{outcome: outcome, violation: violation, phrased: phrased, total: total})
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
// TestEngineRecordsBindingEpochDeltaOnFlip is the CHAOS-3898 §5b
// flip_during_investigation/cf_binding_epoch_delta direct proof: when the
// organization's active graph epoch moves BETWEEN Engine's request-start
// binding resolution and its later re-resolution at Save,
// RecordBindingEpochDelta must report flipped=true and the signed delta --
// and the ORIGINAL binding (not the re-resolved one) must still be what
// Save actually persisted (graphEpoch on the store), proving the
// telemetry-only re-resolution never contaminates correctness.
func TestEngineRecordsBindingEpochDeltaOnFlip(t *testing.T) {
	t.Parallel()
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	graph := &capturingGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		context: GraphContext{
			Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{}, FactRequirements: []FactRequirement{},
			EvidenceRefIDs: []string{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
		// First call (request start) resolves epoch 5; the SECOND call
		// (recordBindingEpochDelta, at Save) resolves epoch 8 -- a
		// simulated build/flip during the investigation.
		bindingEpochs: []int64{5, 8},
	}
	store := &resultStoreStub{}
	telemetry := &recordingTelemetry{}
	engine := mustEngineForPriorReceiptTest(t, graph, store, telemetry)

	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest()); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	// The ORIGINAL binding (epoch 5, resolved once at request start) is
	// what Save actually persists -- proven at the store level by
	// TestF5_FindReusableClassifiesGraphEpochMismatchDistinctlyFromNoCandidate
	// and pginvestigation's own Save tests (resultStoreStub here does not
	// itself capture graphEpoch). This test's own job is narrower and
	// already fully covered by the telemetry assertion below: the
	// re-resolution used for the signal never contaminates what gets
	// stamped on the result.
	if store.saved.ResultID == "" {
		t.Fatal("Save was never called")
	}

	if len(telemetry.bindingEpochDeltas) != 1 {
		t.Fatalf("bindingEpochDeltas = %#v, want exactly one record", telemetry.bindingEpochDeltas)
	}
	got := telemetry.bindingEpochDeltas[0]
	if !got.flipped {
		t.Fatalf("flipped = false, want true (epoch moved 5 -> 8 between binding resolution and save)")
	}
	if got.delta != 3 {
		t.Fatalf("delta = %d, want 3 (8 - 5)", got.delta)
	}
}

// TestEngineRecordsBindingEpochDeltaWithoutFlip proves the ordinary,
// overwhelmingly common case: no build/flip happened, flipped=false and
// delta=0 are still reported (unconditionally, every Save -- see
// RecordBindingEpochDelta's own doc comment for why zero-vs-nonzero
// must be a real counter, not silence).
func TestEngineRecordsBindingEpochDeltaWithoutFlip(t *testing.T) {
	t.Parallel()
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	graph := &capturingGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		context: GraphContext{
			Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{}, FactRequirements: []FactRequirement{},
			EvidenceRefIDs: []string{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
		bindingEpochs: []int64{5, 5},
	}
	store := &resultStoreStub{}
	telemetry := &recordingTelemetry{}
	engine := mustEngineForPriorReceiptTest(t, graph, store, telemetry)

	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest()); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	if len(telemetry.bindingEpochDeltas) != 1 {
		t.Fatalf("bindingEpochDeltas = %#v, want exactly one record", telemetry.bindingEpochDeltas)
	}
	got := telemetry.bindingEpochDeltas[0]
	if got.flipped {
		t.Fatalf("flipped = true, want false (epoch unchanged)")
	}
	if got.delta != 0 {
		t.Fatalf("delta = %d, want 0", got.delta)
	}
}

// bindingEpochDeltaOrderingStore is a resultStoreStub twin whose Save
// records how many times its paired capturingGraphReader had already
// resolved a binding BY THE TIME Save was called, and can be made to fail
// -- CHAOS-3898 P2 fix-forward's own regression fixtures (codex
// retroactive review of #151/#152, chris-verified): sampleBindingEpochDelta
// must run BEFORE Save, and emitBindingEpochDelta must still be a no-op
// when Save fails.
type bindingEpochDeltaOrderingStore struct {
	graph                    *capturingGraphReader
	saveErr                  error
	bindingCallCountAtSave   int
	bindingCallCountObserved bool
}

func (s *bindingEpochDeltaOrderingStore) Save(context.Context, storage.Principal, InvestigationResult, SourceWatermarkSnapshot, RebuildEpoch, string, ReuseRetrievalIdentity, ReusePromptVersions, ReuseVersionAuthorities, int64) error {
	s.graph.bindingCallCountMu.Lock()
	s.bindingCallCountAtSave = s.graph.bindingCallCount
	s.bindingCallCountObserved = true
	s.graph.bindingCallCountMu.Unlock()
	return s.saveErr
}

func (s *bindingEpochDeltaOrderingStore) Get(context.Context, storage.Principal, string) (StoredInvestigationResult, error) {
	return StoredInvestigationResult{}, nil
}

// TestEngineSamplesBindingEpochDeltaBeforeSave is CHAOS-3898 P2's direct
// regression proof: the epoch re-resolution used for
// flip_during_investigation/cf_binding_epoch_delta must be taken BEFORE
// Save runs, not after. Before the fix, Save's own I/O sat inside the
// window this signal measures -- a flip landing strictly after Save had
// already persisted the result (work this investigation was no longer
// doing) could still be attributed to "during" it. graph.bindingEpochs has
// two entries (request-start resolve, then the sample); by the time Save
// observes bindingCallCount, both must have already happened.
func TestEngineSamplesBindingEpochDeltaBeforeSave(t *testing.T) {
	t.Parallel()
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	graph := &capturingGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		context: GraphContext{
			Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{}, FactRequirements: []FactRequirement{},
			EvidenceRefIDs: []string{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
		bindingEpochs: []int64{5, 8},
	}
	store := &bindingEpochDeltaOrderingStore{graph: graph}
	telemetry := &recordingTelemetry{}
	engine := mustEngineForPriorReceiptTest(t, graph, store, telemetry)

	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest()); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if !store.bindingCallCountObserved {
		t.Fatal("Save was never called")
	}
	if store.bindingCallCountAtSave != 2 {
		t.Fatalf("bindingCallCount at Save = %d, want 2 (the epoch-delta sample must be taken BEFORE Save, not after)", store.bindingCallCountAtSave)
	}
	if len(telemetry.bindingEpochDeltas) != 1 || !telemetry.bindingEpochDeltas[0].flipped {
		t.Fatalf("bindingEpochDeltas = %#v, want one flipped record", telemetry.bindingEpochDeltas)
	}
}

// TestEngineDoesNotEmitBindingEpochDeltaWhenSaveFails proves
// emitBindingEpochDelta's own fail-closed-on-error contract survives the
// P2 reordering: sampleBindingEpochDelta now runs unconditionally before
// Save, but the telemetry signal itself must still never fire for a Save
// that failed -- a result that was never persisted has nothing this
// signal should be reported against.
func TestEngineDoesNotEmitBindingEpochDeltaWhenSaveFails(t *testing.T) {
	t.Parallel()
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	graph := &capturingGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		context: GraphContext{
			Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{}, FactRequirements: []FactRequirement{},
			EvidenceRefIDs: []string{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
		bindingEpochs: []int64{5, 8},
	}
	store := &bindingEpochDeltaOrderingStore{graph: graph, saveErr: errors.New("save unavailable")}
	telemetry := &recordingTelemetry{}
	engine := mustEngineForPriorReceiptTest(t, graph, store, telemetry)

	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest()); err == nil {
		t.Fatal("Investigate() error = nil, want the Save failure to surface")
	}
	if len(telemetry.bindingEpochDeltas) != 0 {
		t.Fatalf("bindingEpochDeltas = %#v, want none recorded when Save fails", telemetry.bindingEpochDeltas)
	}
}

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

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
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
	// CHAOS-3478/CHAOS-3813: the wire echo must disclose "applied" for a
	// receipt that actually survived end to end, not just omit it as
	// silently fine.
	wantDispositions := []contractsv1.ContextFabricPriorSubjectReceiptEntry{
		{PriorResultID: "result_prior_1", ReceiptID: "receipt_abc12345", Disposition: contractsv1.ContextFabricPriorSubjectReceiptApplied},
	}
	if !reflect.DeepEqual(result.SubjectResolution.PriorSubjectReceiptDispositions, wantDispositions) {
		t.Fatalf("PriorSubjectReceiptDispositions = %#v, want %#v", result.SubjectResolution.PriorSubjectReceiptDispositions, wantDispositions)
	}
}

// TestEngineConfirmedAnchorReachesResolveSubjects is CHAOS-4042's own
// engine-plumbing proof, one level up from TestCHAOS4042_AnchorMembership-
// Reverification_VerifierAcceptsConfirms (structure_test.go): that test
// proves canonicalizeStructure produces a Confirmed entry once a wired
// AnchorMembershipVerifier accepts a redeemed v2 anchor receipt; this test
// proves the REST of the chain -- that confirmedAnchorSelection's own
// conversion of that entry actually reaches GraphReader.ResolveSubjects as
// its ConfirmedAnchorSelection argument (engine.go's ResolveSubjects call
// site), which is what lets a redeemed claimant become the shadow evidence
// round's own census discriminator (RunShadowEvidenceRound's ConfirmedAnchor
// input) instead of BindAnchor's own re-derivation from question text.
func TestEngineConfirmedAnchorReachesResolveSubjects(t *testing.T) {
	t.Parallel()
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_prior_anchor_1"
	priorResult.SchemaVersion = InvestigationResultSchemaV2
	priorResult.StructureNeeds = &StructureNeeds{
		Missing: []StructureNeedKind{"subject_anchor"},
		AnchorOptions: []AnchorOption{
			{
				ReceiptID: "ancr_confirm0001", OptionID: "opt_anchor", Label: "the widget-service repository",
				Kind: SubjectRepository, CanonicalID: "repository_widget_service",
				MatchedTermHash: "aa11bb22cc33dd44ee55ff66",
				OfferSource:     "engine",
			},
		},
	}
	store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}
	graph := &capturingGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		context: GraphContext{
			Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{}, FactRequirements: []FactRequirement{},
			EvidenceRefIDs: []string{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
	}
	verifierCalls := 0
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
		Results: store,
		AnchorMembershipVerifier: func(ctx context.Context, principal storage.Principal, scope RequestedScope, binding ResolvedGraphBinding, kind contractsv1.ContextFabricSubjectKind, canonicalID, matchedTermHash string) (bool, AnchorVerificationReason) {
			verifierCalls++
			return true, AnchorVerificationValid
		},
	}, EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(200, 0).UTC() }, NewResultID: func() string { return "result_99999999" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	request := validInvestigationRequest()
	request.PriorAnchorReceipts = []BoundSubjectReceipt{{ResultID: priorResult.ResultID, ReceiptID: "ancr_confirm0001"}}

	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if verifierCalls != 1 {
		t.Fatalf("AnchorMembershipVerifier called %d times, want 1", verifierCalls)
	}
	if len(graph.confirmedAnchors) != 1 {
		t.Fatalf("confirmedAnchors = %#v, want exactly one ResolveSubjects call recorded", graph.confirmedAnchors)
	}
	got := graph.confirmedAnchors[0]
	if got == nil {
		t.Fatal("confirmedAnchors[0] = nil, want the redeemed receipt's own kind/canonical_id -- a nil here means the confirmed anchor never reached ResolveSubjects, so it could never reach the shadow evidence round's census discriminator either")
	}
	if got.Kind != SubjectRepository || got.CanonicalID != "repository_widget_service" {
		t.Fatalf("confirmedAnchors[0] = %+v, want {Kind: repository, CanonicalID: repository_widget_service} -- the redeemed offer's own content, not a re-derivation", got)
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

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v, want an unresolvable receipt to degrade safely, not fail", err)
	}
	if len(graph.resolveRequests) != 1 || len(graph.resolveRequests[0].RequestedScope.SubjectHints) != 0 {
		t.Fatalf("resolveRequests = %#v, want no hint added for an unresolvable receipt", graph.resolveRequests)
	}
	if len(telemetry.priorSubjectReceiptsSkipped) != 1 || telemetry.priorSubjectReceiptsSkipped[0] != 1 {
		t.Fatalf("telemetry = %#v, want exactly one skip of count 1 recorded", telemetry.priorSubjectReceiptsSkipped)
	}
	// CHAOS-3888: an unloadable prior result (store.getErr) must classify as
	// "unloadable", never "no_match" or "failed_reauth" -- see
	// resolvePriorSubjectHints' own doc comment for the three-reason split.
	if want := []priorSubjectReceiptSkipReasonRecord{{reason: "unloadable", count: 1}}; !reflect.DeepEqual(telemetry.priorSubjectReceiptSkipReasons, want) {
		t.Fatalf("priorSubjectReceiptSkipReasons = %#v, want %#v", telemetry.priorSubjectReceiptSkipReasons, want)
	}
	// CHAOS-3478/CHAOS-3813: this is the exact ticket-3813 scenario -- "a
	// client re-asks with a chosen candidate, ACR silently ignores the
	// choice" -- proven closed: the wire response now names the receipt
	// and why it was skipped, not just a server-side telemetry count.
	wantDispositions := []contractsv1.ContextFabricPriorSubjectReceiptEntry{
		{PriorResultID: "result_missing", ReceiptID: "receipt_missing1", Disposition: contractsv1.ContextFabricPriorSubjectReceiptSkippedUnloadable},
	}
	if !reflect.DeepEqual(result.SubjectResolution.PriorSubjectReceiptDispositions, wantDispositions) {
		t.Fatalf("PriorSubjectReceiptDispositions = %#v, want %#v", result.SubjectResolution.PriorSubjectReceiptDispositions, wantDispositions)
	}
}

// TestEngineStripsPriorSubjectReceiptFromADifferentGraphEpoch is the
// CHAOS-3898 §2.2 ingress taint gate's direct proof: a receipt whose prior
// result loaded successfully, and whose candidate WOULD otherwise match,
// must still be stripped entirely -- no hint built, no label or id leaked
// -- when its StoredInvestigationResult carrier's GraphEpoch differs from
// this investigation's own ResolvedGraphBinding.Epoch, and the strip must
// classify as "stale_graph_epoch" (cf_receipt_taint_strip, §5b), distinct
// from every other skip reason.
func TestEngineStripsPriorSubjectReceiptFromADifferentGraphEpoch(t *testing.T) {
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
	staleEpoch := int64(7)
	store := &staticResultStore{
		results:    map[string]InvestigationResult{"result_prior_1": priorResult},
		graphEpoch: &staleEpoch,
	}
	// The graph reader's own ResolveInvestigationBinding (capturingGraphReader,
	// fixed at Epoch 0) never matches the store's stale epoch 7 -- exactly
	// the "a build/flip happened since the prior turn" scenario.
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
	request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: "result_prior_1", ReceiptID: "receipt_abc12345"}}

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v, want a graph-epoch-mismatched receipt to degrade safely, not fail", err)
	}
	// The taint gate must strip the receipt BEFORE any hint is built --
	// nothing from the prior result's committed subject reaches GraphReader.
	if len(graph.resolveRequests) != 1 || len(graph.resolveRequests[0].RequestedScope.SubjectHints) != 0 {
		t.Fatalf("resolveRequests = %#v, want no hint added for a graph-epoch-mismatched receipt", graph.resolveRequests)
	}
	if len(telemetry.priorSubjectReceiptsSkipped) != 1 || telemetry.priorSubjectReceiptsSkipped[0] != 1 {
		t.Fatalf("telemetry = %#v, want exactly one skip of count 1 recorded", telemetry.priorSubjectReceiptsSkipped)
	}
	if want := []priorSubjectReceiptSkipReasonRecord{{reason: "stale_graph_epoch", count: 1, epochDelta: -7}}; !reflect.DeepEqual(telemetry.priorSubjectReceiptSkipReasons, want) {
		t.Fatalf("priorSubjectReceiptSkipReasons = %#v, want %#v", telemetry.priorSubjectReceiptSkipReasons, want)
	}
	wantDispositions := []contractsv1.ContextFabricPriorSubjectReceiptEntry{
		{PriorResultID: "result_prior_1", ReceiptID: "receipt_abc12345", Disposition: contractsv1.ContextFabricPriorSubjectReceiptSkippedStaleGraphEpoch},
	}
	if !reflect.DeepEqual(result.SubjectResolution.PriorSubjectReceiptDispositions, wantDispositions) {
		t.Fatalf("PriorSubjectReceiptDispositions = %#v, want %#v", result.SubjectResolution.PriorSubjectReceiptDispositions, wantDispositions)
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
	// CHAOS-3888: a receipt that built a real hint but did not survive
	// THIS call's own graph resolution must classify as "failed_reauth",
	// distinct from "unloadable"/"no_match" -- the receipt's prior result
	// loaded fine and named a real candidate; it is only re-resolution that
	// declined it.
	if want := []priorSubjectReceiptSkipReasonRecord{{reason: "failed_reauth", count: 1}}; !reflect.DeepEqual(telemetry.priorSubjectReceiptSkipReasons, want) {
		t.Fatalf("priorSubjectReceiptSkipReasons = %#v, want %#v", telemetry.priorSubjectReceiptSkipReasons, want)
	}
	// CHAOS-3478/CHAOS-3813: the wire echo must distinguish "we tried to
	// re-verify this and it failed" from every pre-graph skip reason, not
	// collapse everything into one undifferentiated "skipped".
	wantDispositions := []contractsv1.ContextFabricPriorSubjectReceiptEntry{
		{PriorResultID: "result_prior_1", ReceiptID: "receipt_abc12345", Disposition: contractsv1.ContextFabricPriorSubjectReceiptSkippedFailedReauth},
	}
	if !reflect.DeepEqual(result.SubjectResolution.PriorSubjectReceiptDispositions, wantDispositions) {
		t.Fatalf("PriorSubjectReceiptDispositions = %#v, want %#v", result.SubjectResolution.PriorSubjectReceiptDispositions, wantDispositions)
	}
}

// TestEngineClassifiesPriorSubjectReceiptWithNoMatchingCandidateAsNoMatch
// (CHAOS-3888) is the third leg of the skip-reason split: a receipt whose
// prior InvestigationResult loads fine but names no candidate carrying a
// matching ReceiptID must classify as "no_match" -- distinct from
// "unloadable" (the prior result itself failed to load) and
// "failed_reauth" (a hint WAS built from a real match).
func TestEngineClassifiesPriorSubjectReceiptWithNoMatchingCandidateAsNoMatch(t *testing.T) {
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
	// receipt_wrong00 names a real, loadable prior result but a ReceiptID
	// that result never issued.
	request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: "result_prior_1", ReceiptID: "receipt_wrong00"}}

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(graph.resolveRequests) != 1 || len(graph.resolveRequests[0].RequestedScope.SubjectHints) != 0 {
		t.Fatalf("resolveRequests = %#v, want no hint added for a receipt naming no matching candidate", graph.resolveRequests)
	}
	if want := []priorSubjectReceiptSkipReasonRecord{{reason: "no_match", count: 1}}; !reflect.DeepEqual(telemetry.priorSubjectReceiptSkipReasons, want) {
		t.Fatalf("priorSubjectReceiptSkipReasons = %#v, want %#v", telemetry.priorSubjectReceiptSkipReasons, want)
	}
	// CHAOS-3478/CHAOS-3813: a receipt naming a candidate the referenced
	// result never actually offered must be disclosed as such, by its own
	// closed reason, never silently merged into the investigation's answer.
	wantDispositions := []contractsv1.ContextFabricPriorSubjectReceiptEntry{
		{PriorResultID: "result_prior_1", ReceiptID: "receipt_wrong00", Disposition: contractsv1.ContextFabricPriorSubjectReceiptSkippedNoMatch},
	}
	if !reflect.DeepEqual(result.SubjectResolution.PriorSubjectReceiptDispositions, wantDispositions) {
		t.Fatalf("PriorSubjectReceiptDispositions = %#v, want %#v", result.SubjectResolution.PriorSubjectReceiptDispositions, wantDispositions)
	}
}

// mustEngineForPriorReceiptTestCapturingInterpret is
// mustEngineForPriorReceiptTest's twin, except its interpreter records the
// InvestigationRequest it actually received into *capturedReceipts --
// CHAOS-3898 P1-1's own regression proof needs to see PriorSubjectReceipts
// as Interpret itself received them, not as the caller originally sent
// them.
func mustEngineForPriorReceiptTestCapturingInterpret(t *testing.T, graph GraphReader, store InvestigationResultStore, telemetry EngineTelemetry, capturedReceipts *[]BoundSubjectReceipt) *Engine {
	t.Helper()
	interpretation := InterpretedQuestion{
		Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent},
		FactRequirements: []FactRequirement{{Kind: FactStatus}},
	}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(_ context.Context, _ storage.Principal, request InvestigationRequest) (InterpretedQuestion, error) {
			*capturedReceipts = request.PriorSubjectReceipts
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

// TestEngineNeverPassesAStaleGraphEpochReceiptToInterpret is CHAOS-3898
// P1-1's direct regression proof (codex retroactive review of #151/#152,
// chris-verified, fix-forward on this branch): a receipt whose GRAPH EPOCH
// taint check would go on to strip it entirely must never reach the
// QuestionInterpreter's own input first. Before the fix, Interpret ran
// BEFORE resolvePriorSubjectHints (the taint gate), so genkitruntime's
// InterpretQuestion (runtime.go) serialized request.PriorSubjectReceipts
// VERBATIM into the model's input -- including a receipt this exact test's
// sibling, TestEngineStripsPriorSubjectReceiptFromADifferentGraphEpoch,
// proves the engine goes on to treat as if it never existed. Same fixture
// shape as that test; this one asserts on Interpret's OWN received request
// instead of GraphReader's.
func TestEngineNeverPassesAStaleGraphEpochReceiptToInterpret(t *testing.T) {
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
	staleEpoch := int64(7)
	store := &staticResultStore{
		results:    map[string]InvestigationResult{"result_prior_1": priorResult},
		graphEpoch: &staleEpoch,
	}
	graph := &capturingGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		context: GraphContext{
			Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{}, FactRequirements: []FactRequirement{},
			EvidenceRefIDs: []string{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
	}
	telemetry := &recordingTelemetry{}
	var capturedReceipts []BoundSubjectReceipt
	engine := mustEngineForPriorReceiptTestCapturingInterpret(t, graph, store, telemetry, &capturedReceipts)

	request := validInvestigationRequest()
	request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: "result_prior_1", ReceiptID: "receipt_abc12345"}}

	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(capturedReceipts) != 0 {
		t.Fatalf("Interpret received PriorSubjectReceipts = %#v, want empty -- a stale-graph-epoch receipt must be stripped BEFORE Interpret, not just before GraphReader", capturedReceipts)
	}
}

// TestEngineOnlyPassesValidatedReceiptsToInterpret is
// TestEngineNeverPassesAStaleGraphEpochReceiptToInterpret's positive
// control: a receipt that legitimately survives the taint/match gate DOES
// still reach Interpret -- CHAOS-3898 P1-1's fix withholds only UNVALIDATED
// receipts, never validated ones, so a conversational follow-up ("what
// about it") still has real prior-subject context to resolve against.
func TestEngineOnlyPassesValidatedReceiptsToInterpret(t *testing.T) {
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
	var capturedReceipts []BoundSubjectReceipt
	engine := mustEngineForPriorReceiptTestCapturingInterpret(t, graph, store, telemetry, &capturedReceipts)

	want := BoundSubjectReceipt{ResultID: "result_prior_1", ReceiptID: "receipt_abc12345"}
	request := validInvestigationRequest()
	request.PriorSubjectReceipts = []BoundSubjectReceipt{want}

	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(capturedReceipts) != 1 || capturedReceipts[0] != want {
		t.Fatalf("Interpret received PriorSubjectReceipts = %#v, want [%#v]", capturedReceipts, want)
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

// TestEngineDoesNotReportABudgetTruncatedReceiptAsAppliedOnACoincidentalMatch
// is a codex CHAOS-3813 round-1 finding (Medium, fixed): the previous
// version of composePriorSubjectReceiptDispositions classified a receipt
// solely by whether its subject appeared ANYWHERE in the final
// SubjectResolution, so a receipt whose own derived hint was dropped by
// the maxSubjectHints budget (same fixture shape as
// TestEngineCapsCombinedSubjectHintsAtContractBound above) could still be
// reported "applied" if some OTHER hint happened to resolve the identical
// subject -- exactly this test's fixture, where one of the 50 explicit
// caller hints already names the prior receipt's own subject and the
// stubbed graph resolves it. The receipt's own hint never reached
// GraphReader; the disposition must say so.
func TestEngineDoesNotReportABudgetTruncatedReceiptAsAppliedOnACoincidentalMatch(t *testing.T) {
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
		// The stubbed graph resolves project_ask_dev regardless of which
		// hint asked for it -- standing in for "one of the caller's own
		// 50 explicit hints already named this subject and it resolved",
		// NOT the prior receipt's own (truncated) hint.
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
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

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	wantDispositions := []contractsv1.ContextFabricPriorSubjectReceiptEntry{
		{PriorResultID: "result_prior_1", ReceiptID: "receipt_abc12345", Disposition: contractsv1.ContextFabricPriorSubjectReceiptSkippedFailedReauth},
	}
	if !reflect.DeepEqual(result.SubjectResolution.PriorSubjectReceiptDispositions, wantDispositions) {
		t.Fatalf("PriorSubjectReceiptDispositions = %#v, want %#v (the receipt's own hint was budget-truncated and must not read as applied on the strength of an unrelated hint)", result.SubjectResolution.PriorSubjectReceiptDispositions, wantDispositions)
	}
	if len(telemetry.priorSubjectReceiptsSkipped) != 1 || telemetry.priorSubjectReceiptsSkipped[0] != 1 {
		t.Fatalf("telemetry = %#v, want the budget-truncated receipt counted as a skip", telemetry.priorSubjectReceiptsSkipped)
	}
}

// TestEngineStillPassesAGraphBudgetCappedReceiptToInterpret is a codex
// re-review finding on CHAOS-3898 P1-1 (fixed): TestEngineCapsCombinedSubjectHintsAtContractBound's
// same 50-explicit-hints fixture, but proving the OTHER half of the fix --
// a receipt the v1 RequestedScope.SubjectHints contract bound excludes
// from GraphReader's own hints (maxSubjectHints, engine.go) must still
// reach Interpret. That cap is a GRAPH-CONTRACT limit; Interpret's own
// input carries no such bound, and the receipt already passed
// resolvePriorSubjectHints' own taint/match validation -- dropping it from
// Interpret too, just because the caller's own explicit hints already
// filled the graph-side budget, would silently narrow what a
// conversational follow-up ("it") can resolve against for no reason tied
// to the interpreter at all.
func TestEngineStillPassesAGraphBudgetCappedReceiptToInterpret(t *testing.T) {
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
	var capturedReceipts []BoundSubjectReceipt
	engine := mustEngineForPriorReceiptTestCapturingInterpret(t, graph, store, telemetry, &capturedReceipts)

	request := validInvestigationRequest()
	hints := make([]SubjectHint, 0, 50)
	for i := 0; i < 50; i++ {
		hints = append(hints, SubjectHint{Kind: SubjectProject, ID: fmt.Sprintf("project_caller_%d", i), Label: fmt.Sprintf("Caller Project %d", i), Source: "workbench"})
	}
	request.RequestedScope.SubjectHints = hints
	want := BoundSubjectReceipt{ResultID: "result_prior_1", ReceiptID: "receipt_abc12345"}
	request.PriorSubjectReceipts = []BoundSubjectReceipt{want}

	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(capturedReceipts) != 1 || capturedReceipts[0] != want {
		t.Fatalf("Interpret received PriorSubjectReceipts = %#v, want [%#v] -- the graph-hint budget cap must not also strip a validated receipt from Interpret's own input", capturedReceipts, want)
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
func (g *countingGraphReader) ResolveInvestigationBinding(context.Context, storage.Principal) (ResolvedGraphBinding, error) {
	return ResolvedGraphBinding{GraphKey: "counting-key", Epoch: 0}, nil
}

func (g *countingGraphReader) ResolveSubjects(context.Context, storage.Principal, InvestigationRequest, InterpretedQuestion, ResolvedGraphBinding, *ConfirmedExpectedKind, *ConfirmedAnchorSelection) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	g.resolveCalls++
	// CHAOS-4085: nil CommitBasisSet -- every commit this double returns reads
	// back as CommitBasisUnknown, the strict (must-be-affirmed) treatment.
	return SubjectResolution{
		Candidates: []SubjectCandidate{},
		Committed:  []SubjectRef{{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}},
	}, StructureOfferMaterial{}, nil, nil, nil
}

func (g *countingGraphReader) DiscoverContext(context.Context, storage.Principal, GraphDiscoveryRequest) (GraphContext, error) {
	g.discoverCalls++
	return GraphContext{}, nil
}
