package contextfabric

// CHAOS-5465 pins. Every arm drives Engine.Investigate -- the public entry
// point -- through real receipt validation, real carry resolution, a real
// store and the real decision, because a test that calls the decision function
// directly proves the function decides and cannot fail when the engine never
// consults it. That is the plumbing defect this programme keeps rediscovering.
//
// THE ONE THING THAT MAKES THESE ARMS DISCRIMINATING: the interpreter is
// FORCED to disagree, in BOTH directions. Turn one and turn two of this corpus
// row resolve to either family across replicates -- measured, 18 of 36 archived
// pairs disagree on family and 18 on the requirement set -- so an arm that let
// the model pick could pass by the model happening to agree with itself. Every
// forced-conflict arm below therefore states which family each turn holds, and
// the direction is run twice, reversed.

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	continuationReceiptID = "winr_5465continuationaaaa"
	continuationPriorID   = "result_5465_turn_one_0001"
	continuationOlderID   = "result_5465_turn_zero_001"
)

// forcedFamilyInterpreter proposes a stated family with a stated group kind.
//
// It exists because interpreterFunc's adapter always reports
// unclassified/none, which is the ONE condition under which the pre-existing
// carry applied -- so every arm built on it would exercise the old path and
// none would exercise this one.
type forcedFamilyInterpreter struct {
	family    QuestionFamily
	groupKind SubjectKind
	err       error
}

func (f forcedFamilyInterpreter) Interpret(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error) {
	if f.err != nil {
		return InterpretedQuestion{}, QuestionFamilyOutcome{}, f.err
	}
	return InterpretedQuestion{
		Shape:             ShapeOpen,
		RequestedJudgment: "status",
		TimeContext:       TimeContext{Axis: TemporalCurrent},
	}, QuestionFamilyOutcome{
		Family:             f.family,
		Source:             QuestionFamilySourceModel,
		WinningSampleIndex: 0,
		WinningSample:      FamilySample{ModelFamily: f.family, GroupKind: f.groupKind},
		Version:            QuestionFamilyTableVersion,
	}, nil
}

// continuationPrior builds a turn one that classified `family` and offers a
// real window receipt for turn two to redeem.
func continuationPrior(resultID, question string, family QuestionFamily, groupKind SubjectKind) InvestigationResult {
	frozenStart := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	frozenEnd := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	prior := validInvestigationResult()
	prior.ResultID = resultID
	prior.Question = question
	prior.ConfirmedStructure = nil
	prior.AnswerPlan = &contractsv1.ContextFabricAnswerPlan{
		Family:        family,
		GroupKind:     groupKind,
		FamilyVersion: QuestionFamilyTableVersion,
		Budget:        contractsv1.ContextFabricAnswerPlanBudget{NarrowingBasis: contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical},
	}
	prior.WindowClarification = &WindowClarification{Options: []WindowOption{{
		ReceiptID:  continuationReceiptID,
		OptionID:   "opt_90d",
		Label:      "the last 90 days",
		RelativeID: RelativeWindowTrailing90D,
		Start:      &frozenStart,
		End:        &frozenEnd,
	}}}
	return prior
}

// continuationRequest is the window-only turn two: identical question bytes,
// exactly one window receipt, and no other prior-result reference. That is the
// archived shape -- 38 of 38 turn-two requests measured.
func continuationRequest(question string) InvestigationRequest {
	request := validInvestigationRequest()
	request.Question = question
	request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: continuationPriorID, ReceiptID: continuationReceiptID}}
	return request
}

type continuationHarness struct {
	engine    *Engine
	telemetry *recordingTelemetry
	store     *staticResultStore
}

func newContinuationHarness(t *testing.T, store *staticResultStore, interpreter QuestionInterpreter) continuationHarness {
	t.Helper()
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	telemetry := &recordingTelemetry{}
	fresh := validInvestigationResult()
	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: graphReaderStub{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
			bases:      provenCommitBases(project),
		},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return fresh, nil
		}),
		Interpreter: interpreter,
		Results:     store,
		Telemetry:   telemetry,
	})
	return continuationHarness{engine: engine, telemetry: telemetry, store: store}
}

func (h continuationHarness) investigate(t *testing.T, request InvestigationRequest) InvestigationResult {
	t.Helper()
	result, err := h.engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	return result
}

// soleDecision asserts EXACTLY ONE continuation event and returns it.
//
// Exactly one, never "at least one": a surplus would double-count every rate
// computed off this line, and a test that counts only what it expects cannot
// detect a surplus.
func (h continuationHarness) soleDecision(t *testing.T) windowContinuationDecision {
	t.Helper()
	if len(h.telemetry.windowContinuationDecisions) != 1 {
		t.Fatalf("got %d window-continuation decisions, want exactly 1 -- this is the axis's only denominator, so a surplus corrupts every rate computed from it; records: %#v",
			len(h.telemetry.windowContinuationDecisions), h.telemetry.windowContinuationDecisions)
	}
	return h.telemetry.windowContinuationDecisions[0]
}

// ---------------------------------------------------------------------------
// D-a / D-b: the carried context wins under FORCED disagreement, BOTH WAYS.
// Mutant: forced_family_conflict.
// ---------------------------------------------------------------------------

func TestWindowContinuation_ForcedFamilyConflictServesTheCarriedContext(t *testing.T) {
	t.Parallel()

	// BOTH DIRECTIONS. Run one way only and the arm could pass on a build that
	// simply prefers one family -- and this corpus row genuinely resolves to
	// either family on identical bytes, so that is not a hypothetical.
	for _, tc := range []struct {
		name          string
		carriedFamily QuestionFamily
		carriedGroup  SubjectKind
		freshFamily   QuestionFamily
		freshGroup    SubjectKind
	}{
		{
			name:          "discovered carried over fresh grouped",
			carriedFamily: QuestionFamilyDiscoveredCohortRanking,
			carriedGroup:  "",
			freshFamily:   QuestionFamilyGroupedCohortStatus,
			freshGroup:    contractsv1.ContextFabricSubjectTeam,
		},
		{
			name:          "grouped carried over fresh discovered",
			carriedFamily: QuestionFamilyGroupedCohortStatus,
			carriedGroup:  contractsv1.ContextFabricSubjectTeam,
			freshFamily:   QuestionFamilyDiscoveredCohortRanking,
			freshGroup:    "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			request := continuationRequest(validInvestigationRequest().Question)
			prior := continuationPrior(continuationPriorID, request.Question, tc.carriedFamily, tc.carriedGroup)
			harness := newContinuationHarness(t,
				&staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
				forcedFamilyInterpreter{family: tc.freshFamily, groupKind: tc.freshGroup})

			result := harness.investigate(t, request)
			if result.AnswerPlan == nil {
				t.Fatalf("no answer plan served")
			}
			if result.AnswerPlan.Family != tc.carriedFamily {
				t.Errorf("served family = %q, want the CARRIED %q -- a window-only confirmation continues the reading whose window offer it redeems", result.AnswerPlan.Family, tc.carriedFamily)
			}
			if result.AnswerPlan.FamilySource != QuestionFamilySourceCarried {
				t.Errorf("served family_source = %q, want %q", result.AnswerPlan.FamilySource, QuestionFamilySourceCarried)
			}
			if result.AnswerPlan.GroupKind != tc.carriedGroup {
				t.Errorf("served group_kind = %q, want the CARRIED %q -- carrying a family while keeping the fresh group axis is the same-family substitution this pin exists for", result.AnswerPlan.GroupKind, tc.carriedGroup)
			}

			decision := harness.soleDecision(t)
			if decision.Disposition != ContinuationApplied {
				t.Errorf("disposition = %q, want %q", decision.Disposition, ContinuationApplied)
			}
			if decision.Reason != ContinuationReasonNone {
				t.Errorf("decision_reason = %q, want %q", decision.Reason, ContinuationReasonNone)
			}
			// A DISAGREEMENT IS DISCLOSED, NOT REFUSED.
			if !decision.ComparisonEvaluated {
				t.Errorf("comparison_evaluated = false, want true: a fresh proposal existed and was compared")
			}
			if decision.Agreement {
				t.Errorf("agreement = true on a forced conflict")
			}
			if decision.ConflictReason != ContinuationConflictNonWindowContext {
				t.Errorf("conflict_reason = %q, want %q", decision.ConflictReason, ContinuationConflictNonWindowContext)
			}
			// EVERY differing component, not merely the first: family AND the
			// subject expression differ in both directions here.
			if got := decision.ConflictFieldTokens(); len(got) != 2 {
				t.Errorf("conflict_fields = %v, want both family and subject_expression -- reporting only the first difference hides a same-family substitution", got)
			}
			if decision.ConflictCount() != 2 {
				t.Errorf("conflict_count = %d, want 2", decision.ConflictCount())
			}
			if decision.FamilyCarried() != tc.carriedFamily || decision.FamilyFresh() != tc.freshFamily || decision.FamilyAccepted() != tc.carriedFamily {
				t.Errorf("family_carried/fresh/accepted = %q/%q/%q, want %q/%q/%q",
					decision.FamilyCarried(), decision.FamilyFresh(), decision.FamilyAccepted(),
					tc.carriedFamily, tc.freshFamily, tc.carriedFamily)
			}
			if decision.AcceptedContextID() != prior.ResultID {
				t.Errorf("accepted_context_id = %q, want the prior result %q", decision.AcceptedContextID(), prior.ResultID)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// D-a: SAME FAMILY, different subject expression. Mutant:
// same_family_subject_expression_conflict.
//
// Named for what this build can actually carry. The design's own arm is
// `same_family_role_requirement_conflict`; roles and requirement declarations
// are not persisted by any stored result today (measured: 0 of 38 archived
// turn-one artefacts carry a frame), so the same defect SHAPE is pinned on the
// component that is carried. A build that carried only the family token and
// kept the fresh group axis passes every family assertion above and fails here.
// ---------------------------------------------------------------------------

func TestWindowContinuation_SameFamilySubjectExpressionConflictStillServesTheCarriedContext(t *testing.T) {
	t.Parallel()

	request := continuationRequest(validInvestigationRequest().Question)
	prior := continuationPrior(continuationPriorID, request.Question, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
	harness := newContinuationHarness(t,
		&staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
		// SAME family, DIFFERENT group axis.
		forcedFamilyInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectProject})

	result := harness.investigate(t, request)
	if result.AnswerPlan.GroupKind != contractsv1.ContextFabricSubjectTeam {
		t.Errorf("served group_kind = %q, want the carried %q", result.AnswerPlan.GroupKind, contractsv1.ContextFabricSubjectTeam)
	}
	if result.AnswerPlan.FamilySource != QuestionFamilySourceCarried {
		t.Errorf("served family_source = %q, want %q", result.AnswerPlan.FamilySource, QuestionFamilySourceCarried)
	}

	decision := harness.soleDecision(t)
	if decision.ConflictReason != ContinuationConflictNonWindowContext {
		t.Errorf("conflict_reason = %q, want %q -- the families AGREE, so a family-only comparison would report agreement here", decision.ConflictReason, ContinuationConflictNonWindowContext)
	}
	tokens := decision.ConflictFieldTokens()
	if len(tokens) != 1 || tokens[0] != string(ContinuationConflictFieldSubjectExpression) {
		t.Errorf("conflict_fields = %v, want exactly [subject_expression]", tokens)
	}
	if decision.Agreement {
		t.Errorf("agreement = true while the subject expression differs")
	}
}

// ---------------------------------------------------------------------------
// D-b: an AGREEING fresh proposal is a separate control and must STILL report
// carried. Mutant: conflict_serves_carried_context (the agreement half).
// ---------------------------------------------------------------------------

func TestWindowContinuation_AnAgreeingProposalStillReportsCarried(t *testing.T) {
	t.Parallel()

	request := continuationRequest(validInvestigationRequest().Question)
	prior := continuationPrior(continuationPriorID, request.Question, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
	harness := newContinuationHarness(t,
		&staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
		forcedFamilyInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectTeam})

	result := harness.investigate(t, request)
	// THE PROVENANCE IS NOT AN OPINION ABOUT WHO AGREED. A build that reported
	// `model` whenever the two happened to agree would make the applied-carry
	// rate a function of sampler luck.
	if result.AnswerPlan.FamilySource != QuestionFamilySourceCarried {
		t.Errorf("served family_source = %q, want %q even on agreement", result.AnswerPlan.FamilySource, QuestionFamilySourceCarried)
	}

	decision := harness.soleDecision(t)
	if !decision.ComparisonEvaluated || !decision.Agreement {
		t.Errorf("comparison_evaluated/agreement = %v/%v, want true/true", decision.ComparisonEvaluated, decision.Agreement)
	}
	if decision.ConflictReason != ContinuationConflictNone || decision.ConflictCount() != 0 {
		t.Errorf("conflict_reason/count = %q/%d, want %q/0 explicitly", decision.ConflictReason, decision.ConflictCount(), ContinuationConflictNone)
	}
}

// ---------------------------------------------------------------------------
// D-a: the RECORDED standard governs. Mutant: context_version_mismatch.
// ---------------------------------------------------------------------------

func TestWindowContinuation_AVersionMismatchedCarrierIsWithheldNotReinterpreted(t *testing.T) {
	t.Parallel()

	request := continuationRequest(validInvestigationRequest().Question)
	prior := continuationPrior(continuationPriorID, request.Question, QuestionFamilyDiscoveredCohortRanking, "")
	prior.AnswerPlan.FamilyVersion = "question-family.v0-not-in-force"
	harness := newContinuationHarness(t,
		&staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
		forcedFamilyInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectTeam})

	result := harness.investigate(t, request)
	// NOT reinterpreted under today's tables, and not silently carried either.
	if result.AnswerPlan.FamilySource == QuestionFamilySourceCarried {
		t.Errorf("served family_source = carried on a carrier stamped by a version not in force")
	}

	decision := harness.soleDecision(t)
	if decision.Disposition != ContinuationWithheld {
		t.Errorf("disposition = %q, want %q", decision.Disposition, ContinuationWithheld)
	}
	if decision.Reason != ContinuationReasonContextVersionMismatch {
		t.Errorf("decision_reason = %q, want %q", decision.Reason, ContinuationReasonContextVersionMismatch)
	}
	if decision.ComparisonEvaluated || decision.Agreement {
		t.Errorf("comparison_evaluated/agreement = %v/%v on a withheld carrier, want false/false -- an unevaluated comparison is neither agreement nor disagreement",
			decision.ComparisonEvaluated, decision.Agreement)
	}
	if decision.ConflictReason != ContinuationConflictNone || decision.ConflictCount() != 0 {
		t.Errorf("conflict_reason/count = %q/%d, want explicit none/0", decision.ConflictReason, decision.ConflictCount())
	}
}

// ---------------------------------------------------------------------------
// D-d: CONTAINMENT. Mutants: changed_question_no_context_carry,
// indeterminate_identity, epoch_taint, immediate_carrier_only.
// ---------------------------------------------------------------------------

func TestWindowContinuation_ContainmentRefusesEverythingThatIsNotTheTransition(t *testing.T) {
	t.Parallel()

	baseQuestion := validInvestigationRequest().Question

	for _, tc := range []struct {
		name string
		// mutate shapes the request; prior shapes turn one; store lets an arm
		// change epoch behaviour.
		mutate          func(*InvestigationRequest)
		prior           func(InvestigationResult) InvestigationResult
		storeEpoch      *int64
		wantDisposition ContinuationDisposition
		wantReason      ContinuationDecisionReason
		// wantLegacyCarryBlocked asserts the OLD family-only carry is refused
		// too, which is the escape the design names explicitly.
		wantLegacyCarryBlocked bool
	}{
		{
			name:  "a changed question cannot import the prior context",
			prior: func(p InvestigationResult) InvestigationResult { p.Question = driftQuestion; return p },
			// NOT withheld: the transition was never established, so there was
			// no continuation to withhold.
			wantDisposition:        ContinuationNotApplicable,
			wantReason:             ContinuationReasonChangedQuestion,
			wantLegacyCarryBlocked: true,
		},
		{
			name:   "an indeterminate identity is not drift and is not carried",
			mutate: func(r *InvestigationRequest) { r.Question = "?" },
			prior:  func(p InvestigationResult) InvestigationResult { p.Question = "!!"; return p },
			// Not "the questions differ" -- there is no identity to compare.
			// Folding this into drift would put a false basis in the data,
			// which is the mistake carryOriginVerdict's three-state rule
			// exists to avoid.
			wantDisposition:        ContinuationNotApplicable,
			wantReason:             ContinuationReasonIndeterminateIdentity,
			wantLegacyCarryBlocked: true,
		},
		{
			name:            "a carrier from another graph epoch is withheld",
			storeEpoch:      func() *int64 { e := int64(97); return &e }(),
			wantDisposition: ContinuationWithheld,
			wantReason:      ContinuationReasonInvalidContext,
		},
		{
			name: "a window receipt riding with a typed selection is not a continuation",
			mutate: func(r *InvestigationRequest) {
				r.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: continuationPriorID, ReceiptID: "kindr_5465aaaaaaaaaaaa"}}
			},
			wantDisposition: ContinuationNotApplicable,
			wantReason:      ContinuationReasonNotWindowOnly,
		},
		{
			// FOUND BY THE MUTANT BATTERY, not by review: deleting the
			// parent_result_id exclusion SURVIVED the first table, because
			// every arm that named a parent had removed the window receipt,
			// so the exclusion was never reached. A request carrying BOTH is
			// the shape that makes it load-bearing -- the caller named a
			// parent AND redeemed a window offer, which is not the turn that
			// changed only the window.
			name: "a window receipt riding with a parent_result_id is not a continuation",
			mutate: func(r *InvestigationRequest) {
				r.ParentResultID = continuationOlderID
			},
			wantDisposition: ContinuationNotApplicable,
			wantReason:      ContinuationReasonNotWindowOnly,
		},
		{
			name: "a parent-only reference cannot establish the transition",
			mutate: func(r *InvestigationRequest) {
				r.PriorWindowReceipts = nil
				r.ParentResultID = continuationPriorID
			},
			// No window receipt at all, so no event: the denominator is
			// requests carrying window receipts.
			wantDisposition: ContinuationNotApplicable,
			wantReason:      ContinuationReasonNotWindowOnly,
		},
		{
			// D-e, MEASURED rather than assumed. Plural window receipts are
			// CHAOS-5271's veto_conflict and the veto fires FIRST, before this
			// decision runs at all -- so the honest reading is `window_veto`,
			// not `not_window_only`. Pinned in that shape deliberately: it is
			// the evidence that this change did not suppress a window veto to
			// make a continuation succeed, which is exactly what D-e forbids.
			name: "plural window receipts are vetoed before the continuation decision",
			mutate: func(r *InvestigationRequest) {
				r.PriorWindowReceipts = append(r.PriorWindowReceipts,
					BoundSubjectReceipt{ResultID: continuationOlderID, ReceiptID: "winr_5465secondaaaaaaaa"})
			},
			wantDisposition: ContinuationNotApplicable,
			wantReason:      ContinuationReasonWindowVeto,
		},
		{
			// Also measured, and also not what it looks like: an unresolvable
			// window receipt never reaches the continuation gate, because the
			// EXISTING window rules veto it first
			// (windowVetoConfirmationUnresolved). The `invalid_context`
			// withholding is reached by a carrier that resolves for the WINDOW
			// and then fails this decision's own admission -- the epoch arm
			// above. Pinned so a later change that moved the continuation gate
			// ahead of window validation would fail here rather than silently
			// re-order two vetoes.
			name: "an unresolvable window receipt is vetoed before the continuation decision",
			mutate: func(r *InvestigationRequest) {
				r.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: "result_5465_absent_00001", ReceiptID: continuationReceiptID}}
			},
			wantDisposition: ContinuationNotApplicable,
			wantReason:      ContinuationReasonWindowVeto,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			request := continuationRequest(baseQuestion)
			prior := continuationPrior(continuationPriorID, baseQuestion, QuestionFamilyDiscoveredCohortRanking, "")
			// An OLDER ancestor that WOULD be usable. immediate_carrier_only:
			// a rescue from it must never happen -- the axis is one hop, and
			// the hop is the result the caller named.
			older := continuationPrior(continuationOlderID, baseQuestion, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
			if tc.prior != nil {
				prior = tc.prior(prior)
			}
			if tc.mutate != nil {
				tc.mutate(&request)
			}
			store := &staticResultStore{
				results:    map[string]InvestigationResult{prior.ResultID: prior, older.ResultID: older},
				graphEpoch: tc.storeEpoch,
			}
			harness := newContinuationHarness(t, store,
				// This turn classifies NOTHING, so the OLD family-only carry
				// would apply if the containment let it. That is what makes
				// wantLegacyCarryBlocked a real assertion rather than a
				// restatement of the disposition.
				interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
					return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
				}))

			result := harness.investigate(t, request)

			if requestCarriesWindowReceipts(request) {
				decision := harness.soleDecision(t)
				if decision.Disposition != tc.wantDisposition {
					t.Errorf("disposition = %q, want %q", decision.Disposition, tc.wantDisposition)
				}
				if decision.Reason != tc.wantReason {
					t.Errorf("decision_reason = %q, want %q", decision.Reason, tc.wantReason)
				}
				if decision.Disposition == ContinuationApplied {
					t.Errorf("a containment arm applied a continuation")
				}
			} else if len(harness.telemetry.windowContinuationDecisions) != 0 {
				t.Errorf("emitted %d continuation decisions for a request carrying no window receipt -- the denominator is requests that carry one",
					len(harness.telemetry.windowContinuationDecisions))
			}

			if tc.wantLegacyCarryBlocked {
				if result.AnswerPlan != nil && result.AnswerPlan.FamilySource == QuestionFamilySourceCarried {
					t.Errorf("the OLD family-only carry applied a context the continuation gate refused -- this is the fall-through the design names: a window receipt whose referenced turn answered a different question must not import that reading by another route")
				}
				if len(harness.telemetry.planCarries) != 0 {
					t.Errorf("got %d applied-carry emits, want 0 on a refused containment arm", len(harness.telemetry.planCarries))
				}
				// The refusal is OBSERVABLE on the axis's own denominator, not
				// only inside the new event.
				if len(harness.telemetry.planCarryOutcomes) != 1 {
					t.Fatalf("got %d plan-carry outcome records, want exactly 1", len(harness.telemetry.planCarryOutcomes))
				}
				switch got := harness.telemetry.planCarryOutcomes[0].outcome; got {
				case PlanCarryMissQuestionDrift, PlanCarryMissQuestionIndeterminate:
				default:
					t.Errorf("plan-carry outcome = %q, want a question-identity miss so the containment publishes a reason", got)
				}
			}
			// IMMEDIATE CARRIER ONLY, asserted on every arm: no arm may be
			// rescued by the usable older ancestor in the store.
			if result.AnswerPlan != nil && result.AnswerPlan.FamilySource == QuestionFamilySourceCarried &&
				harness.telemetry.windowContinuationDecisions != nil &&
				len(harness.telemetry.windowContinuationDecisions) == 1 &&
				harness.telemetry.windowContinuationDecisions[0].AcceptedContextID() == continuationOlderID {
				t.Errorf("an older ancestor rescued an unusable immediate carrier -- the plan axis is one hop by design")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// D-c: THE EVENT CENSUS. Mutant: decision_sink_census.
//
// One event per window-receipt request, on EVERY path -- including the ones
// that end before the decision runs. A per-site emit would pass a happy-path
// test and silently drop the veto path, which is exactly the shape this
// asserts against.
// ---------------------------------------------------------------------------

func TestWindowContinuation_EveryWindowReceiptRequestEmitsExactlyOneDecision(t *testing.T) {
	t.Parallel()

	baseQuestion := validInvestigationRequest().Question

	for _, tc := range []struct {
		name       string
		mutate     func(*InvestigationRequest)
		wantReason ContinuationDecisionReason
	}{
		{
			name:       "applied",
			wantReason: ContinuationReasonNone,
		},
		{
			// A window veto ends the turn BEFORE interpretation. The event
			// still fires, and it says which mechanism ended the turn -- so
			// CHAOS-5271's veto and this decision stay separable in the data.
			name: "ended by a window veto",
			mutate: func(r *InvestigationRequest) {
				r.PriorWindowReceipts = []BoundSubjectReceipt{
					{ResultID: continuationPriorID, ReceiptID: continuationReceiptID},
					{ResultID: continuationOlderID, ReceiptID: "winr_5465secondaaaaaaaa"},
				}
			},
			wantReason: ContinuationReasonWindowVeto,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			request := continuationRequest(baseQuestion)
			if tc.mutate != nil {
				tc.mutate(&request)
			}
			prior := continuationPrior(continuationPriorID, baseQuestion, QuestionFamilyDiscoveredCohortRanking, "")
			older := continuationPrior(continuationOlderID, baseQuestion, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
			harness := newContinuationHarness(t,
				&staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior, older.ResultID: older}},
				forcedFamilyInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectTeam})

			// A veto path returns a terminal result rather than an error, so
			// both arms go through the same call.
			if _, err := harness.engine.Investigate(context.Background(), acceptancePrincipal(), request); err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			decision := harness.soleDecision(t)
			if decision.Reason != tc.wantReason {
				t.Errorf("decision_reason = %q, want %q", decision.Reason, tc.wantReason)
			}
			// EXPLICIT ZEROS on every path: an omitted key and a measured zero
			// are indistinguishable downstream.
			if decision.ConflictReason == "" {
				t.Errorf("conflict_reason is empty, want the explicit %q", ContinuationConflictNone)
			}
			if decision.Reason == "" {
				t.Errorf("decision_reason is empty; it is always present by contract")
			}
			if decision.Disposition == "" {
				t.Errorf("continuation_disposition is empty; it is always present by contract")
			}
			if decision.Reason == ContinuationReasonUnspecified {
				t.Errorf("decision_reason = unspecified on a path this pin names -- unspecified means a decision site recorded nothing, which is a defect, not a state")
			}
		})
	}
}

// A request carrying NO window receipt emits NOTHING. Without this the census
// above would pass on a build that emitted on every request, and the rate
// would be computed over the wrong denominator.
func TestWindowContinuation_NoWindowReceiptEmitsNoDecision(t *testing.T) {
	t.Parallel()

	request := validInvestigationRequest()
	harness := newContinuationHarness(t, &staticResultStore{results: map[string]InvestigationResult{}},
		forcedFamilyInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectTeam})
	if _, err := harness.engine.Investigate(context.Background(), acceptancePrincipal(), request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if got := len(harness.telemetry.windowContinuationDecisions); got != 0 {
		t.Errorf("got %d continuation decisions on a request with no window receipt, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// D-c: THE LOGGED SELECTION IS THE EXECUTED ONE.
// Mutant: decision_served_document_join.
//
// A build that logged `carried` while passing the fresh family downstream
// passes every event assertion above. This joins the two: the served
// document's own plan must equal the decision's accepted context.
// ---------------------------------------------------------------------------

func TestWindowContinuation_TheDecisionJoinsToTheServedDocument(t *testing.T) {
	t.Parallel()

	request := continuationRequest(validInvestigationRequest().Question)
	prior := continuationPrior(continuationPriorID, request.Question, QuestionFamilyDiscoveredCohortRanking, "")
	harness := newContinuationHarness(t,
		&staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
		forcedFamilyInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectTeam})

	result := harness.investigate(t, request)
	decision := harness.soleDecision(t)

	if result.AnswerPlan == nil {
		t.Fatalf("no answer plan served")
	}
	if QuestionFamily(result.AnswerPlan.Family) != decision.FamilyAccepted() {
		t.Errorf("served family %q != decision family_accepted %q -- the decision was logged and a different value was executed",
			result.AnswerPlan.Family, decision.FamilyAccepted())
	}
	if result.AnswerPlan.GroupKind != decision.Accepted.GroupKind {
		t.Errorf("served group_kind %q != accepted group_kind %q", result.AnswerPlan.GroupKind, decision.Accepted.GroupKind)
	}
	if string(result.AnswerPlan.FamilySource) != string(decision.AcceptedFamilySource()) {
		t.Errorf("served family_source %q != decision family_source %q", result.AnswerPlan.FamilySource, decision.AcceptedFamilySource())
	}
	// The narrowing basis the earlier turn declared reaches the plan, so two
	// turns of one conversation narrow the same way.
	if result.AnswerPlan.Budget.NarrowingBasis != contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical {
		t.Errorf("served narrowing_basis = %q, want the carried %q",
			result.AnswerPlan.Budget.NarrowingBasis, contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical)
	}
	// The applied-carry counter and the served provenance agree. A served
	// `carried` with a zero applied-carry count is the numerator/denominator
	// split in the other direction.
	if len(harness.telemetry.planCarries) != 1 {
		t.Errorf("got %d applied-carry emits beside a served family_source=carried, want exactly 1", len(harness.telemetry.planCarries))
	}
}

// ---------------------------------------------------------------------------
// D-b: LIVE AUTHORIZATION AND MEMBERSHIP ARE NOT CARRIED.
// Mutant: authorization_changed_same_epoch.
// ---------------------------------------------------------------------------

func TestWindowContinuation_CarriesNoMembershipAndNoAuthorizationVerdict(t *testing.T) {
	t.Parallel()

	request := continuationRequest(validInvestigationRequest().Question)
	prior := continuationPrior(continuationPriorID, request.Question, QuestionFamilyDiscoveredCohortRanking, "")
	// Turn one committed a subject. The continuation must not inherit it.
	prior.SubjectResolution = SubjectResolution{
		Candidates: []SubjectCandidate{},
		Committed:  []SubjectRef{{Kind: SubjectTeam, CanonicalID: "team_no_longer_authorized", Label: "gone"}},
	}
	harness := newContinuationHarness(t,
		&staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
		forcedFamilyInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectTeam})

	result := harness.investigate(t, request)
	decision := harness.soleDecision(t)

	if decision.Disposition != ContinuationApplied {
		t.Fatalf("fixture defect: wanted an applied continuation, got %q/%q", decision.Disposition, decision.Reason)
	}
	// The CARRIED context is a reading, not a population. North Star check 18:
	// carrying members would carry an authorization decision.
	if decision.Accepted == nil {
		t.Fatalf("no accepted context")
	}
	for _, committed := range result.SubjectResolution.Committed {
		if committed.CanonicalID == "team_no_longer_authorized" {
			t.Errorf("the continuation re-served a subject committed by the PRIOR turn -- membership and authorization re-resolve live every turn, and the graph in this fixture never returned it")
		}
	}
}

// ---------------------------------------------------------------------------
// THE VOCABULARY PINS.
//
// A closed vocabulary whose members are never asserted is a vocabulary a
// mutation can rename. These also pin what this BUILD can compare, so the
// component set cannot silently widen or narrow.
// ---------------------------------------------------------------------------

func TestWindowContinuation_ComparableFieldsAreExactlyTheCarriedComponents(t *testing.T) {
	t.Parallel()

	got := continuationComparableFields()
	want := []ContinuationConflictField{ContinuationConflictFieldFamily, ContinuationConflictFieldSubjectExpression}
	if len(got) != len(want) {
		t.Fatalf("comparable fields = %v, want %v -- this list is what `agreement` is a statement ABOUT; widening it without carrying the component would claim agreement about something nothing carried, and narrowing it would hide a real substitution", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("comparable fields = %v, want %v (order is part of the contract: conflict_fields must be deterministic)", got, want)
		}
	}
	// The vocabulary is declared WHOLE even though this build populates two of
	// its members. Pinned so the members a later slice fills in are already
	// named rather than invented twice.
	for _, member := range []ContinuationConflictField{
		ContinuationConflictFieldRoles, ContinuationConflictFieldRequirements,
		ContinuationConflictFieldObligations, ContinuationConflictFieldFrameGate,
	} {
		if string(member) == "" {
			t.Errorf("declared conflict-field member is empty")
		}
	}
}

// ---------------------------------------------------------------------------
// THE REAL SINK. A recording double proves the engine called SOMETHING; it can
// never prove the shipped sink emits anything usable, drops a value, renames a
// key or filters the line by level.
// ---------------------------------------------------------------------------

func TestWindowContinuation_TheDecisionReachesTheRealSink(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	request := continuationRequest(validInvestigationRequest().Question)
	prior := continuationPrior(continuationPriorID, request.Question, QuestionFamilyDiscoveredCohortRanking, "")
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	fresh := validInvestigationResult()

	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: graphReaderStub{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
			bases:      provenCommitBases(project),
		},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return fresh, nil
		}),
		Interpreter: forcedFamilyInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectTeam},
		Results:     &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
		Telemetry:   SlogEngineTelemetry{logger: logger},
	})
	if _, err := engine.Investigate(context.Background(), acceptancePrincipal(), request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	var line string
	for _, candidate := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if strings.Contains(candidate, "context fabric window continuation decision") {
			line = candidate
			break
		}
	}
	if line == "" {
		t.Fatalf("the shipped sink emitted no continuation-decision line at Info; output:\n%s", buf.String())
	}
	// KEYS ARE ASSERTED, not just the message: a renamed key silently breaks
	// every downstream query while the line still appears.
	for _, key := range []string{
		`"org_id"`, `"source_result_id"`, `"seed_source"`,
		`"family_carried"`, `"family_fresh"`, `"family_accepted"`, `"family_source"`,
		`"continuation_disposition"`, `"decision_reason"`,
		`"comparison_evaluated"`, `"agreement"`,
		`"conflict_reason"`, `"conflict_count"`, `"conflict_fields"`,
		`"applied_window"`,
		`"carried_context_id"`, `"fresh_context_id"`, `"accepted_context_id"`,
	} {
		if !strings.Contains(line, key) {
			t.Errorf("emitted line is missing key %s -- a field populated on the decision and never logged is not telemetry, it is a field.\nline: %s", key, line)
		}
	}
	if !strings.Contains(line, `"continuation_disposition":"applied"`) {
		t.Errorf("disposition did not reach the sink; line: %s", line)
	}
	if !strings.Contains(line, `"conflict_reason":"non_window_context_conflict"`) {
		t.Errorf("the conflict token did not reach the sink; line: %s", line)
	}
	// CONTENT SAFETY: ids, closed values and equality results only.
	if strings.Contains(line, request.Question) {
		t.Errorf("the emitted line contains the question text; this line carries ids and closed vocabularies only")
	}
}

// ---------------------------------------------------------------------------
// REGRESSION PINS FROM THE COUNTED r1 REVIEW.
//
// Four P1s, each reported with an executed repro, each REPRODUCED HERE against
// the unfixed tree before anything was changed, and each kept as the regression
// test for its own fix. The reviewer's own arms were self-cleaned with its
// worktree, so these reconstruct the SHAPE rather than a similar one.
//
// What every one of them has in common is worth stating once: the lane's
// original fixtures could not see any of these, and in three of the four cases
// the reason was that the DOUBLE was too weak -- an interpreter returning a nil
// frame, an interpreter that classified when the hole needed one that did not,
// a graph reader that always bound. A test double that cannot express the
// failing state is a test that measures nothing about it.
// ---------------------------------------------------------------------------

// frameBearingInterpreter is forcedFamilyInterpreter plus a real validated
// FRAME carrying a group axis -- which the lane's own fixtures never had, and
// which is the whole of R1-1.
type frameBearingInterpreter struct {
	family     QuestionFamily
	groupKind  SubjectKind
	frameGroup SubjectKind
}

func (f frameBearingInterpreter) Interpret(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error) {
	frame := &QuestionFrame{
		SubjectExpression: SubjectExpression{
			Kind:    SubjectExpressionGroupedMembers,
			Grouped: &GroupedSetExpression{GroupKind: f.frameGroup},
		},
	}
	return InterpretedQuestion{
		Shape: ShapeOpen, RequestedJudgment: "status",
		TimeContext: TimeContext{Axis: TemporalCurrent},
	}, QuestionFamilyOutcome{
		Family:             f.family,
		Source:             QuestionFamilySourceModel,
		Frame:              frame,
		WinningSampleIndex: 0,
		WinningSample:      FamilySample{ModelFamily: f.family, GroupKind: f.groupKind},
		Version:            QuestionFamilyTableVersion,
	}, nil
}

// R1-1: the executed plan takes its group axis from the FRESH frame, so what is
// served differs from the logged accepted context.
func TestWindowContinuation_R1_TheServedGroupAxisIsTheAcceptedOneNotTheFreshFrames(t *testing.T) {
	request := continuationRequest(validInvestigationRequest().Question)
	prior := continuationPrior(continuationPriorID, request.Question, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
	harness := newContinuationHarness(t,
		&staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
		frameBearingInterpreter{
			family:     QuestionFamilyGroupedCohortStatus,
			groupKind:  contractsv1.ContextFabricSubjectProject,
			frameGroup: contractsv1.ContextFabricSubjectProject,
		})

	result := harness.investigate(t, request)
	decision := harness.soleDecision(t)
	t.Logf("R1-1: decision=%q accepted_group_kind=%q served_group_kind=%q",
		decision.Disposition, decision.Accepted.GroupKind, result.AnswerPlan.GroupKind)
	if result.AnswerPlan.GroupKind != decision.Accepted.GroupKind {
		t.Fatalf("R1-1 REGRESSION: served group_kind=%q but the logged accepted context says %q -- the decision was logged and a different value executed",
			result.AnswerPlan.GroupKind, decision.Accepted.GroupKind)
	}
}

// R1-2: a withheld continuation still lets the OLD family-only carry serve the
// stale reading, because the containment covers only the two identity reasons.
func TestWindowContinuation_R1_AWithheldCarrierCannotBeServedByTheLegacyCarry(t *testing.T) {
	request := continuationRequest(validInvestigationRequest().Question)
	prior := continuationPrior(continuationPriorID, request.Question, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
	prior.AnswerPlan.FamilyVersion = "question-family.v0-not-in-force"
	harness := newContinuationHarness(t,
		&staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
		// THIS TURN CLASSIFIES NOTHING -- the one condition under which the old
		// carry applies. The lane's own version-mismatch pin used a CLASSIFYING
		// interpreter, so applyCarriedPlan refused for an unrelated reason and
		// the hole stayed invisible.
		interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}))

	result := harness.investigate(t, request)
	decision := harness.soleDecision(t)
	outcome := ""
	if len(harness.telemetry.planCarryOutcomes) > 0 {
		outcome = string(harness.telemetry.planCarryOutcomes[0].outcome)
	}
	t.Logf("R1-2: decision=%q/%q plan_carry_outcome=%q served_family=%q served_family_source=%q",
		decision.Disposition, decision.Reason, outcome, result.AnswerPlan.Family, result.AnswerPlan.FamilySource)
	if result.AnswerPlan.FamilySource == QuestionFamilySourceCarried {
		t.Fatalf("R1-2 REGRESSION: the continuation was %q for %q, yet the legacy carry served family=%q with family_source=carried from the SAME refused carrier",
			decision.Disposition, decision.Reason, result.AnswerPlan.Family)
	}
}

// R1-3: a window-receipt request that fails graph binding emits no decision
// line, so the event's denominator is not "requests carrying window receipts".
func TestWindowContinuation_R1_AWindowReceiptRequestEmitsADecisionEvenWhenGraphBindingFails(t *testing.T) {
	request := continuationRequest(validInvestigationRequest().Question)
	prior := continuationPrior(continuationPriorID, request.Question, QuestionFamilyDiscoveredCohortRanking, "")
	telemetry := &recordingTelemetry{}
	fresh := validInvestigationResult()
	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: bindingFailingGraphReader{err: errBindingUnavailableForRepro},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return fresh, nil
		}),
		Interpreter: forcedFamilyInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectTeam},
		Results:     &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
		Telemetry:   telemetry,
	})
	if _, err := engine.Investigate(context.Background(), acceptancePrincipal(), request); err == nil {
		t.Fatalf("fixture defect: wanted a graph-binding failure")
	}
	t.Logf("R1-3: window_decisions=%d on a request that carries a window receipt", len(telemetry.windowContinuationDecisions))
	if len(telemetry.windowContinuationDecisions) != 1 {
		t.Fatalf("R1-3 REGRESSION: %d decision lines for a window-receipt request, want exactly 1 -- a missing line is supposed to mean only 'no window receipt'",
			len(telemetry.windowContinuationDecisions))
	}
}

// R1-4: applied_window is declared, logged, and never populated.
func TestWindowContinuation_R1_AnAppliedContinuationLogsTheWindowItApplied(t *testing.T) {
	request := continuationRequest(validInvestigationRequest().Question)
	prior := continuationPrior(continuationPriorID, request.Question, QuestionFamilyDiscoveredCohortRanking, "")
	harness := newContinuationHarness(t,
		&staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
		forcedFamilyInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectTeam})

	result := harness.investigate(t, request)
	decision := harness.soleDecision(t)
	t.Logf("R1-4: disposition=%q applied_window_token=%q served_effective_window=%v",
		decision.Disposition, decision.AppliedWindowToken(), result.EffectiveEvidenceWindow != nil)
	if decision.Disposition == ContinuationApplied && decision.AppliedWindowToken() == "" {
		t.Fatalf("R1-4 REGRESSION: an APPLIED continuation logs applied_window=\"\" -- the field is declared and logged but never populated, so the line cannot say which window was actually applied")
	}
}

var errBindingUnavailableForRepro = &bindingReproError{}

type bindingReproError struct{}

func (*bindingReproError) Error() string { return "graph binding unavailable (repro)" }
