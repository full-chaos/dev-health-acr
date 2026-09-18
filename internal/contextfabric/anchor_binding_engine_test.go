package contextfabric

// The shadow anchor binding driven through the real Engine.Investigate:
// the three cross-turn anchor seams reproduced with scripted resolution
// outputs, each asserting that the SHADOW binding holds the right anchor
// while the served document and the served ledger are byte-identical with
// the shadow on and off.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var (
	probeAlpha = SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:probe-alpha", Label: "probe-alpha"}
	probeBeta  = SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:probe-beta", Label: "probe-beta"}
)

// anchorProbeInterpreter is a mutable interpreter: each turn sets the
// reading it returns.
type anchorProbeInterpreter struct {
	interpreted InterpretedQuestion
	outcome     QuestionFamilyOutcome
}

func (p *anchorProbeInterpreter) Interpret(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error) {
	return p.interpreted, p.outcome, nil
}

func (p *anchorProbeInterpreter) read(anchorKind SubjectKind, windowed bool) {
	frame := committedAnchorFrame()
	p.interpreted = InterpretedQuestion{
		Shape: ShapeOpen, RequestedJudgment: "count", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{},
	}
	if windowed {
		p.interpreted.WindowClass = WindowClassTrendAssessment
	}
	p.outcome = QuestionFamilyOutcome{
		Frame: frame, FrameObligations: frame.Obligations,
		Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel,
		WinningSampleIndex: 0, WinningSample: FamilySample{ScopeAnchorKind: anchorKind},
	}
}

// anchorProbeRig is one engine over one persistent store, with the shadow on
// or off.
type anchorProbeRig struct {
	engine      *Engine
	graph       *needTurnGraph
	store       *staticResultStore
	telemetry   *recordingTelemetry
	interpreter *anchorProbeInterpreter
}

func newAnchorProbeRig(t *testing.T, shadowOff bool) *anchorProbeRig {
	t.Helper()
	rig := &anchorProbeRig{
		graph:       &needTurnGraph{},
		store:       &staticResultStore{results: map[string]InvestigationResult{}, states: map[string]*PersistedSemanticState{}},
		telemetry:   &recordingTelemetry{},
		interpreter: &anchorProbeInterpreter{},
	}
	rig.interpreter.read("", false)
	engine, err := NewEngine(EngineDependencies{
		Interpreter: rig.interpreter,
		Graph:       rig.graph,
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{}}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return validInvestigationResult(), nil
		}),
		Results:      rig.store,
		Telemetry:    rig.telemetry,
		Requirements: registryDeriver{},
	}, EngineOptions{
		ServiceVersion:             "anchor-binding-probe",
		Now:                        func() time.Time { return time.Unix(500, 0).UTC() },
		NewResultID:                resultIDSequence(),
		DisableAnchorBindingShadow: shadowOff,
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	rig.engine = engine
	return rig
}

// anchorProbeTurn is one turn's served document, its saved snapshot and the
// transition lines it emitted.
type anchorProbeTurn struct {
	result      InvestigationResult
	served      []byte
	saved       *PersistedSemanticState
	transitions []AnchorBindingTransitionEvent
}

func (r *anchorProbeRig) turn(t *testing.T, request InvestigationRequest, response needTurnResponse) anchorProbeTurn {
	t.Helper()
	r.graph.response = response
	r.store.saved, r.store.savedSemantic = nil, nil
	mark := len(r.telemetry.anchorBindingTransitions)
	result, err := r.engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate(%s) error = %v", request.RequestID, err)
	}
	if r.store.saved == nil {
		t.Fatalf("fixture defect: turn %s must save", request.RequestID)
	}
	r.store.results[r.store.saved.ResultID] = *r.store.saved
	var saved *PersistedSemanticState
	if r.store.savedSemantic != nil && r.store.savedSemantic.State != nil {
		saved = r.store.savedSemantic.State
		r.store.states[r.store.saved.ResultID] = saved
	}
	served, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal served result: %v", err)
	}
	return anchorProbeTurn{result: result, served: served, saved: saved, transitions: append([]AnchorBindingTransitionEvent(nil), r.telemetry.anchorBindingTransitions[mark:]...)}
}

// withoutBinding is the snapshot with its binding removed, for comparing the
// served state across the two arms.
func withoutBinding(state *PersistedSemanticState) *PersistedSemanticState {
	if state == nil {
		return nil
	}
	copied := cloneSemanticState(state)
	setBindingMember(copied, nil)
	return copied
}

// identityProvenCandidate is a candidate resolution matched on the frame's
// own anchor term, committed on an authoritative identity.
func identityProvenResponse(subjects ...SubjectRef) needTurnResponse {
	resolution := SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}
	bases := CommitBasisSet{}
	for _, subject := range subjects {
		resolution.Committed = append(resolution.Committed, subject)
		resolution.Candidates = append(resolution.Candidates, SubjectCandidate{
			ReceiptID: "receipt_probe_" + subject.Label, Subject: subject, State: ResolutionCommitted,
			MatchedTerms: []string{"a"}, MatchReasons: []string{"matched"}, Confidence: 1, EvidenceRefIDs: []string{},
		})
		bases.Record(subject, CommitBasisAuthoritativeIdentity)
	}
	return needTurnResponse{resolution: resolution, bases: bases}
}

func emptyProbeResponse() needTurnResponse {
	return needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}, bases: CommitBasisSet{}}
}

// anchorProbeStep is one scripted turn.
type anchorProbeStep struct {
	request    func(prior []anchorProbeTurn) InvestigationRequest
	anchorKind SubjectKind
	windowed   bool
	gate       *FrameGate
	// seed, when set, edits the store before the turn runs.
	seed     func(*staticResultStore)
	response needTurnResponse
}

// runAnchorProbe runs the same script on a shadow-on and a shadow-off rig and
// proves the two arms serve identical bytes and persist identical snapshots
// apart from the binding. It returns the shadow-on turns.
func runAnchorProbe(t *testing.T, steps []anchorProbeStep) []anchorProbeTurn {
	t.Helper()
	on, off := newAnchorProbeRig(t, false), newAnchorProbeRig(t, true)
	var onTurns, offTurns []anchorProbeTurn
	for i, step := range steps {
		on.interpreter.read(step.anchorKind, step.windowed)
		off.interpreter.read(step.anchorKind, step.windowed)
		onTurn := on.turn(t, step.request(onTurns), step.response)
		offTurn := off.turn(t, step.request(offTurns), step.response)
		if string(onTurn.served) != string(offTurn.served) {
			t.Fatalf("turn %d served bytes differ with the shadow on:\n on=%s\noff=%s", i+1, onTurn.served, offTurn.served)
		}
		if !SemanticStatesEqual(withoutBinding(onTurn.saved), offTurn.saved) {
			t.Fatalf("turn %d persisted snapshot differs beyond the binding:\n on=%+v\noff=%+v", i+1, withoutBinding(onTurn.saved), offTurn.saved)
		}
		if offTurn.saved != nil && bindingMember(offTurn.saved) != nil {
			t.Fatalf("turn %d: the shadow-off arm persisted a binding %+v", i+1, bindingMember(offTurn.saved))
		}
		if len(offTurn.transitions) != 0 {
			t.Fatalf("turn %d: the shadow-off arm emitted %d transition lines", i+1, len(offTurn.transitions))
		}
		if len(onTurn.transitions) != 1 {
			t.Fatalf("turn %d: the shadow-on arm emitted %d transition lines, want exactly 1", i+1, len(onTurn.transitions))
		}
		if onTurn.saved == nil || bindingMember(onTurn.saved) == nil {
			t.Fatalf("turn %d: the shadow-on arm persisted no binding", i+1)
		}
		if err := ValidateAnchorBinding(*bindingMember(onTurn.saved)); err != nil {
			t.Fatalf("turn %d: persisted binding invalid: %v", i+1, err)
		}
		if got, want := *bindingMember(onTurn.saved), onTurn.transitions[0].To; !reflect.DeepEqual(got, want) {
			t.Fatalf("turn %d: persisted binding %+v differs from the line's decision %+v", i+1, got, want)
		}
		onTurns, offTurns = append(onTurns, onTurn), append(offTurns, offTurn)
	}
	return onTurns
}

func servedLedgerAnchor(turn anchorProbeTurn) anchorRef {
	if turn.saved == nil {
		return anchorRef{}
	}
	return ledgerAnchor(turn.saved.ConfirmedNeeds)
}

func firstTurnRequest(id string, statedWindow bool) func([]anchorProbeTurn) InvestigationRequest {
	return func([]anchorProbeTurn) InvestigationRequest { return needTurnRequest(id, statedWindow) }
}

// followUp continues the previous turn, optionally with a different question.
func followUp(id, question string, mutate func(*InvestigationRequest)) func([]anchorProbeTurn) InvestigationRequest {
	return func(prior []anchorProbeTurn) InvestigationRequest {
		request := continuingNeedTurn(needTurnRequest(id, true), prior[len(prior)-1].result.ResultID)
		if question != "" {
			request.Question = question
		}
		if mutate != nil {
			mutate(&request)
		}
		return request
	}
}

// TestShadowBindingKeepsTheProvenRepositoryAcrossANaturalFollowUp is the shadow counterpart of the design-review probe
// TestAnchorDesignReviewNaturalFollowup. Turn one
// proves alpha; the follow-up asks a different question naming the parent.
// Served: the ledger drops alpha (question changed). Shadow: alpha stays
// bound, and the line says the served ledger disagrees.
func TestShadowBindingKeepsTheProvenRepositoryAcrossANaturalFollowUp(t *testing.T) {
	turns := runAnchorProbe(t, []anchorProbeStep{
		{request: firstTurnRequest("request_probe_nat_one", true), response: identityProvenResponse(probeAlpha)},
		{request: followUp("request_probe_nat_two", "And how many teams contribute to it?", nil), response: emptyProbeResponse()},
	})
	one, two := turns[0], turns[1]
	if got := *bindingMember(one.saved); got.State != AnchorBindingBound || got.CanonicalID != probeAlpha.CanonicalID || got.Reason != AnchorBindingReasonIdentityProven {
		t.Fatalf("turn one binding = %+v, want bound alpha on identity_proven", got)
	}
	if got := servedLedgerAnchor(two); !got.none() {
		t.Fatalf("premise: the served follow-up ledger holds %+v; the reproduced seam drops the anchor", got)
	}
	binding := *bindingMember(two.saved)
	if binding.State != AnchorBindingBound || binding.CanonicalID != probeAlpha.CanonicalID || binding.Reason != AnchorBindingReasonCarriedSilent || binding.OriginResultID != one.result.ResultID {
		t.Fatalf("follow-up binding = %+v, want alpha bound, carried_silent, origin %s", binding, one.result.ResultID)
	}
	line := two.transitions[0]
	if line.ParentBinding != AnchorBindingParentPresent || line.Agreement != AnchorBindingDisagree || line.DisagreementField != AnchorBindingFieldCarriedAnchor {
		t.Fatalf("follow-up line = %+v, want parent present and a carried_anchor disagreement", line)
	}
}

// TestShadowBindingNeverBindsAnAliasThatWinsOnAFollowUp is the shadow counterpart of the design-review probe
// TestAnchorDesignReviewWrongSubject. The follow-up's
// resolution proves beta through the anchor term. Served: beta is captured
// and counted. Shadow: alpha is retained, beta is recorded as the contender,
// and nothing is effective.
func TestShadowBindingNeverBindsAnAliasThatWinsOnAFollowUp(t *testing.T) {
	turns := runAnchorProbe(t, []anchorProbeStep{
		{request: firstTurnRequest("request_probe_alias_one", true), response: identityProvenResponse(probeAlpha)},
		{request: followUp("request_probe_alias_two", "And how many teams contribute to it?", nil), response: identityProvenResponse(probeBeta)},
	})
	two := turns[1]
	// The follow-up names the parent and its own text resolves to beta. The
	// subject-substitution guard withholds beta -- the turn clarifies and
	// serves no subject -- so the served ledger captures nothing, and the
	// binder still sees the proof the engine made.
	if two.result.Status != InvestigationClarificationRequired || len(two.result.SubjectResolution.Committed) != 0 {
		t.Fatalf("premise: the follow-up served %q with %d committed; the guard withholds beta", two.result.Status, len(two.result.SubjectResolution.Committed))
	}
	if got := servedLedgerAnchor(two); !got.none() {
		t.Fatalf("premise: the served follow-up ledger holds %+v; a withheld subject is never captured", got)
	}
	binding := *bindingMember(two.saved)
	want := AnchorBinding{
		State: AnchorBindingContested, Kind: probeAlpha.Kind, CanonicalID: probeAlpha.CanonicalID,
		Proof: AnchorBindingProofIdentityProven, Reason: AnchorBindingReasonContestedByResolution,
		OriginResultID: turns[0].result.ResultID, ContenderKind: probeBeta.Kind, ContenderID: probeBeta.CanonicalID,
	}
	if !reflect.DeepEqual(binding, want) {
		t.Fatalf("follow-up binding = %+v, want %+v", binding, want)
	}
	// Nothing is served on the guarded turn, so the binder's held anchor and
	// the served document agree that neither carries beta forward; the line
	// still proves beta, and names the guard that withheld it.
	if line := two.transitions[0]; line.Agreement != AnchorBindingAgree || line.DisagreementField != AnchorBindingFieldNone ||
		!reflect.DeepEqual(line.ProvenAnchorIDs, []string{"repository:" + probeBeta.CanonicalID}) || !line.SubstitutionGuard.Fired() {
		t.Fatalf("follow-up line = %+v, want an agreement over a withheld beta that the line still proves", line)
	}
}

// TestShadowBindingKeepsWindowGatedProofAsPending is the shadow counterpart of the design-review probe
// TestAnchorDesignReviewWindowDiscard. Turn one ends on the
// class-default window gate after its offers-only resolution proved alpha.
// Served: the proof is discarded. Shadow: alpha is pending, and the
// confirmation turn binds it.
func TestShadowBindingKeepsWindowGatedProofAsPending(t *testing.T) {
	turns := runAnchorProbe(t, []anchorProbeStep{
		{request: firstTurnRequest("request_probe_gate_one", false), windowed: true, response: identityProvenResponse(probeAlpha)},
		{request: followUp("request_probe_gate_two", "", func(r *InvestigationRequest) {
			r.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectTeam}
		}), response: emptyProbeResponse()},
	})
	one, two := turns[0], turns[1]
	if one.result.Status != InvestigationClarificationRequired {
		t.Fatalf("premise: turn one status = %q, want the window gate's clarification", one.result.Status)
	}
	if got := servedLedgerAnchor(one); !got.none() {
		t.Fatalf("premise: the gated turn's served ledger holds %+v; the reproduced seam discards it", got)
	}
	pending := *bindingMember(one.saved)
	if pending.State != AnchorBindingPendingWindowConfirmation || pending.CanonicalID != probeAlpha.CanonicalID || pending.Proof != AnchorBindingProofIdentityProven {
		t.Fatalf("gated binding = %+v, want alpha pending on identity proof", pending)
	}
	if line := one.transitions[0]; line.Evaluation != AnchorBindingEvaluationWindowGated || line.DisagreementField != AnchorBindingFieldPendingProof {
		t.Fatalf("gated line = %+v, want window_gated with a pending_proof disagreement", line)
	}
	if got := servedLedgerAnchor(two); !got.none() {
		t.Fatalf("premise: the confirmation turn's served ledger holds %+v", got)
	}
	bound := *bindingMember(two.saved)
	if bound.State != AnchorBindingBound || bound.CanonicalID != probeAlpha.CanonicalID || bound.Reason != AnchorBindingReasonWindowConfirmed || bound.OriginResultID != one.result.ResultID {
		t.Fatalf("confirmation binding = %+v, want alpha bound on window_confirmed from %s", bound, one.result.ResultID)
	}
}

// TestShadowBindingHoldsOneAnchorWhenTheModelKindConflicts is the shadow counterpart of the design-review probe
// TestAnchorDesignReviewConflictingKind. Turn two repeats
// the question, the carried alpha is recommitted, and the model states a
// different anchor kind. Served: the ledger applies alpha while the count
// decision refuses it. Shadow: alpha is the one bound anchor under its own
// kind, and the line names the count disagreement.
func TestShadowBindingHoldsOneAnchorWhenTheModelKindConflicts(t *testing.T) {
	turns := runAnchorProbe(t, []anchorProbeStep{
		{request: firstTurnRequest("request_probe_kind_one", true), anchorKind: SubjectRepository, response: identityProvenResponse(probeAlpha)},
		{request: followUp("request_probe_kind_two", "", nil), anchorKind: SubjectProject, response: needTurnResponse{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{probeAlpha}},
			bases:      provenCommitBases(probeAlpha),
		}},
	})
	two := turns[1]
	if got := servedLedgerAnchor(two); got.ID != probeAlpha.CanonicalID {
		t.Fatalf("premise: the served ledger holds %+v; the reproduced seam applies alpha", got)
	}
	line := two.transitions[0]
	if line.ServedCount != string(CountPopulationScopeAnchorUnresolved) {
		t.Fatalf("premise: served count decision = %q, want anchor_unresolved", line.ServedCount)
	}
	binding := *bindingMember(two.saved)
	if binding.State != AnchorBindingBound || binding.CanonicalID != probeAlpha.CanonicalID || binding.Kind != SubjectRepository || binding.Reason != AnchorBindingReasonCarriedReconfirmed {
		t.Fatalf("binding = %+v, want alpha bound as a repository, carried_reconfirmed", binding)
	}
	if line.EffectiveKind != SubjectRepository || line.ModelAnchorKind != SubjectProject || line.Agreement != AnchorBindingDisagree || line.DisagreementField != AnchorBindingFieldCountAnchor {
		t.Fatalf("line = %+v, want the carried kind effective over the model's and a count_anchor disagreement", line)
	}
}

// anchorTeeTelemetry records every call and writes the transition line
// through the production slog sink as well.
type anchorTeeTelemetry struct {
	*recordingTelemetry
	sink SlogEngineTelemetry
}

func (a anchorTeeTelemetry) RecordAnchorBindingTransition(ctx context.Context, principal storage.Principal, event AnchorBindingTransitionEvent) {
	a.recordingTelemetry.RecordAnchorBindingTransition(ctx, principal, event)
	a.sink.RecordAnchorBindingTransition(ctx, principal, event)
}

// anchorSiteOutcome is one arm of one save-site scenario.
type anchorSiteOutcome struct {
	served      []byte
	states      []*PersistedSemanticState
	saves       int
	transitions []AnchorBindingTransitionEvent
	rec         *recordingTelemetry
}

// anchorSiteScenario drives one exit with the shadow on or off. run builds
// its engine with sink as the engine's telemetry and rec as the recorder the
// scenario's own harness reads.
type anchorSiteScenario struct {
	name  string
	site  BudgetAssertStage
	reach AnchorBindingEvaluation
	run   func(t *testing.T, sink EngineTelemetry, rec *recordingTelemetry, off bool) InvestigationResult
	// zero names the AnchorBinding fields this exit leaves at their zero
	// value on the shadow-on arm; every other field must be populated.
	zero map[string]bool
	// check, when set, asserts scenario-specific facts on the shadow-on arm.
	check func(t *testing.T, on anchorSiteOutcome)
}

func anchorSiteArm(t *testing.T, scenario anchorSiteScenario, shadowOff bool, log *bytes.Buffer) anchorSiteOutcome {
	t.Helper()
	rec := &recordingTelemetry{}
	var sink EngineTelemetry = rec
	if log != nil {
		sink = anchorTeeTelemetry{recordingTelemetry: rec, sink: NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(log, &slog.HandlerOptions{Level: slog.LevelInfo})))}
	}
	result := scenario.run(t, sink, rec, shadowOff)
	served, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal served result: %v", err)
	}
	out := anchorSiteOutcome{served: served, saves: len(rec.semanticStatePersistences), transitions: rec.anchorBindingTransitions, rec: rec}
	for _, event := range rec.semanticStatePersistences {
		out.states = append(out.states, event.State)
	}
	return out
}

// unboundZero are the fields an unbound binding leaves empty on an engine
// whose graph epoch is zero.
var unboundZero = map[string]bool{"Kind": true, "CanonicalID": true, "OriginResultID": true, "GraphEpoch": true, "ContenderKind": true, "ContenderID": true}

// heldZero are the fields a held, uncontested binding leaves empty on an
// engine whose graph epoch is zero.
var heldZero = map[string]bool{"GraphEpoch": true, "ContenderKind": true, "ContenderID": true}

// anchorProbeScenario runs scripted probe turns on a fresh rig; the last
// turn's served document is the scenario's.
func anchorProbeScenario(steps ...anchorProbeStep) func(*testing.T, EngineTelemetry, *recordingTelemetry, bool) InvestigationResult {
	return func(t *testing.T, sink EngineTelemetry, rec *recordingTelemetry, off bool) InvestigationResult {
		rig := newAnchorProbeRig(t, off)
		rig.telemetry = rec
		rig.engine.telemetry = sink
		var turns []anchorProbeTurn
		for _, step := range steps {
			rig.interpreter.read(step.anchorKind, step.windowed)
			if step.gate != nil {
				rig.interpreter.outcome.Gate = *step.gate
			}
			if step.seed != nil {
				step.seed(rig.store)
			}
			turns = append(turns, rig.turn(t, step.request(turns), step.response))
		}
		return turns[len(turns)-1].result
	}
}

func lastLineHas(field string, want func(AnchorBindingTransitionEvent) bool) func(*testing.T, anchorSiteOutcome) {
	return func(t *testing.T, on anchorSiteOutcome) {
		t.Helper()
		if len(on.transitions) == 0 {
			t.Fatalf("no transition line to check %s on", field)
		}
		if line := on.transitions[len(on.transitions)-1]; !want(line) {
			t.Fatalf("last line %s check failed: %+v", field, line)
		}
	}
}

// anchorWorkItemTupleScenario runs one work-item-tuple turn whose own
// resolution ends on a pre-discovery terminal.
func anchorWorkItemTupleScenario(committed []SubjectRef, authorized bool) func(*testing.T, EngineTelemetry, *recordingTelemetry, bool) InvestigationResult {
	return func(t *testing.T, sink EngineTelemetry, _ *recordingTelemetry, off bool) InvestigationResult {
		engine, graph, store, _ := buildWorkItemTupleCarryEngine(t, "request_site_tuple_unused", "request_site_tuple")
		engine.telemetry, engine.anchorBindingShadowDisabled = sink, off
		if !authorized {
			engine.candidateVerifier = func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, SubjectKind, string) (bool, CandidateVerificationReason) {
				return false, CandidateVerificationValid
			}
		}
		graph.response = needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: committed}, bases: provenCommitBases(committed...)}
		result := mustInvestigate(t, engine, acceptancePrincipal(), needTurnRequest("request_site_tuple", true))
		if store.saved == nil {
			t.Fatalf("fixture defect: the tuple terminal must save")
		}
		if len(result.SubjectResolution.Committed) != 0 {
			t.Fatalf("premise: the tuple terminal committed %+v", result.SubjectResolution.Committed)
		}
		return result
	}
}

func anchorSiteScenarios() []anchorSiteScenario {
	const maxItems = 500
	flip := func(engine *Engine, off bool) *Engine {
		engine.anchorBindingShadowDisabled = off
		return engine
	}
	notProjected := emptyProbeResponse()
	notProjected.err = fmt.Errorf("probe graph: %w", ErrGraphNotProjected)
	refused := FrameGate{Outcome: FrameGateRefusedBasis, RefuseBasis: CohortMemberKindUnservable, DeclaredMemberKind: SubjectTeam}
	return []anchorSiteScenario{
		{name: "window_veto_pre_interpretation", site: BudgetAssertWindowVeto, reach: AnchorBindingEvaluationNotResolved, zero: unboundZero,
			run: func(t *testing.T, sink EngineTelemetry, _ *recordingTelemetry, off bool) InvestigationResult {
				engine := flip(buildVetoEngineWithBudget(t, sink, maxItems), off)
				request := validInvestigationRequest()
				request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: "result_does_not_exist_01", ReceiptID: "winr_confirm0001"}}
				return mustInvestigate(t, engine, reusePrincipal(), request)
			}},
		{name: "window_veto_axis_conflict", site: BudgetAssertWindowVeto, reach: AnchorBindingEvaluationNotResolved, zero: unboundZero,
			run: func(t *testing.T, sink EngineTelemetry, rec *recordingTelemetry, off bool) InvestigationResult {
				h := newNeedTurnHarness(t, nil)
				h.telemetry, h.engine.telemetry, h.engine.anchorBindingShadowDisabled = rec, sink, off
				one, option := windowTurnOne(t, h, "request_site_axis_one")
				h.historical = true
				request := needTurnRequest("request_site_axis_two", false)
				request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: one.result.ResultID, ReceiptID: option.ReceiptID}}
				request.ParentResultID = one.result.ResultID
				two := h.turn(request, committingNeedResponse())
				axis := false
				for _, outcome := range two.windowCanons {
					axis = axis || outcome == WindowCanonicalizationVetoAxisConflict
				}
				if !axis || two.result.Status != InvestigationNoMatch {
					t.Fatalf("premise: turn two is not the axis-conflict veto (status %s)", two.result.Status)
				}
				return two.result
			},
			check: lastLineHas("parent", func(l AnchorBindingTransitionEvent) bool {
				return l.ParentBinding == AnchorBindingParentPresent && l.CarryChecks == AnchorBindingCarryChecksNotEvaluated && l.From.State == AnchorBindingUnbound
			})},
		{name: "structure_veto", site: BudgetAssertStructureVeto, reach: AnchorBindingEvaluationNotResolved, zero: unboundZero,
			run: func(t *testing.T, sink EngineTelemetry, _ *recordingTelemetry, off bool) InvestigationResult {
				engine := flip(buildVetoEngineWithBudget(t, sink, maxItems), off)
				request := validInvestigationRequest()
				request.PriorAnchorReceipts = []BoundSubjectReceipt{{ResultID: "result_does_not_exist_01", ReceiptID: "ancr_confirm0001"}}
				return mustInvestigate(t, engine, reusePrincipal(), request)
			}},
		{name: "structure_veto_supersession_race", site: BudgetAssertStructureVeto, reach: AnchorBindingEvaluationResolved, zero: unboundZero,
			run: func(t *testing.T, sink EngineTelemetry, _ *recordingTelemetry, off bool) InvestigationResult {
				prior := validInvestigationResult()
				prior.ResultID = "result_prior_structure_race"
				prior.StructureNeeds = &StructureNeeds{
					Missing: []StructureNeedKind{"subject_anchor"},
					AnchorOptions: []AnchorOption{{ReceiptID: "ancr_confirm0001", OptionID: "opt_anchor", Label: "the race repository",
						Kind: SubjectRepository, CanonicalID: "repository_race", MatchedTermHash: "aa11bb22cc33dd44ee55ff66", OfferSource: "engine"}},
				}
				store := &supersessionRacingResultStore{
					staticResultStore: &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
					conflictMembers:   []contractsv1.ContextFabricStructureNeedKind{"subject_anchor"},
				}
				project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
				engine := mustReuseTestEngine(t, EngineDependencies{
					Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
					Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
						return CanonicalFactBundle{}, nil
					}),
					Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
						return validInvestigationResult(), nil
					}),
					Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
						return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
					}),
					AnchorVerifier: func(context.Context, string, contractsv1.ContextFabricSubjectKind, string, string) (bool, AnchorVerificationReason) {
						return true, AnchorVerificationValid
					},
					Results: store,
				})
				engine.telemetry, engine.anchorBindingShadowDisabled = sink, off
				request := validInvestigationRequest()
				request.PriorAnchorReceipts = []BoundSubjectReceipt{{ResultID: prior.ResultID, ReceiptID: "ancr_confirm0001"}}
				result := mustInvestigate(t, engine, reusePrincipal(), request)
				if store.saveCalls != 2 {
					t.Fatalf("premise: the race must save twice, saved %d times", store.saveCalls)
				}
				return result
			},
			check: func(t *testing.T, on anchorSiteOutcome) {
				if len(on.transitions) != 2 || on.transitions[0].Site != BudgetAssertDecisive || on.transitions[0].Persisted != AnchorBindingPersistence(SemanticStateSupersededDecision) {
					t.Fatalf("the lost Save must report persisted=superseded at the decisive site: %+v", on.transitions)
				}
				// The public decision vetoed the redeemed receipt, so the
				// binding the veto Save stores asserts no anchor at all.
				if on.transitions[1].To.State != AnchorBindingUnbound || on.transitions[1].To.Reason != AnchorBindingReasonNoProof {
					t.Fatalf("the veto Save's binding = %+v, want unbound after the public veto", on.transitions[1].To)
				}
			}},
		{name: "window_confirmation_required_explicit", site: BudgetAssertWindowConfirmationRequired, reach: AnchorBindingEvaluationNotResolved, zero: unboundZero,
			run: func(t *testing.T, sink EngineTelemetry, _ *recordingTelemetry, off bool) InvestigationResult {
				interpreter := &countingInterpreter{interpretation: bootstrapInterpretation()}
				graph := &acceptanceGraphReader{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}, context: emptyGraphContext()}
				engine := flip(buildWindowGateEngineWithBudget(t, interpreter, graph, newMapResultStore(), maxItems, sink), off)
				request := validInvestigationRequest()
				request.Consumer.Surface = "mcp"
				request.TimeContext.EvidenceWindow = &RequestedEvidenceWindow{RelativeID: RelativeWindowTrailing90D}
				return mustInvestigate(t, engine, acceptancePrincipal(), request)
			}},
		{name: "window_confirmation_required_class_default", site: BudgetAssertWindowConfirmationRequired, reach: AnchorBindingEvaluationWindowGated, zero: heldZero,
			run: anchorProbeScenario(anchorProbeStep{request: firstTurnRequest("request_site_gate", false), windowed: true, response: identityProvenResponse(probeAlpha)})},
		{name: "interpreted_time_bound", site: BudgetAssertInterpretedTimeBound, reach: AnchorBindingEvaluationNotResolved, zero: unboundZero,
			run: func(t *testing.T, sink EngineTelemetry, _ *recordingTelemetry, off bool) InvestigationResult {
				ancient := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC).Add(-3000 * 24 * time.Hour)
				end := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
				unanswerable := bootstrapInterpretation()
				unanswerable.TimeContext = TimeContext{Axis: TemporalRange, Start: &ancient, End: &end}
				graph := &acceptanceGraphReader{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}, context: emptyGraphContext()}
				engine := flip(buildWindowGateEngineWithBudget(t, &countingInterpreter{interpretation: unanswerable}, graph, newMapResultStore(), maxItems, sink), off)
				return mustInvestigate(t, engine, acceptancePrincipal(), validInvestigationRequest())
			},
			check: lastLineHas("persisted", func(l AnchorBindingTransitionEvent) bool { return l.Persisted == AnchorBindingStateAbsent })},
		{name: "continuation_refusal", site: BudgetAssertContinuationRefusal, reach: AnchorBindingEvaluationNotResolved, zero: unboundZero,
			run: func(t *testing.T, sink EngineTelemetry, _ *recordingTelemetry, off bool) InvestigationResult {
				question := validInvestigationRequest().Question
				prior := continuationPrior(t, continuationPriorID, question, QuestionFamilyDiscoveredCohortRanking, "")
				stale := int64(97)
				store := &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}, graphEpoch: &stale}
				graph := &acceptanceGraphReader{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}, context: emptyGraphContext()}
				engine := flip(buildWindowGateEngineWithBudget(t, forcedFamilyInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: SubjectTeam}, graph, store, maxItems, sink), off)
				return mustInvestigate(t, engine, acceptancePrincipal(), continuationRequest(question))
			}},
		{name: "reuse", site: BudgetAssertReuse, reach: AnchorBindingEvaluationReused, zero: unboundZero,
			run: func(t *testing.T, sink EngineTelemetry, _ *recordingTelemetry, off bool) InvestigationResult {
				project, candidate := reusableCandidate()
				engine := flip(buildReuseEngineWithBudget(t, project, candidate, maxItems, sink), off)
				return mustInvestigate(t, engine, reusePrincipal(), validInvestigationRequest())
			}},
		{name: "subjectless_terminal_ambiguous", site: BudgetAssertSubjectlessTerminal, reach: AnchorBindingEvaluationResolved, zero: unboundZero,
			run: func(t *testing.T, sink EngineTelemetry, _ *recordingTelemetry, off bool) InvestigationResult {
				graphCtx := emptyGraphContext()
				graphCtx.Coverage.Sources = []SourceObservation{{Source: "context-fabric:graph", State: SourceAvailable}}
				graph := &acceptanceGraphReader{resolution: manyAmbiguousCandidates(2, "Which subject did you mean?"), context: graphCtx}
				engine := flip(buildTerminalEngineWithBudgetAndTelemetry(t, graph, newMapResultStore(), maxItems, sink), off)
				return mustInvestigate(t, engine, acceptancePrincipal(), validInvestigationRequestWithConfirmedWindow())
			}},
		{name: "subjectless_terminal_frame_refused", site: BudgetAssertSubjectlessTerminal, reach: AnchorBindingEvaluationNotResolved, zero: unboundZero,
			run: anchorProbeScenario(anchorProbeStep{request: firstTurnRequest("request_site_refused", true), gate: &refused, response: identityProvenResponse(probeAlpha)})},
		{name: "subjectless_terminal_graph_not_projected", site: BudgetAssertSubjectlessTerminal, reach: AnchorBindingEvaluationNotResolved, zero: unboundZero,
			run: anchorProbeScenario(anchorProbeStep{request: firstTurnRequest("request_site_unprojected", true), response: notProjected})},
		{name: "subjectless_terminal_work_item_tuple_refused", site: BudgetAssertSubjectlessTerminal, reach: AnchorBindingEvaluationResolved, zero: unboundZero,
			run: anchorWorkItemTupleScenario([]SubjectRef{workItemTupleCarryAnchor, workItemTupleCarryAnchorOther}, true)},
		{name: "subjectless_terminal_work_item_tuple_unauthorized", site: BudgetAssertSubjectlessTerminal, reach: AnchorBindingEvaluationResolved, zero: unboundZero,
			run: anchorWorkItemTupleScenario([]SubjectRef{workItemTupleCarryAnchor}, false)},
		{name: "subjectless_terminal_group_axis_collapsed", site: BudgetAssertSubjectlessTerminal, reach: AnchorBindingEvaluationResolved, zero: unboundZero,
			run: func(t *testing.T, sink EngineTelemetry, _ *recordingTelemetry, off bool) InvestigationResult {
				recorder := &groupReadRecorder{facts: func(CanonicalFactRequest) CanonicalFactBundle {
					bundle := emptyFactBundle()
					bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
					return bundle
				}}
				engine, request := groupReadEngineFixtureSelfGroup(t, sink, recorder)
				engine.anchorBindingShadowDisabled = off
				if engine.results == nil {
					engine.results = newMapResultStore()
				}
				result := mustInvestigate(t, engine, storage.Principal{OrgID: "org_1"}, request)
				if result.RefusalBasis != contractsv1.ContextFabricRefusalBasisFrameInvariantViolated {
					t.Fatalf("premise: not the collapsed-axis refusal (basis %q)", result.RefusalBasis)
				}
				return result
			}},
		{name: "decisive_identity_proven", site: BudgetAssertDecisive, reach: AnchorBindingEvaluationResolved, zero: heldZero,
			run: anchorProbeScenario(anchorProbeStep{request: firstTurnRequest("request_site_decisive", true), response: identityProvenResponse(probeAlpha)})},
		// A follow-up whose own resolution commits a different subject is
		// withheld by the subject-substitution guard and saved at the
		// subjectless terminal, where the binder records the contest.
		{name: "guarded_contested", site: BudgetAssertSubjectlessTerminal, reach: AnchorBindingEvaluationResolved, zero: map[string]bool{"GraphEpoch": true},
			run: anchorProbeScenario(
				anchorProbeStep{request: firstTurnRequest("request_site_contest_one", true), response: identityProvenResponse(probeAlpha)},
				anchorProbeStep{request: followUp("request_site_contest_two", "And how many teams contribute to it?", nil), response: identityProvenResponse(probeBeta)},
			)},
	}
}

func mustInvestigate(t *testing.T, engine *Engine, principal storage.Principal, request InvestigationRequest) InvestigationResult {
	t.Helper()
	result, err := engine.Investigate(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("Investigate(%s) error = %v", request.RequestID, err)
	}
	return result
}

// TestAnchorBindingShadowParityAtEverySaveSite drives every exit that saves
// or serves through the real engine, twice: the served bytes and the
// persisted snapshots (binding aside) are identical with the shadow on and
// off; the shadow-on arm persists a valid binding at every Save whose capture
// has a snapshot, emits exactly one transition line per Save (one per reuse
// serve), never an unrecorded one, and populates exactly the binding fields
// the exit declares.
func TestAnchorBindingShadowParityAtEverySaveSite(t *testing.T) {
	seen := map[BudgetAssertStage]bool{}
	reached := map[AnchorBindingEvaluation]bool{}
	for _, scenario := range anchorSiteScenarios() {
		scenario := scenario
		t.Run(scenario.name, func(t *testing.T) {
			on, off := anchorSiteArm(t, scenario, false, nil), anchorSiteArm(t, scenario, true, nil)
			if string(on.served) != string(off.served) {
				t.Fatalf("served bytes differ with the shadow on:\n on=%s\noff=%s", on.served, off.served)
			}
			if on.saves != off.saves || len(on.states) != len(off.states) {
				t.Fatalf("saves differ: on=%d off=%d", on.saves, off.saves)
			}
			for i := range on.states {
				if !SemanticStatesEqual(withoutBinding(on.states[i]), off.states[i]) {
					t.Fatalf("save %d snapshot differs beyond the binding", i)
				}
				if off.states[i] != nil && bindingMember(off.states[i]) != nil {
					t.Fatalf("save %d: the shadow-off arm persisted a binding", i)
				}
			}
			if len(off.transitions) != 0 {
				t.Fatalf("the shadow-off arm emitted %d transition lines", len(off.transitions))
			}
			wantLines := on.saves
			if scenario.site == BudgetAssertReuse {
				wantLines = 1
			}
			if len(on.transitions) != wantLines {
				t.Fatalf("the shadow-on arm emitted %d transition lines for %d saves", len(on.transitions), on.saves)
			}
			if len(on.transitions) == 0 {
				t.Fatalf("the shadow-on arm emitted no transition line")
			}
			last := on.transitions[len(on.transitions)-1]
			if last.Site != scenario.site || last.Evaluation != scenario.reach {
				t.Fatalf("last line site/evaluation = %s/%s, want %s/%s", last.Site, last.Evaluation, scenario.site, scenario.reach)
			}
			// THE DECLARED MULTIPLICITY, COUNTED ON EVERY SAVE-SITE PATH.
			// eventspec declares exactly one line per ATTEMPT, and this
			// event's attempt is its whole Attribution: (org_id, result_id,
			// site). One request carries as many lines as it saved results --
			// a decisive Save that loses a structure claim saves a second
			// result at the structure_veto site -- and never two lines for
			// one result at one site.
			attempts := map[string]bool{}
			for _, line := range on.transitions {
				key := line.ResultID + "\x00" + string(line.Site)
				if attempts[key] {
					t.Fatalf("two lines for one (result_id, site): %q / %s -- the declared multiplicity is exactly one per attempt", line.ResultID, line.Site)
				}
				attempts[key] = true
			}
			for _, line := range on.transitions {
				if line.To.Reason == AnchorBindingReasonUnrecorded {
					t.Fatalf("a Save reached persistence with no binding decision: %+v", line)
				}
				if (line.CarryChecks == AnchorBindingCarryChecksNotEvaluated) != (line.ParentBinding == AnchorBindingParentPresent) {
					t.Fatalf("carry_checks %s disagrees with parent_binding %s", line.CarryChecks, line.ParentBinding)
				}
			}
			binding := last.To
			if scenario.site != BudgetAssertReuse {
				state := on.states[len(on.states)-1]
				if state == nil {
					if last.Persisted != AnchorBindingStateAbsent {
						t.Fatalf("a Save with no snapshot reported persisted=%s", last.Persisted)
					}
				} else if bindingMember(state) == nil || !reflect.DeepEqual(*bindingMember(state), binding) || last.Persisted != AnchorBindingPersistence(SemanticStatePersisted) {
					t.Fatalf("persisted binding %+v / persisted=%s, want the line's decision %+v persisted", bindingMember(state), last.Persisted, binding)
				}
			}
			if err := ValidateAnchorBinding(binding); err != nil {
				t.Fatalf("decided binding invalid: %v", err)
			}
			value := reflect.ValueOf(binding)
			for f := 0; f < value.NumField(); f++ {
				name := value.Type().Field(f).Name
				if isZero, wantZero := value.Field(f).IsZero(), scenario.zero[name]; isZero != wantZero {
					t.Errorf("binding field %s zero=%v, the exit declares zero=%v (binding %+v)", name, isZero, wantZero, binding)
				}
			}
			if scenario.check != nil {
				scenario.check(t, on)
			}
			seen[scenario.site] = true
			reached[scenario.reach] = true
		})
	}
	for _, stage := range BudgetAssertStageVocabulary() {
		if !seen[stage] {
			t.Errorf("no scenario drove the %q exit", stage)
		}
	}
	for _, evaluation := range anchorBindingEvaluations() {
		if !reached[evaluation] {
			t.Errorf("no scenario reached evaluation %q", evaluation)
		}
	}
}
