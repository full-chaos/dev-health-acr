package contextfabric

// The shadow anchor binding driven through the real Engine.Investigate:
// the three cross-turn anchor seams reproduced with scripted resolution
// outputs, each asserting that the SHADOW binding holds the right anchor
// while the served document and the served ledger are byte-identical with
// the shadow on and off.

import (
	"context"
	"encoding/json"
	"fmt"
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
	copied.AnchorBinding = nil
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
	response   needTurnResponse
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
		if offTurn.saved != nil && offTurn.saved.AnchorBinding != nil {
			t.Fatalf("turn %d: the shadow-off arm persisted a binding %+v", i+1, offTurn.saved.AnchorBinding)
		}
		if len(offTurn.transitions) != 0 {
			t.Fatalf("turn %d: the shadow-off arm emitted %d transition lines", i+1, len(offTurn.transitions))
		}
		if len(onTurn.transitions) != 1 {
			t.Fatalf("turn %d: the shadow-on arm emitted %d transition lines, want exactly 1", i+1, len(onTurn.transitions))
		}
		if onTurn.saved == nil || onTurn.saved.AnchorBinding == nil {
			t.Fatalf("turn %d: the shadow-on arm persisted no binding", i+1)
		}
		if err := ValidateAnchorBinding(*onTurn.saved.AnchorBinding); err != nil {
			t.Fatalf("turn %d: persisted binding invalid: %v", i+1, err)
		}
		if got, want := *onTurn.saved.AnchorBinding, onTurn.transitions[0].To; !reflect.DeepEqual(got, want) {
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
	if got := *one.saved.AnchorBinding; got.State != AnchorBindingBound || got.CanonicalID != probeAlpha.CanonicalID || got.Reason != AnchorBindingReasonIdentityProven {
		t.Fatalf("turn one binding = %+v, want bound alpha on identity_proven", got)
	}
	if got := servedLedgerAnchor(two); !got.none() {
		t.Fatalf("premise: the served follow-up ledger holds %+v; the reproduced seam drops the anchor", got)
	}
	binding := *two.saved.AnchorBinding
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
	if got := servedLedgerAnchor(two); got.ID != probeBeta.CanonicalID {
		t.Fatalf("premise: the served follow-up ledger holds %+v; the reproduced seam captures beta", got)
	}
	binding := *two.saved.AnchorBinding
	want := AnchorBinding{
		State: AnchorBindingContested, Kind: probeAlpha.Kind, CanonicalID: probeAlpha.CanonicalID,
		Proof: AnchorBindingProofIdentityProven, Reason: AnchorBindingReasonContestedByResolution,
		OriginResultID: turns[0].result.ResultID, ContenderKind: probeBeta.Kind, ContenderID: probeBeta.CanonicalID,
	}
	if !reflect.DeepEqual(binding, want) {
		t.Fatalf("follow-up binding = %+v, want %+v", binding, want)
	}
	if line := two.transitions[0]; line.Agreement != AnchorBindingDisagree || line.DisagreementField != AnchorBindingFieldCarriedAnchor || !reflect.DeepEqual(line.ProvenAnchorIDs, []string{"repository:" + probeBeta.CanonicalID}) {
		t.Fatalf("follow-up line = %+v, want a carried_anchor disagreement proving beta", line)
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
	pending := *one.saved.AnchorBinding
	if pending.State != AnchorBindingPendingWindowConfirmation || pending.CanonicalID != probeAlpha.CanonicalID || pending.Proof != AnchorBindingProofIdentityProven {
		t.Fatalf("gated binding = %+v, want alpha pending on identity proof", pending)
	}
	if line := one.transitions[0]; line.Evaluation != AnchorBindingEvaluationWindowGated || line.DisagreementField != AnchorBindingFieldPendingProof {
		t.Fatalf("gated line = %+v, want window_gated with a pending_proof disagreement", line)
	}
	if got := servedLedgerAnchor(two); !got.none() {
		t.Fatalf("premise: the confirmation turn's served ledger holds %+v", got)
	}
	bound := *two.saved.AnchorBinding
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
	binding := *two.saved.AnchorBinding
	if binding.State != AnchorBindingBound || binding.CanonicalID != probeAlpha.CanonicalID || binding.Kind != SubjectRepository || binding.Reason != AnchorBindingReasonCarriedReconfirmed {
		t.Fatalf("binding = %+v, want alpha bound as a repository, carried_reconfirmed", binding)
	}
	if line.EffectiveKind != SubjectRepository || line.ModelAnchorKind != SubjectProject || line.Agreement != AnchorBindingDisagree || line.DisagreementField != AnchorBindingFieldCountAnchor {
		t.Fatalf("line = %+v, want the carried kind effective over the model's and a count_anchor disagreement", line)
	}
}

// anchorSiteOutcome is one arm of one save-site scenario.
type anchorSiteOutcome struct {
	served      []byte
	states      []*PersistedSemanticState
	saves       int
	transitions []AnchorBindingTransitionEvent
}

// anchorSiteScenario drives one exit with the shadow on or off.
type anchorSiteScenario struct {
	name  string
	site  BudgetAssertStage
	reach AnchorBindingEvaluation
	run   func(t *testing.T, telemetry *recordingTelemetry, shadowOff bool) InvestigationResult
	// zero names the AnchorBinding fields this exit leaves at their zero
	// value on the shadow-on arm; every other field must be populated.
	zero map[string]bool
}

func anchorSiteArm(t *testing.T, scenario anchorSiteScenario, shadowOff bool) anchorSiteOutcome {
	t.Helper()
	telemetry := &recordingTelemetry{}
	result := scenario.run(t, telemetry, shadowOff)
	served, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal served result: %v", err)
	}
	out := anchorSiteOutcome{served: served, saves: len(telemetry.semanticStatePersistences), transitions: telemetry.anchorBindingTransitions}
	for _, event := range telemetry.semanticStatePersistences {
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

func anchorSiteScenarios() []anchorSiteScenario {
	const maxItems = 500
	flip := func(engine *Engine, off bool) *Engine {
		engine.anchorBindingShadowDisabled = off
		return engine
	}
	probe := func(steps ...anchorProbeStep) func(*testing.T, *recordingTelemetry, bool) InvestigationResult {
		return func(t *testing.T, telemetry *recordingTelemetry, off bool) InvestigationResult {
			rig := newAnchorProbeRig(t, off)
			rig.telemetry = telemetry
			rig.engine.telemetry = telemetry
			var turns []anchorProbeTurn
			for _, step := range steps {
				rig.interpreter.read(step.anchorKind, step.windowed)
				if step.gate != nil {
					rig.interpreter.outcome.Gate = *step.gate
				}
				turns = append(turns, rig.turn(t, step.request(turns), step.response))
			}
			return turns[len(turns)-1].result
		}
	}
	notProjected := emptyProbeResponse()
	notProjected.err = fmt.Errorf("probe graph: %w", ErrGraphNotProjected)
	refused := FrameGate{Outcome: FrameGateRefusedBasis, RefuseBasis: CohortMemberKindUnservable, DeclaredMemberKind: SubjectTeam}
	return []anchorSiteScenario{
		{name: "window_veto", site: BudgetAssertWindowVeto, reach: AnchorBindingEvaluationNotResolved, zero: unboundZero,
			run: func(t *testing.T, telemetry *recordingTelemetry, off bool) InvestigationResult {
				engine := flip(buildVetoEngineWithBudget(t, telemetry, maxItems), off)
				request := validInvestigationRequest()
				request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: "result_does_not_exist_01", ReceiptID: "winr_confirm0001"}}
				return mustInvestigate(t, engine, reusePrincipal(), request)
			}},
		{name: "structure_veto", site: BudgetAssertStructureVeto, reach: AnchorBindingEvaluationNotResolved, zero: unboundZero,
			run: func(t *testing.T, telemetry *recordingTelemetry, off bool) InvestigationResult {
				engine := flip(buildVetoEngineWithBudget(t, telemetry, maxItems), off)
				request := validInvestigationRequest()
				request.PriorAnchorReceipts = []BoundSubjectReceipt{{ResultID: "result_does_not_exist_01", ReceiptID: "ancr_confirm0001"}}
				return mustInvestigate(t, engine, reusePrincipal(), request)
			}},
		{name: "window_confirmation_required_explicit", site: BudgetAssertWindowConfirmationRequired, reach: AnchorBindingEvaluationNotResolved, zero: unboundZero,
			run: func(t *testing.T, telemetry *recordingTelemetry, off bool) InvestigationResult {
				interpreter := &countingInterpreter{interpretation: bootstrapInterpretation()}
				graph := &acceptanceGraphReader{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}, context: emptyGraphContext()}
				engine := flip(buildWindowGateEngineWithBudget(t, interpreter, graph, newMapResultStore(), maxItems, telemetry), off)
				request := validInvestigationRequest()
				request.Consumer.Surface = "mcp"
				request.TimeContext.EvidenceWindow = &RequestedEvidenceWindow{RelativeID: RelativeWindowTrailing90D}
				return mustInvestigate(t, engine, acceptancePrincipal(), request)
			}},
		{name: "window_confirmation_required_class_default", site: BudgetAssertWindowConfirmationRequired, reach: AnchorBindingEvaluationWindowGated, zero: heldZero,
			run: probe(anchorProbeStep{request: firstTurnRequest("request_site_gate", false), windowed: true, response: identityProvenResponse(probeAlpha)})},
		{name: "interpreted_time_bound", site: BudgetAssertInterpretedTimeBound, reach: AnchorBindingEvaluationNotResolved, zero: unboundZero,
			run: func(t *testing.T, telemetry *recordingTelemetry, off bool) InvestigationResult {
				ancient := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC).Add(-3000 * 24 * time.Hour)
				end := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
				unanswerable := bootstrapInterpretation()
				unanswerable.TimeContext = TimeContext{Axis: TemporalRange, Start: &ancient, End: &end}
				graph := &acceptanceGraphReader{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}, context: emptyGraphContext()}
				engine := flip(buildWindowGateEngineWithBudget(t, &countingInterpreter{interpretation: unanswerable}, graph, newMapResultStore(), maxItems, telemetry), off)
				return mustInvestigate(t, engine, acceptancePrincipal(), validInvestigationRequest())
			}},
		{name: "continuation_refusal", site: BudgetAssertContinuationRefusal, reach: AnchorBindingEvaluationNotResolved, zero: unboundZero,
			run: func(t *testing.T, telemetry *recordingTelemetry, off bool) InvestigationResult {
				question := validInvestigationRequest().Question
				prior := continuationPrior(t, continuationPriorID, question, QuestionFamilyDiscoveredCohortRanking, "")
				stale := int64(97)
				store := &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}, graphEpoch: &stale}
				graph := &acceptanceGraphReader{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}, context: emptyGraphContext()}
				engine := flip(buildWindowGateEngineWithBudget(t, forcedFamilyInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: SubjectTeam}, graph, store, maxItems, telemetry), off)
				return mustInvestigate(t, engine, acceptancePrincipal(), continuationRequest(question))
			}},
		{name: "reuse", site: BudgetAssertReuse, reach: AnchorBindingEvaluationReused, zero: unboundZero,
			run: func(t *testing.T, telemetry *recordingTelemetry, off bool) InvestigationResult {
				project, candidate := reusableCandidate()
				engine := flip(buildReuseEngineWithBudget(t, project, candidate, maxItems, telemetry), off)
				return mustInvestigate(t, engine, reusePrincipal(), validInvestigationRequest())
			}},
		{name: "subjectless_terminal_ambiguous", site: BudgetAssertSubjectlessTerminal, reach: AnchorBindingEvaluationResolved, zero: unboundZero,
			run: func(t *testing.T, telemetry *recordingTelemetry, off bool) InvestigationResult {
				graphCtx := emptyGraphContext()
				graphCtx.Coverage.Sources = []SourceObservation{{Source: "context-fabric:graph", State: SourceAvailable}}
				graph := &acceptanceGraphReader{resolution: manyAmbiguousCandidates(2, "Which subject did you mean?"), context: graphCtx}
				engine := flip(buildTerminalEngineWithBudgetAndTelemetry(t, graph, newMapResultStore(), maxItems, telemetry), off)
				return mustInvestigate(t, engine, acceptancePrincipal(), validInvestigationRequestWithConfirmedWindow())
			}},
		{name: "subjectless_terminal_frame_refused", site: BudgetAssertSubjectlessTerminal, reach: AnchorBindingEvaluationNotResolved, zero: unboundZero,
			run: probe(anchorProbeStep{request: firstTurnRequest("request_site_refused", true), gate: &refused, response: identityProvenResponse(probeAlpha)})},
		{name: "subjectless_terminal_graph_not_projected", site: BudgetAssertSubjectlessTerminal, reach: AnchorBindingEvaluationNotResolved, zero: unboundZero,
			run: probe(anchorProbeStep{request: firstTurnRequest("request_site_unprojected", true), response: notProjected})},
		{name: "decisive_identity_proven", site: BudgetAssertDecisive, reach: AnchorBindingEvaluationResolved, zero: heldZero,
			run: probe(anchorProbeStep{request: firstTurnRequest("request_site_decisive", true), response: identityProvenResponse(probeAlpha)})},
		{name: "decisive_contested", site: BudgetAssertDecisive, reach: AnchorBindingEvaluationResolved, zero: map[string]bool{"GraphEpoch": true},
			run: probe(
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
			on, off := anchorSiteArm(t, scenario, false), anchorSiteArm(t, scenario, true)
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
				if off.states[i] != nil && off.states[i].AnchorBinding != nil {
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
			last := on.transitions[len(on.transitions)-1]
			if last.Site != scenario.site || last.Evaluation != scenario.reach {
				t.Fatalf("last line site/evaluation = %s/%s, want %s/%s", last.Site, last.Evaluation, scenario.site, scenario.reach)
			}
			for _, line := range on.transitions {
				if line.To.Reason == AnchorBindingReasonUnrecorded {
					t.Fatalf("a Save reached persistence with no binding decision: %+v", line)
				}
			}
			binding := last.To
			if scenario.site != BudgetAssertReuse {
				state := on.states[len(on.states)-1]
				if state == nil {
					if last.Persisted != AnchorBindingStateAbsent {
						t.Fatalf("a Save with no snapshot reported persisted=%s", last.Persisted)
					}
				} else {
					if state.AnchorBinding == nil || !reflect.DeepEqual(*state.AnchorBinding, binding) || last.Persisted != AnchorBindingPersistence(SemanticStatePersisted) {
						t.Fatalf("persisted binding %+v / persisted=%s, want the line's decision %+v persisted", state.AnchorBinding, last.Persisted, binding)
					}
				}
			}
			if err := ValidateAnchorBinding(binding); binding.Reason != AnchorBindingReasonReusedStored && err != nil {
				t.Fatalf("decided binding invalid: %v", err)
			}
			value := reflect.ValueOf(binding)
			for f := 0; f < value.NumField(); f++ {
				name := value.Type().Field(f).Name
				if isZero, wantZero := value.Field(f).IsZero(), scenario.zero[name]; isZero != wantZero {
					t.Errorf("binding field %s zero=%v, the exit declares zero=%v (binding %+v)", name, isZero, wantZero, binding)
				}
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
