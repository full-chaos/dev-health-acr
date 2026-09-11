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
			prior := continuationPrior(t, continuationPriorID, request.Question, tc.carriedFamily, tc.carriedGroup)
			harness := newContinuationHarness(t,
				&staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
				forcedFamilyInterpreter{family: tc.freshFamily, groupKind: tc.freshGroup})

			result := harness.investigate(t, request)
			if result.AnswerPlan == nil {
				t.Fatalf("no answer plan served")
			}
			if servedPlanFamily(result) != tc.carriedFamily {
				t.Errorf("served family = %q, want the CARRIED %q -- a window-only confirmation continues the reading whose window offer it redeems", servedPlanFamily(result), tc.carriedFamily)
			}
			if servedPlanSource(result) != QuestionFamilySourceCarried {
				t.Errorf("served family_source = %q, want %q", servedPlanSource(result), QuestionFamilySourceCarried)
			}
			if servedPlanGroup(result) != tc.carriedGroup {
				t.Errorf("served group_kind = %q, want the CARRIED %q -- carrying a family while keeping the fresh group axis is the same-family substitution this pin exists for", servedPlanGroup(result), tc.carriedGroup)
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
	prior := continuationPrior(t, continuationPriorID, request.Question, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
	harness := newContinuationHarness(t,
		&staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
		// SAME family, DIFFERENT group axis.
		forcedFamilyInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectProject})

	result := harness.investigate(t, request)
	if servedPlanGroup(result) != contractsv1.ContextFabricSubjectTeam {
		t.Errorf("served group_kind = %q, want the carried %q", servedPlanGroup(result), contractsv1.ContextFabricSubjectTeam)
	}
	if servedPlanSource(result) != QuestionFamilySourceCarried {
		t.Errorf("served family_source = %q, want %q", servedPlanSource(result), QuestionFamilySourceCarried)
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
	prior := continuationPrior(t, continuationPriorID, request.Question, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
	harness := newContinuationHarness(t,
		&staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
		forcedFamilyInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectTeam})

	result := harness.investigate(t, request)
	// THE PROVENANCE IS NOT AN OPINION ABOUT WHO AGREED. A build that reported
	// `model` whenever the two happened to agree would make the applied-carry
	// rate a function of sampler luck.
	if servedPlanSource(result) != QuestionFamilySourceCarried {
		t.Errorf("served family_source = %q, want %q even on agreement", servedPlanSource(result), QuestionFamilySourceCarried)
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
	prior := continuationPrior(t, continuationPriorID, request.Question, QuestionFamilyDiscoveredCohortRanking, "")
	prior.AnswerPlan.FamilyVersion = "question-family.v0-not-in-force"
	harness := newContinuationHarness(t,
		&staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
		forcedFamilyInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectTeam})

	result := harness.investigate(t, request)
	// NOT reinterpreted under today's tables, and not silently carried either.
	if result.AnswerPlan != nil && servedPlanSource(result) == QuestionFamilySourceCarried {
		t.Errorf("served family_source = carried on a carrier stamped by a version not in force")
	}
	// And not answered under the fresh reading: a carrier that cannot be
	// established refuses the turn.
	assertContinuationRefused(t, result)

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
			// MEASURED, not assumed. An unresolvable typed receipt is vetoed by
			// structure canonicalisation BEFORE admission runs, so the honest
			// reason is `structure_veto`. Pinned in that shape because it is the
			// evidence that admission did not run early and pre-empt an existing
			// veto -- the r2 ruling's whole point. The receipt-field branch of
			// the shape check has its own arm below, on a receipt that resolves.
			name: "a window receipt riding with an unresolvable typed receipt is vetoed before admission",
			mutate: func(r *InvestigationRequest) {
				r.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: continuationPriorID, ReceiptID: "kindr_5465aaaaaaaaaaaa"}}
			},
			wantDisposition: ContinuationNotApplicable,
			wantReason:      ContinuationReasonStructureVeto,
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
			prior := continuationPrior(t, continuationPriorID, baseQuestion, QuestionFamilyDiscoveredCohortRanking, "")
			// An OLDER ancestor that WOULD be usable. immediate_carrier_only:
			// a rescue from it must never happen -- the axis is one hop, and
			// the hop is the result the caller named.
			older := continuationPrior(t, continuationOlderID, baseQuestion, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
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
				if result.AnswerPlan != nil && servedPlanSource(result) == QuestionFamilySourceCarried {
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
			if result.AnswerPlan != nil && servedPlanSource(result) == QuestionFamilySourceCarried &&
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
			prior := continuationPrior(t, continuationPriorID, baseQuestion, QuestionFamilyDiscoveredCohortRanking, "")
			older := continuationPrior(t, continuationOlderID, baseQuestion, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
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
	prior := continuationPrior(t, continuationPriorID, request.Question, QuestionFamilyDiscoveredCohortRanking, "")
	harness := newContinuationHarness(t,
		&staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
		forcedFamilyInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectTeam})

	result := harness.investigate(t, request)
	decision := harness.soleDecision(t)

	if result.AnswerPlan == nil {
		t.Fatalf("no answer plan served")
	}
	if QuestionFamily(servedPlanFamily(result)) != decision.FamilyAccepted() {
		t.Errorf("served family %q != decision family_accepted %q -- the decision was logged and a different value was executed",
			servedPlanFamily(result), decision.FamilyAccepted())
	}
	if servedPlanGroup(result) != decision.AcceptedGroupKind() {
		t.Errorf("served group_kind %q != accepted group_kind %q", servedPlanGroup(result), decision.AcceptedGroupKind())
	}
	if string(servedPlanSource(result)) != string(decision.AcceptedFamilySource()) {
		t.Errorf("served family_source %q != decision family_source %q", servedPlanSource(result), decision.AcceptedFamilySource())
	}
	// The narrowing basis the earlier turn declared reaches the plan, so two
	// turns of one conversation narrow the same way.
	if result.AnswerPlan == nil {
		t.Fatalf("the served document has no plan, so the carried narrowing basis cannot be read")
	}
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
	prior := continuationPrior(t, continuationPriorID, request.Question, QuestionFamilyDiscoveredCohortRanking, "")
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
	prior := continuationPrior(t, continuationPriorID, request.Question, QuestionFamilyDiscoveredCohortRanking, "")
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
	// A FRAME THAT ACTUALLY VALIDATES. The earlier version of this fixture
	// declared a group kind and nothing else, which no phase of frame
	// validation accepts -- it only ever reached the served plan because the
	// old build substituted into frames without revalidating them. Composition
	// now refuses an invalid composition, so a pin about the SERVED axis has
	// to be driven by a frame the server would really serve.
	frame := &QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{
			Kind:    SubjectExpressionGroupedMembers,
			Grouped: &GroupedSetExpression{GroupKind: f.frameGroup, MemberKind: SubjectRepository},
		},
		Temporal: TemporalIntentCurrent,
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
	prior := continuationPrior(t, continuationPriorID, request.Question, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
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
		decision.Disposition, decision.AcceptedGroupKind(), servedPlanGroup(result))
	if servedPlanGroup(result) != decision.AcceptedGroupKind() {
		t.Fatalf("R1-1 REGRESSION: served group_kind=%q but the logged accepted context says %q -- the decision was logged and a different value executed",
			servedPlanGroup(result), decision.AcceptedGroupKind())
	}
}

// R1-2: a withheld continuation still lets the OLD family-only carry serve the
// stale reading, because the containment covers only the two identity reasons.
func TestWindowContinuation_R1_AWithheldCarrierCannotBeServedByTheLegacyCarry(t *testing.T) {
	request := continuationRequest(validInvestigationRequest().Question)
	prior := continuationPrior(t, continuationPriorID, request.Question, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
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
	family, source, _ := servedPlanAxes(result)
	t.Logf("R1-2: decision=%q/%q plan_carry_outcome=%q served_family=%q served_family_source=%q refusal_basis=%q",
		decision.Disposition, decision.Reason, outcome, family, source, result.RefusalBasis)
	if source == QuestionFamilySourceCarried {
		t.Fatalf("R1-2 REGRESSION: the continuation was %q for %q, yet the legacy carry served family=%q with family_source=carried from the SAME refused carrier",
			decision.Disposition, decision.Reason, family)
	}
	assertContinuationRefused(t, result)
}

// R1-3: a window-receipt request that fails graph binding emits no decision
// line, so the event's denominator is not "requests carrying window receipts".
func TestWindowContinuation_R1_AWindowReceiptRequestEmitsADecisionEvenWhenGraphBindingFails(t *testing.T) {
	request := continuationRequest(validInvestigationRequest().Question)
	prior := continuationPrior(t, continuationPriorID, request.Question, QuestionFamilyDiscoveredCohortRanking, "")
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
	prior := continuationPrior(t, continuationPriorID, request.Question, QuestionFamilyDiscoveredCohortRanking, "")
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

// ---------------------------------------------------------------------------
// THE RULED SHAPE (r2): ONE ADMISSION FUNCTION, and the pins that hold it.
//
// The r2 class was "admission has no single owner". These two pins are what
// stop it coming back: the first enumerates the closed reason vocabulary
// against the paths that can produce it, so a reason with no path (or a path
// with no reason) fails here rather than surfacing as `unspecified` on a live
// line; the second walks the INPUT SHAPE SPACE and asserts all four observable
// consequences at once, so a fix that gets the disposition right while getting
// the served provenance or the frame wrong cannot pass.
// ---------------------------------------------------------------------------

// everyContinuationReason DELEGATES to the producer's own vocabulary.
//
// r1 (#492) found the previous version: a hand-written list in this file, whose
// comment called listing it once a virtue. Two reasons were added to production
// and neither reached the list, so the pin below -- the one whose entire job is
// to catch a reason with no driver -- could not see them at all. A test that
// owns its own copy of a production vocabulary is an oracle that agrees with
// itself.
func everyContinuationReason() []ContinuationDecisionReason {
	return continuationDecisionReasons()
}

// reasonDriver is a request shape that reaches ONE reason through
// Engine.Investigate.
type reasonDriver struct {
	reason      ContinuationDecisionReason
	mutate      func(*InvestigationRequest)
	prior       func(InvestigationResult) InvestigationResult
	interpreter QuestionInterpreter
	bindingErr  bool
	cancel      bool
	storeEpoch  *int64
	principal   *storage.Principal
	// resultsWrap replaces the store the engine saves through, for the one
	// reason that is decided at SAVE time rather than at admission.
	resultsWrap func(*staticResultStore) InvestigationResultStore
}

// TestWindowContinuation_EveryReasonIsReachedThroughTheEngine is the r3 F4
// rewrite, and the difference is the whole point.
//
// THE OLD PIN COMPARED TWO HAND-WRITTEN LISTS. It read no production code, so a
// production path that assigned no reason could not fail it -- and that is
// precisely how two such paths (an unanswerable caller bound, a cancelled
// context) shipped and published `unspecified` on a live line. A pin that
// cannot fail is worse than no pin, because it is counted as coverage.
//
// This one DRIVES every member through the public entry point and reads the
// reason off the EMITTED event. A member with no driver fails here; a driver
// that stops reaching its member fails here; and `unspecified` has no driver by
// construction, which is the assertion that no real path produces it.
func TestWindowContinuation_EveryReasonIsReachedThroughTheEngine(t *testing.T) {
	t.Parallel()

	base := validInvestigationRequest().Question
	futureAsOf := time.Unix(9_000_000, 0).UTC()
	staleEpoch := int64(97)
	unauthenticated := storage.Principal{}

	drivers := []reasonDriver{
		{reason: ContinuationReasonNone},
		{
			reason: ContinuationReasonNotWindowOnly,
			mutate: func(r *InvestigationRequest) { r.ParentResultID = continuationOlderID },
		},
		{
			reason: ContinuationReasonWindowVeto,
			mutate: func(r *InvestigationRequest) {
				r.PriorWindowReceipts = append(r.PriorWindowReceipts,
					BoundSubjectReceipt{ResultID: continuationOlderID, ReceiptID: "winr_5465secondaaaaaaaa"})
			},
		},
		{
			reason: ContinuationReasonChangedQuestion,
			prior:  func(p InvestigationResult) InvestigationResult { p.Question = driftQuestion; return p },
		},
		{
			reason: ContinuationReasonIndeterminateIdentity,
			mutate: func(r *InvestigationRequest) { r.Question = "?" },
			prior:  func(p InvestigationResult) InvestigationResult { p.Question = "!!"; return p },
		},
		{
			reason: ContinuationReasonMissingContext,
			prior:  func(p InvestigationResult) InvestigationResult { p.AnswerPlan = nil; return p },
		},
		{reason: ContinuationReasonInvalidContext, storeEpoch: &staleEpoch},
		{
			reason: ContinuationReasonContextVersionMismatch,
			prior: func(p InvestigationResult) InvestigationResult {
				p.AnswerPlan.FamilyVersion = "question-family.v0-not-in-force"
				return p
			},
		},
		{
			reason:      ContinuationReasonFreshContextUnavailable,
			interpreter: forcedFamilyInterpreter{err: errBindingUnavailableForRepro},
		},
		{reason: ContinuationReasonBindingUnavailable, bindingErr: true},
		{
			reason: ContinuationReasonStructureVeto,
			mutate: func(r *InvestigationRequest) {
				r.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: continuationPriorID, ReceiptID: "kindr_5465unresolvable01"}}
			},
		},
		{
			reason: ContinuationReasonExplicitStructureHint,
			mutate: func(r *InvestigationRequest) {
				r.ExpectedKinds = []SubjectKind{contractsv1.ContextFabricSubjectProject}
			},
		},
		{
			reason:      ContinuationReasonInterpretedAxisVeto,
			interpreter: axisMovingInterpreter{family: QuestionFamilyGroupedCohortStatus},
		},
		{
			reason: ContinuationReasonRequestInvalid,
			mutate: func(r *InvestigationRequest) { r.SchemaVersion = "not-a-schema-version" },
		},
		{reason: ContinuationReasonPrincipalUnauthenticated, principal: &unauthenticated},
		{
			reason: ContinuationReasonRequestTimeUnresolvable,
			mutate: func(r *InvestigationRequest) {
				r.TimeContext = TimeContext{Axis: TemporalValidTime, AsOf: &futureAsOf}
			},
		},
		{reason: ContinuationReasonRequestCancelled, cancel: true},
		{
			reason:      ContinuationReasonAsOfUnresolvable,
			interpreter: futureAsOfInterpreter{family: QuestionFamilyGroupedCohortStatus},
		},
		{
			// The carried reading is grouped and the fresh frame has no
			// grouped expression to put the axis in, so the composition is
			// refused rather than served without its axis.
			reason:      ContinuationReasonCompositionInvalid,
			interpreter: r1UngroupedInterpreter{family: QuestionFamilyDiscoveredCohortRanking},
		},
		{
			// Decided at SAVE time, not at admission: the continuation was
			// applied and the window did not survive the save.
			reason: ContinuationReasonWindowSuperseded,
			resultsWrap: func(store *staticResultStore) InvestigationResultStore {
				return &supersessionRacingResultStore{
					staticResultStore: store,
					conflictMembers:   []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedWindow},
				}
			},
		},
	}

	// EVERY MEMBER HAS A DRIVER, and `unspecified` deliberately has none.
	driven := map[ContinuationDecisionReason]bool{}
	for _, d := range drivers {
		if driven[d.reason] {
			t.Fatalf("duplicate driver for %q", d.reason)
		}
		driven[d.reason] = true
	}
	for _, reason := range everyContinuationReason() {
		if reason == ContinuationReasonUnspecified {
			if driven[reason] {
				t.Errorf("`unspecified` has a driver -- it is the fail-closed member and no real path may produce it")
			}
			continue
		}
		if !driven[reason] {
			t.Errorf("closed reason %q has NO driver through the engine -- either the member is dead, or a decision site was added without one, which is how `unspecified` reaches a live line", reason)
		}
	}

	for _, d := range drivers {
		t.Run(string(d.reason), func(t *testing.T) {
			t.Parallel()

			request := continuationRequest(base)
			if d.mutate != nil {
				d.mutate(&request)
			}
			prior := continuationPrior(t, continuationPriorID, base, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
			older := continuationPrior(t, continuationOlderID, base, QuestionFamilyDiscoveredCohortRanking, "")
			if d.prior != nil {
				prior = d.prior(prior)
			}
			baseStore := &staticResultStore{
				results:    map[string]InvestigationResult{prior.ResultID: prior, older.ResultID: older},
				graphEpoch: d.storeEpoch,
			}
			var store InvestigationResultStore = baseStore
			if d.resultsWrap != nil {
				store = d.resultsWrap(baseStore)
			}
			project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
			telemetry := &recordingTelemetry{}
			fresh := validInvestigationResult()

			var interpreter QuestionInterpreter = forcedFamilyInterpreter{
				family: QuestionFamilyDiscoveredCohortRanking,
			}
			if d.interpreter != nil {
				interpreter = d.interpreter
			}
			deps := EngineDependencies{
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
			}
			if d.bindingErr {
				deps.Graph = bindingFailingGraphReader{err: errBindingUnavailableForRepro}
			}
			principal := acceptancePrincipal()
			if d.principal != nil {
				principal = *d.principal
			}
			ctx := context.Background()
			if d.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			//nolint:errcheck // several drivers END the turn with an error on purpose.
			_, _ = mustReuseTestEngine(t, deps).Investigate(ctx, principal, request)

			if len(telemetry.windowContinuationDecisions) != 1 {
				t.Fatalf("got %d decisions, want exactly 1 -- this driver must reach the emitter", len(telemetry.windowContinuationDecisions))
			}
			got := telemetry.windowContinuationDecisions[0].Reason
			t.Logf("driver for %q -> emitted reason %q", d.reason, got)
			if got != d.reason {
				t.Errorf("emitted reason = %q, want %q -- the driver no longer reaches the member it is written for", got, d.reason)
			}
			if got == ContinuationReasonUnspecified {
				t.Errorf("`unspecified` reached the emitter on a real path")
			}
			// AND THE OTHER DIRECTION. The check above asks whether every member
			// has a path; this asks whether every emitted value is a member. A
			// decision site that invents a reason string reaches the line as free
			// text in a closed telemetry field, and nothing else in the package
			// would object.
			if !ValidContinuationDecisionReason(got) {
				t.Errorf("emitted reason %q is OUTSIDE the closed vocabulary -- a value no consumer can group on", got)
			}
			// AND THE COMPOSITION OUTCOME, ON EVERY EXIT (r2 F2). This is the
			// pin that catches a second constructor: admission used to rebuild
			// the decision from a literal, dropping the field the real
			// constructor sets, so an ordinary admission exit published the
			// unrecognised sentinel on a closed field. Every driver here ends at
			// a different exit, so asserting membership WITH A VALUE across all
			// of them is what makes "assigned above every return" checkable
			// rather than merely stated in a comment.
			composition := telemetry.windowContinuationDecisions[0].CompositionOutcome
			if composition == "" {
				t.Errorf("composition_outcome is EMPTY at the %q exit -- some path builds this struct without the constructor", d.reason)
			}
			if !ValidCompositionOutcome(composition) {
				t.Errorf("composition_outcome %q at the %q exit is outside the closed vocabulary", composition, d.reason)
			}
		})
	}
}

// TestWindowContinuation_TheInputShapeSpace walks the shapes the r2 ruling names
// and asserts ALL FOUR observable consequences on each, because a fix that gets
// the disposition right while getting the served provenance or the frame wrong
// passes any one of them alone.
func TestWindowContinuation_TheInputShapeSpace(t *testing.T) {
	t.Parallel()

	base := validInvestigationRequest().Question

	for _, tc := range []struct {
		name            string
		mutate          func(*InvestigationRequest)
		bindingFails    bool
		axisMoves       bool
		wantDisposition ContinuationDisposition
		wantReason      ContinuationDecisionReason
		wantCarried     bool // served family_source == carried
		wantFrameSeen   bool // ResolveSubjects saw a NON-nil frame
	}{
		{
			name:            "admitted",
			wantDisposition: ContinuationApplied,
			wantReason:      ContinuationReasonNone,
			wantCarried:     true,
			wantFrameSeen:   true,
		},
		{
			name: "explicit expected kinds",
			mutate: func(r *InvestigationRequest) {
				r.ExpectedKinds = []SubjectKind{contractsv1.ContextFabricSubjectProject}
			},
			wantDisposition: ContinuationNotApplicable,
			wantReason:      ContinuationReasonExplicitStructureHint,
			wantCarried:     false,
			wantFrameSeen:   true,
		},
		{
			name: "explicit subject handles",
			mutate: func(r *InvestigationRequest) {
				r.SubjectHandles = []contractsv1.ContextFabricRequestedHandle{{Kind: SubjectPullRequest, PatternID: "pull_request_number", Value: "532"}}
			},
			wantDisposition: ContinuationNotApplicable,
			wantReason:      ContinuationReasonExplicitStructureHint,
			wantCarried:     false,
			wantFrameSeen:   true,
		},
		{
			name:            "interpreted axis veto",
			axisMoves:       true,
			wantDisposition: ContinuationNotApplicable,
			wantReason:      ContinuationReasonInterpretedAxisVeto,
			wantCarried:     false,
			// the turn ends at the axis-conflict veto before ResolveSubjects
			wantFrameSeen: false,
		},
		{
			name:            "binding failure",
			bindingFails:    true,
			wantDisposition: ContinuationNotApplicable,
			wantReason:      ContinuationReasonBindingUnavailable,
			wantCarried:     false,
			wantFrameSeen:   false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			request := continuationRequest(base)
			if tc.mutate != nil {
				tc.mutate(&request)
			}
			prior := continuationPrior(t, continuationPriorID, base, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
			project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
			graph := &frameRecordingGraphReader{graphReaderStub: graphReaderStub{
				resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
				bases:      provenCommitBases(project),
			}}
			telemetry := &recordingTelemetry{}
			fresh := validInvestigationResult()

			var interpreter QuestionInterpreter = frameBearingInterpreter{
				family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectProject,
				frameGroup: contractsv1.ContextFabricSubjectProject,
			}
			if tc.axisMoves {
				interpreter = axisMovingInterpreter{family: QuestionFamilyGroupedCohortStatus}
			}
			deps := EngineDependencies{
				Graph: graph,
				Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
					return CanonicalFactBundle{}, nil
				}),
				Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
					return fresh, nil
				}),
				Interpreter: interpreter,
				Results:     &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
				Telemetry:   telemetry,
			}
			if tc.bindingFails {
				deps.Graph = bindingFailingGraphReader{err: errBindingUnavailableForRepro}
			}
			result, err := mustReuseTestEngine(t, deps).Investigate(context.Background(), acceptancePrincipal(), request)
			if tc.bindingFails {
				if err == nil {
					t.Fatalf("wanted a binding failure")
				}
			} else if err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}

			if len(telemetry.windowContinuationDecisions) != 1 {
				t.Fatalf("got %d decisions, want exactly 1 on every shape in this space", len(telemetry.windowContinuationDecisions))
			}
			d := telemetry.windowContinuationDecisions[0]
			t.Logf("%s -> disposition=%q reason=%q served_source=%q frame_seen=%v",
				tc.name, d.Disposition, d.Reason,
				func() string {
					if result.AnswerPlan == nil {
						return "<none>"
					}
					return string(servedPlanSource(result))
				}(), graph.lastFrameNonNil)

			if d.Disposition != tc.wantDisposition {
				t.Errorf("disposition = %q, want %q", d.Disposition, tc.wantDisposition)
			}
			if d.Reason != tc.wantReason {
				t.Errorf("decision_reason = %q, want %q", d.Reason, tc.wantReason)
			}
			if d.Reason == ContinuationReasonUnspecified {
				t.Errorf("decision_reason reached the emitter as `unspecified` -- the fail-closed member must never describe a real path")
			}
			gotCarried := result.AnswerPlan != nil && servedPlanSource(result) == QuestionFamilySourceCarried
			if gotCarried != tc.wantCarried {
				t.Errorf("served family_source carried = %v, want %v", gotCarried, tc.wantCarried)
			}
			if !tc.bindingFails && !tc.axisMoves {
				// THE FRAME IS NEVER NIL FOR THE GRAPH CONSUMERS (r2 R2-3).
				if graph.calls == 0 {
					t.Errorf("ResolveSubjects was never reached; this arm cannot say anything about the frame")
				} else if graph.lastFrameNonNil != tc.wantFrameSeen {
					t.Errorf("ResolveSubjects saw non-nil frame = %v, want %v -- an admitted continuation substitutes ONE component into the proposed frame, it never hands retrieval a nil",
						graph.lastFrameNonNil, tc.wantFrameSeen)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// REGRESSION PINS FROM THE COUNTED r2 REVIEW (BLOCK, four P1s).
//
// Each was reproduced against the pre-ruling tree before anything changed, and
// each is kept as the regression test for the ruled fix. The class was
// "admission has no single owner": a disqualifier that ran too late, a reason
// initialised too early, a mutation applied too widely, and a publication that
// happened before a veto that could undo it. The shape-space pin above is what
// holds the ruled ordering; these four hold the individual defects.
// ---------------------------------------------------------------------------

// R2-1: an EXPLICIT ExpectedKinds/SubjectHandles hint is a semantic change this
// turn made, but windowOnlyReferencedResultID only rejects RECEIPT fields and a
// parent, so the request still admits as a window-only continuation.
func TestWindowContinuation_R2_AnExplicitStructureHintDisqualifiesTheContinuation(t *testing.T) {
	request := continuationRequest(validInvestigationRequest().Question)
	request.ExpectedKinds = []SubjectKind{contractsv1.ContextFabricSubjectProject}
	prior := continuationPrior(t, continuationPriorID, request.Question, QuestionFamilyDiscoveredCohortRanking, "")
	harness := newContinuationHarness(t,
		&staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
		forcedFamilyInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectTeam})

	result := harness.investigate(t, request)
	decision := harness.soleDecision(t)
	t.Logf("R2-1: explicit ExpectedKinds=%v -> disposition=%q reason=%q served_family_source=%q",
		request.ExpectedKinds, decision.Disposition, decision.Reason, servedPlanSource(result))
	if decision.Disposition == ContinuationApplied {
		t.Fatalf("R2-1 REGRESSION: a request that ALSO states an explicit expected kind admitted as a window-only continuation (%q/%q) -- the shape check enumerates receipt fields and parent_result_id only, so an explicit structure hint walks past it",
			decision.Disposition, decision.Reason)
	}
}

// R2-2: a graph-binding failure emits decision_reason="unspecified" even though
// the shape is knowable -- the emitter was moved above binding (R1-3) but the
// initial value is only refined for the NOT-window-only case.
func TestWindowContinuation_R2_ABindingFailureCarriesItsOwnReasonNotUnspecified(t *testing.T) {
	request := continuationRequest(validInvestigationRequest().Question)
	prior := continuationPrior(t, continuationPriorID, request.Question, QuestionFamilyDiscoveredCohortRanking, "")
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
	if len(telemetry.windowContinuationDecisions) != 1 {
		t.Fatalf("fixture defect: want exactly 1 decision, got %d", len(telemetry.windowContinuationDecisions))
	}
	d := telemetry.windowContinuationDecisions[0]
	t.Logf("R2-2: binding failure -> disposition=%q reason=%q", d.Disposition, d.Reason)
	if d.Reason == ContinuationReasonUnspecified {
		t.Fatalf("R2-2 REGRESSION: decision_reason=%q on a binding failure -- unspecified is the fail-closed member meaning a decision site recorded nothing, so it reads as a defect on a path that is simply not reached",
			d.Reason)
	}
}

// R2-3: clearing the fresh frame (the R1-1 fix) sends nil to the CURRENT-request
// graph consumers, which read the frame for this turn's retrieval.
func TestWindowContinuation_R2_TheGraphConsumersNeverSeeANilFrame(t *testing.T) {
	request := continuationRequest(validInvestigationRequest().Question)
	prior := continuationPrior(t, continuationPriorID, request.Question, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	graph := &frameRecordingGraphReader{graphReaderStub: graphReaderStub{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		bases:      provenCommitBases(project),
	}}
	telemetry := &recordingTelemetry{}
	fresh := validInvestigationResult()
	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: graph,
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return fresh, nil
		}),
		Interpreter: frameBearingInterpreter{
			family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectTeam,
			frameGroup: contractsv1.ContextFabricSubjectTeam,
		},
		Results:   &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
		Telemetry: telemetry,
	})
	if _, err := engine.Investigate(context.Background(), acceptancePrincipal(), request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	t.Logf("R2-3: ResolveSubjects saw frame=%v (calls=%d); the interpreter PROPOSED a non-nil frame",
		graph.lastFrameNonNil, graph.calls)
	if graph.calls > 0 && !graph.lastFrameNonNil {
		t.Fatalf("R2-3 REGRESSION: the fresh frame was cleared for the PLANNER and the nil then reached ResolveSubjects, a CURRENT-request consumer that reads the frame for this turn's retrieval -- the fix for R1-1 is scoped wider than the defect it closed")
	}
}

// R2-4: a continuation is logged `applied` before the interpreted-axis veto, so
// the served terminal can carry no effective window while the line says one was
// applied.
func TestWindowContinuation_R2_AppliedIsNeverPublishedForATurnTheAxisVetoUndoes(t *testing.T) {
	request := continuationRequest(validInvestigationRequest().Question)
	prior := continuationPrior(t, continuationPriorID, request.Question, QuestionFamilyDiscoveredCohortRanking, "")
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
		// The interpreter moves the axis AWAY from current, which is the
		// documented shape that drops a confirmed window.
		Interpreter: axisMovingInterpreter{family: QuestionFamilyGroupedCohortStatus},
		Results:     &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
		Telemetry:   telemetry,
	})
	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(telemetry.windowContinuationDecisions) != 1 {
		t.Fatalf("fixture defect: want exactly 1 decision, got %d", len(telemetry.windowContinuationDecisions))
	}
	d := telemetry.windowContinuationDecisions[0]
	t.Logf("R2-4: disposition=%q applied_window=%q served_effective_window_present=%v served_status=%q",
		d.Disposition, d.AppliedWindowToken(), result.EffectiveEvidenceWindow != nil, result.Status)
	if d.Disposition == ContinuationApplied && d.AppliedWindowToken() != "" && result.EffectiveEvidenceWindow == nil {
		t.Fatalf("R2-4 REGRESSION: the line says a continuation APPLIED with applied_window=%q, and the served result carries NO effective window -- the decision is taken and published before the interpreted-axis step that can drop it",
			d.AppliedWindowToken())
	}
}

// axisMovingInterpreter classifies AND moves the interpreted axis off current.
type axisMovingInterpreter struct{ family QuestionFamily }

func (a axisMovingInterpreter) Interpret(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error) {
	return InterpretedQuestion{
		Shape: ShapeOpen, RequestedJudgment: "status",
		TimeContext: TimeContext{Axis: TemporalValidTime, AsOf: &r2AsOf},
	}, QuestionFamilyOutcome{
		Family: a.family, Source: QuestionFamilySourceModel,
		WinningSampleIndex: 0,
		WinningSample:      FamilySample{ModelFamily: a.family},
		Version:            QuestionFamilyTableVersion,
	}, nil
}

// frameRecordingGraphReader records whether ResolveSubjects saw a frame.
type frameRecordingGraphReader struct {
	graphReaderStub
	calls           int
	lastFrameNonNil bool
}

func (g *frameRecordingGraphReader) ResolveSubjects(ctx context.Context, p storage.Principal, r InvestigationRequest, q InterpretedQuestion, b ResolvedGraphBinding, k *ConfirmedExpectedKind, a *ConfirmedAnchorSelection, frame *QuestionFrame, mk SubjectKind) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	g.calls++
	g.lastFrameNonNil = frame != nil
	return g.graphReaderStub.ResolveSubjects(ctx, p, r, q, b, k, a, frame, mk)
}

// A PAST as-of, so the axis move is answerable and the veto under test is the
// WINDOW-drop one (composeEffectiveWindow's interpreted-axis gate), not the
// unanswerable-bounds refusal.
// mustReuseTestEngine pins Now() to time.Unix(200,0).UTC(), so an answerable
// as-of must be BEFORE that instant, not before the wall clock.
var r2AsOf = time.Unix(100, 0).UTC()

// ---------------------------------------------------------------------------
// TWO MORE ARMS THE MUTANT BATTERY FOUND UNGUARDED. Both are findings, both are
// pinned and killed here rather than explained away.
// ---------------------------------------------------------------------------

// SURVIVOR 1: deleting the typed-selection exclusion from the shape check
// survived, because the only arm that exercised it used an UNRESOLVABLE kind
// receipt -- which structure canonicalisation vetoes before admission ever
// runs. The exclusion is only load-bearing when the typed receipt RESOLVES, so
// the request reaches admission carrying a legitimate typed selection beside
// its window receipt. That is this arm.
func TestWindowContinuation_AResolvableTypedReceiptBesideAWindowReceiptIsNotAContinuation(t *testing.T) {
	t.Parallel()

	base := validInvestigationRequest().Question
	prior := continuationPrior(t, continuationPriorID, base, QuestionFamilyDiscoveredCohortRanking, "")
	// The SAME prior also OFFERED an expected kind. A kindr_ receipt resolves
	// against StructureNeeds.KindOptions -- the offer it redeems -- not against
	// ConfirmedStructure, which is what a previous turn already applied. Getting
	// that wrong is what made the first version of this arm measure the veto.
	prior.StructureNeeds = &StructureNeeds{
		Missing: []StructureNeedKind{"expected_kind"},
		KindOptions: []KindOption{{
			ReceiptID: "kindr_5465resolvable0001", OptionID: "opt_team",
			Label: "a team", Kind: SubjectTeam, OfferSource: "engine",
		}},
	}

	request := continuationRequest(base)
	request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: prior.ResultID, ReceiptID: "kindr_5465resolvable0001"}}

	harness := newContinuationHarness(t,
		&staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
		// Classifies NOTHING, so if the shape check let this through the carry
		// would apply and the arm would be measuring the wrong thing.
		interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}))

	result := harness.investigate(t, request)
	decision := harness.soleDecision(t)
	servedSource := "<no plan>"
	if result.AnswerPlan != nil {
		servedSource = string(servedPlanSource(result))
	}
	t.Logf("resolvable typed receipt + window receipt -> disposition=%q reason=%q served_source=%s",
		decision.Disposition, decision.Reason, servedSource)

	if decision.Reason == ContinuationReasonStructureVeto {
		t.Fatalf("fixture defect: the kind receipt did not resolve, so this arm is measuring the veto and not the shape check")
	}
	if decision.Disposition != ContinuationNotApplicable || decision.Reason != ContinuationReasonNotWindowOnly {
		t.Errorf("disposition/reason = %q/%q, want not_applicable/not_window_only -- a caller that redeems a KIND offer alongside a window offer is having that selection's turn, not a window-only continuation",
			decision.Disposition, decision.Reason)
	}
}

// SURVIVOR 2: mutating the shared frame in place instead of copying it survived
// -- nothing asserted that the interpreter's OWN frame object comes back
// unchanged. It matters because that pointer is held by the interpretation
// receipt and the family outcome: rewriting it in place would silently change
// the record of what the model actually proposed, which is the one thing that
// must stay true even when the server overrides it.
func TestWindowContinuation_TheAcceptedFrameIsACopyAndLeavesTheProposedFrameIntact(t *testing.T) {
	t.Parallel()

	base := validInvestigationRequest().Question
	prior := continuationPrior(t, continuationPriorID, base, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
	request := continuationRequest(base)

	// The interpreter hands out ONE frame object and keeps the pointer, exactly
	// as the production interpreter does through its receipt.
	// A FRAME THAT VALIDATES, and composes to a frame that also validates.
	// Composition now refuses an invalid composition, so the aliasing property
	// this pin measures -- the substitution happens on a COPY and the model's
	// proposed frame is left intact -- can only be reached through a fixture
	// the server would really accept.
	shared := &QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{
			Kind: SubjectExpressionGroupedMembers,
			Grouped: &GroupedSetExpression{
				GroupKind: contractsv1.ContextFabricSubjectProject,
				// NOT `team`: the carried axis IS team, and a composition whose
				// group kind equals its member kind is what invariant I6
				// forbids -- the fixture would then measure the refusal rather
				// than the copy.
				MemberKind: contractsv1.ContextFabricSubjectRepository,
			},
		},
		Temporal: TemporalIntentCurrent,
	}
	harness := newContinuationHarness(t,
		&staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
		sharedFrameInterpreter{family: QuestionFamilyGroupedCohortStatus, frame: shared})

	result := harness.investigate(t, request)
	decision := harness.soleDecision(t)
	if decision.Disposition != ContinuationApplied {
		t.Fatalf("fixture defect: wanted an applied continuation, got %q/%q", decision.Disposition, decision.Reason)
	}
	t.Logf("proposed frame group_kind after the turn = %q; served group_kind = %q",
		shared.SubjectExpression.Grouped.GroupKind, servedPlanGroup(result))

	if shared.SubjectExpression.Grouped.GroupKind != contractsv1.ContextFabricSubjectProject {
		t.Errorf("the interpreter's OWN frame was mutated to %q -- admission must copy the frame and substitute into the copy, never rewrite the object every other holder shares",
			shared.SubjectExpression.Grouped.GroupKind)
	}
	if servedPlanGroup(result) != contractsv1.ContextFabricSubjectTeam {
		t.Errorf("served group_kind = %q, want the carried %q", servedPlanGroup(result), contractsv1.ContextFabricSubjectTeam)
	}
}

// sharedFrameInterpreter hands back the SAME frame pointer every call, so a
// mutation of it is observable from the test.
type sharedFrameInterpreter struct {
	family QuestionFamily
	frame  *QuestionFrame
}

func (s sharedFrameInterpreter) Interpret(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error) {
	return InterpretedQuestion{
		Shape: ShapeOpen, RequestedJudgment: "status",
		TimeContext: TimeContext{Axis: TemporalCurrent},
	}, QuestionFamilyOutcome{
		Family: s.family, Source: QuestionFamilySourceModel, Frame: s.frame,
		WinningSampleIndex: 0,
		WinningSample:      FamilySample{ModelFamily: s.family, GroupKind: contractsv1.ContextFabricSubjectProject},
		Version:            QuestionFamilyTableVersion,
	}, nil
}

// ---------------------------------------------------------------------------
// REGRESSION PINS FROM THE COUNTED r3 REVIEW (REQUEST CHANGES; two P1, one P2,
// one P3 -- all four real, all four reproduced before any fix).
//
// The two P1s were the SAME class the r2 ruling was meant to close, at two exits
// the ruling's own list did not reach: an error return, and a guard sitting
// above where the decision was declared. That is why the decision is now built
// by a CONSTRUCTOR at the very top of Investigate, above every `return` in the
// function -- there is no ordering argument left to get wrong -- and why the
// enumeration below DRIVES production instead of comparing two hand-written
// lists, which is what let both slip through in the first place.
// ---------------------------------------------------------------------------

// F1 (P1): the interpreted time-bound error returns AFTER the deferred emitter
// is installed and WITHOUT assigning a reason, so the line publishes
// `unspecified` -- the fail-closed member -- on a real path.
func TestWindowContinuation_R3_TheInterpretedTimeBoundErrorCarriesItsOwnReason(t *testing.T) {
	request := continuationRequest(validInvestigationRequest().Question)
	prior := continuationPrior(t, continuationPriorID, request.Question, QuestionFamilyDiscoveredCohortRanking, "")
	telemetry := &recordingTelemetry{}
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
		// An as-of AFTER the pinned test clock -> resolveTimeContext errors.
		Interpreter: futureAsOfInterpreter{family: QuestionFamilyGroupedCohortStatus},
		Results:     &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
		Telemetry:   telemetry,
	})
	// CHAOS-5421 turned this exit from a bare error into a SERVED TERMINAL, so
	// the pin asserts the terminal rather than an error -- the property under
	// test is that the exit names its own reason, not how it reports itself.
	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("wanted a served interpreted-time-bound terminal, got error %v", err)
	}
	if result.Status == "" {
		t.Fatalf("fixture defect: no terminal served")
	}
	if len(telemetry.windowContinuationDecisions) != 1 {
		t.Fatalf("want exactly 1 decision, got %d", len(telemetry.windowContinuationDecisions))
	}
	d := telemetry.windowContinuationDecisions[0]
	t.Logf("F1: error=%v decision_reason=%q disposition=%q", err, d.Reason, d.Disposition)
	if d.Reason != ContinuationReasonAsOfUnresolvable {
		t.Fatalf("F1 REGRESSION: decision_reason=%q on the interpreted time-bound refusal, want %q. Asserting only 'not unspecified' let this pass while carrying a DIFFERENT member -- a pin must name the value it is protecting, not merely reject one wrong answer",
			d.Reason, ContinuationReasonAsOfUnresolvable)
	}
}

// F2 (P1): a cancelled context returns BEFORE the decision and its deferred
// emitter are created, so a request carrying a window receipt emits nothing --
// contradicting the census this event's own contract states.
func TestWindowContinuation_R3_ACancelledWindowRequestStillEmitsADecision(t *testing.T) {
	request := continuationRequest(validInvestigationRequest().Question)
	prior := continuationPrior(t, continuationPriorID, request.Question, QuestionFamilyDiscoveredCohortRanking, "")
	telemetry := &recordingTelemetry{}
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
		Telemetry:   telemetry,
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := engine.Investigate(ctx, acceptancePrincipal(), request)
	t.Logf("F2: error=%v decisions=%d", err, len(telemetry.windowContinuationDecisions))
	if err == nil {
		t.Fatalf("fixture defect: wanted a cancellation error")
	}
	if len(telemetry.windowContinuationDecisions) != 1 {
		t.Fatalf("F2 REGRESSION: %d decision lines for a cancelled window-receipt request -- the event's contract says a missing line means only `no window receipt`", len(telemetry.windowContinuationDecisions))
	}
}

// F3 (P2) IS PINNED ELSEWHERE, AND DELIBERATELY NOT HERE. The finding was that
// the D-0 negative control had stopped discriminating the legacy
// applyCarriedPlan path from the new continuation emitter. Its regression pins
// are TestWindowContinuation_D0ControlA_TheLegacyCarryStillApplies and
// TestWindowContinuation_D0ControlB_TheContinuationEmitCannotPassAsLegacy in
// chaos5465_d0_probe_test.go, which assert the two attributions separately and
// assert that neither path can satisfy the other's counter. A repro asserting
// the OLD control's weakness cannot be kept green, because the old control no
// longer exists.

// F4 (P3): the two members the production-driven enumeration proved unreachable
// stay removed, and this is what stops either being re-added on the belief that
// a closed vocabulary should name every branch.
//
// Both were reachable-looking and neither is reachable for THIS event's
// population, which is the only population it describes.
func TestWindowContinuation_R3_TheRemovedReasonsAreGenuinelyUnreachable(t *testing.T) {
	t.Parallel()

	request := continuationRequest(validInvestigationRequest().Question)
	if !requestCarriesWindowReceipts(request) {
		t.Fatalf("fixture defect: this population is requests carrying a window receipt")
	}

	// `answer_reused`: reuse is BYPASSED for exactly this population --
	// reuseBypassReason keys the bypass on the same receipt set
	// carryReferencedResultIDs collects (CHAOS-4998).
	bypass := reuseBypassReason(request, requestStructureCanonicalization{})
	if bypass == "" {
		t.Errorf("a window-receipt request no longer bypasses answer reuse -- if that is intended, `answer_reused` becomes reachable and needs a member AND a driver")
	}
	t.Logf("answer reuse bypassed for this population: %q", bypass)

	// `window_confirmation_required`: that gate fires only on
	// ExplicitUnconfirmed, which window.go documents as true ONLY for the
	// MCP bare-explicit field at inferred_default -- "never question_stated or
	// clarification_confirmed". A redeemed window receipt is
	// clarification_confirmed.
	for _, reason := range everyContinuationReason() {
		switch string(reason) {
		case "answer_reused", "window_confirmation_required":
			t.Errorf("reason %q is back in the vocabulary; the enumeration proved it has no driver in this event's population, so it reads as coverage and measures nothing", reason)
		}
	}
}

// futureAsOfInterpreter classifies and returns an as-of AFTER the pinned test
// clock, which resolveTimeContext refuses.
type futureAsOfInterpreter struct{ family QuestionFamily }

func (f futureAsOfInterpreter) Interpret(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error) {
	// RE-DERIVED AGAINST CHAOS-5421, which landed while this branch was in
	// review. A FUTURE as-of is no longer unanswerable -- it is CLAMPED and
	// answered -- so a driver built on one sails past the refusal exit and is
	// caught later by the interpreted-axis disqualifier, reaching a DIFFERENT
	// member. The genuinely unanswerable shape on this axis is an ABSENT as-of
	// (InterpretedTimeBoundAbsentOrZero). The enumeration pin caught this,
	// because it asserts the driver still reaches the member it is written for
	// -- which a presence-only assertion never would.
	return InterpretedQuestion{
		Shape: ShapeOpen, RequestedJudgment: "status",
		TimeContext: TimeContext{Axis: TemporalValidTime},
	}, QuestionFamilyOutcome{
		Family: f.family, Source: QuestionFamilySourceModel,
		WinningSampleIndex: 0,
		WinningSample:      FamilySample{ModelFamily: f.family},
		Version:            QuestionFamilyTableVersion,
	}, nil
}

// The battery found withReason's widening guard unpinned: deleting it survived,
// because no production caller passes `unspecified` today. That makes it a
// defensive branch nothing covers -- which is the shape this whole change keeps
// being told off for, so it is pinned rather than left to read as coverage.
//
// It is KEPT rather than deleted as equivalent because the property is real: a
// future exit that computes its reason (rather than naming a literal) and gets
// the fail-closed member back must not thereby erase a better reason an earlier
// site already recorded. Deleting it would make that silent.
func TestWindowContinuation_WithReasonNarrowsAndNeverWidensToUnspecified(t *testing.T) {
	t.Parallel()

	base := newWindowContinuationDecision(continuationRequest(validInvestigationRequest().Question))
	if base.Reason != ContinuationReasonUnspecified {
		t.Fatalf("fixture defect: a window-only shape should start at the fail-closed member, got %q", base.Reason)
	}

	narrowed := base.withReason(ContinuationReasonRequestCancelled)
	if narrowed.Reason != ContinuationReasonRequestCancelled {
		t.Fatalf("withReason did not narrow: got %q, want %q", narrowed.Reason, ContinuationReasonRequestCancelled)
	}

	// THE GUARD. A later site handing back the fail-closed member must not
	// erase what an earlier one already knew.
	widened := narrowed.withReason(ContinuationReasonUnspecified)
	if widened.Reason != ContinuationReasonRequestCancelled {
		t.Fatalf("withReason widened a known reason back to %q -- an exit that computes its reason could then erase a better one recorded upstream, and the emitter would publish the fail-closed member for a path that was fully understood", widened.Reason)
	}

	// And it still narrows AGAIN afterwards, so the guard is not a freeze.
	renarrowed := widened.withReason(ContinuationReasonAsOfUnresolvable)
	if renarrowed.Reason != ContinuationReasonAsOfUnresolvable {
		t.Fatalf("withReason stopped narrowing after the guard fired: got %q", renarrowed.Reason)
	}
}

// ---------------------------------------------------------------------------
// POST-REBASE PINS. CHAOS-5421 (#476) landed on main while this branch was in
// review and replaced the exact statement one of these reasons hung off, so the
// two changes now describe overlapping ground. These hold the seam between them.
// ---------------------------------------------------------------------------

// The two events describe THE SAME TURN, joined by one request id.
//
// CHAOS-5421 emits an interpreted-time-bound decision unconditionally; this
// change emits a continuation decision on every window-receipt request. On a
// turn that is both, an operator must be able to put them side by side -- and
// the join has to be a fact, not an assumption, because the two were written by
// different changes with no shared test until this one.
func TestWindowContinuation_JoinsTheInterpretedTimeBoundEventOnOneTurn(t *testing.T) {
	t.Parallel()

	request := continuationRequest(validInvestigationRequest().Question)
	prior := continuationPrior(t, continuationPriorID, request.Question, QuestionFamilyDiscoveredCohortRanking, "")
	harness := newContinuationHarness(t,
		&staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
		forcedFamilyInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectTeam})

	result := harness.investigate(t, request)
	decision := harness.soleDecision(t)

	// EXACTLY ONE of each on this turn: the continuation event's census is
	// "every window-receipt request", and CHAOS-5421's is "every turn reaching
	// the post-Interpret verdict". A turn that is both must produce one of each,
	// never two of either.
	if got := len(harness.telemetry.interpretedTimeBounds); got != 1 {
		t.Fatalf("interpreted-time-bound events = %d, want exactly 1 on a turn that reached the verdict", got)
	}
	if decision.Disposition != ContinuationApplied {
		t.Fatalf("fixture defect: wanted an applied continuation, got %q/%q", decision.Disposition, decision.Reason)
	}
	bound := harness.telemetry.interpretedTimeBounds[0]
	t.Logf("same turn: continuation disposition=%q reason=%q | interpreted bound axis=%q outcome=%q clamped=%v | served status=%q",
		decision.Disposition, decision.Reason, bound.Axis, bound.Outcome, bound.ClampApplied, result.Status)

	// The two agree about the turn they describe: the continuation was admitted,
	// which requires an ANSWERABLE bound, so the bound event must say so. A
	// build where a continuation could be applied on an unanswerable bound would
	// fail here -- which is the ordering CHAOS-5465's own disqualifier asserts,
	// checked from the OTHER change's event rather than from its own.
	if !bound.Answerable() {
		t.Errorf("a continuation was APPLIED on a turn whose interpreted bound was not answerable (outcome=%q) -- the two events disagree about the same turn", bound.Outcome)
	}
	if bound.Axis != TemporalCurrent {
		t.Errorf("bound axis = %q, want %q on an admitted continuation -- the interpreted-axis disqualifier should have refused anything else", bound.Axis, TemporalCurrent)
	}
}

// MEASURED VALUES ARE PINNED BY VALUE, NOT BY PRESENCE (#482's lesson).
//
// applied_window is the one field on this event that carries measured content
// rather than a closed token, and an earlier round already caught it declared,
// logged and permanently empty. Asserting only that the key exists would not
// have caught that, and would not catch a build that emitted a window with the
// right shape and the wrong bounds.
func TestWindowContinuation_TheAppliedWindowCarriesItsActualFrozenBounds(t *testing.T) {
	t.Parallel()

	request := continuationRequest(validInvestigationRequest().Question)
	prior := continuationPrior(t, continuationPriorID, request.Question, QuestionFamilyDiscoveredCohortRanking, "")
	harness := newContinuationHarness(t,
		&staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
		forcedFamilyInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectTeam})

	result := harness.investigate(t, request)
	decision := harness.soleDecision(t)
	if decision.Disposition != ContinuationApplied {
		t.Fatalf("fixture defect: wanted an applied continuation, got %q/%q", decision.Disposition, decision.Reason)
	}

	// The fixture's offer freezes 2026-05-01 -> 2026-08-01 as a 90-day window
	// confirmed by clarification. Every one of those four components is
	// asserted, because each is separately gettable-wrong: a build could carry
	// the relative id and drop the bounds, or carry bounds from a different
	// offer, or report the wrong provenance for a redeemed receipt.
	const want = "trailing_90d|2026-05-01T00:00:00Z|2026-08-01T00:00:00Z|clarification_confirmed"
	got := decision.AppliedWindowToken()
	t.Logf("applied_window = %q", got)
	if got != want {
		t.Fatalf("applied_window = %q, want %q -- this field is MEASURED content, so it is pinned by value; presence alone would pass on the empty string this field shipped as once already", got, want)
	}

	// And it describes the window the turn actually served, not a value the
	// event carries in isolation.
	if result.EffectiveEvidenceWindow == nil {
		t.Fatalf("served result carries no effective window while the event reports one applied")
	}
	if string(result.EffectiveEvidenceWindow.RelativeID) != "trailing_90d" {
		t.Errorf("served window relative_id = %q, want trailing_90d -- the logged window must be the served one",
			result.EffectiveEvidenceWindow.RelativeID)
	}
	if result.EffectiveEvidenceWindow.Start == nil || result.EffectiveEvidenceWindow.End == nil {
		t.Fatalf("served window has no frozen bounds while the event reports them")
	}
	if result.EffectiveEvidenceWindow.Start.UTC().Format("2006-01-02") != "2026-05-01" ||
		result.EffectiveEvidenceWindow.End.UTC().Format("2006-01-02") != "2026-08-01" {
		t.Errorf("served window bounds = %s..%s, want 2026-05-01..2026-08-01",
			result.EffectiveEvidenceWindow.Start.UTC().Format("2006-01-02"),
			result.EffectiveEvidenceWindow.End.UTC().Format("2006-01-02"))
	}
}

// Conflict counts are measured too: a build reporting a non-empty
// conflict_fields with a zero count, or vice versa, is internally inconsistent.
func TestWindowContinuation_TheConflictCountEqualsTheFieldsItNames(t *testing.T) {
	t.Parallel()

	request := continuationRequest(validInvestigationRequest().Question)
	prior := continuationPrior(t, continuationPriorID, request.Question, QuestionFamilyDiscoveredCohortRanking, "")
	harness := newContinuationHarness(t,
		&staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
		forcedFamilyInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectTeam})

	harness.investigate(t, request)
	d := harness.soleDecision(t)
	t.Logf("conflict_reason=%q conflict_count=%d conflict_fields=%v", d.ConflictReason, d.ConflictCount(), d.ConflictFieldTokens())

	if d.ConflictCount() != len(d.ConflictFieldTokens()) {
		t.Errorf("conflict_count=%d but conflict_fields names %d components -- the number and the list must be the same measurement",
			d.ConflictCount(), len(d.ConflictFieldTokens()))
	}
	if (d.ConflictCount() > 0) != (d.ConflictReason == ContinuationConflictNonWindowContext) {
		t.Errorf("conflict_count=%d beside conflict_reason=%q -- a non-zero count with `none`, or a conflict token with zero components, is a line that contradicts itself",
			d.ConflictCount(), d.ConflictReason)
	}
	if d.ConflictCount() == 0 {
		t.Errorf("fixture defect: this arm forces a family AND subject-expression conflict, so a zero count means the comparison did not run")
	}
}
