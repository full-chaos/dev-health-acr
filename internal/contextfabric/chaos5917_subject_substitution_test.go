package contextfabric

// The subject-substitution guard's own executed input domain, driven through
// the REAL Engine.Investigate on every behavioural cell, plus the pure
// decision's own table. The rig is newNeedTurnHarness (confirmed_need_consumers_test.go)
// verbatim: a real interpreter, a scriptable graph port, a persistent store
// and a wired candidate verifier, so a second turn reads exactly what the
// first one saved and the remembered subject's re-read runs for real.

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var (
	substitutionRepoOne   = SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:alpha-service", Label: "alpha-service"}
	substitutionRepoTwo   = SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:beta-service", Label: "beta-service"}
	substitutionOtherKind = SubjectRef{Kind: SubjectTeam, CanonicalID: "team:platform", Label: "platform"}
)

// substitutionResponse commits exactly subject, on a proven identity, with a
// candidate carrying its own redeemable receipt -- the shape a real
// resolution returns for a subject it committed.
func substitutionResponse(subject SubjectRef, receiptID string) needTurnResponse {
	bases := CommitBasisSet{}
	bases.Record(subject, CommitBasisAuthoritativeIdentity)
	return needTurnResponse{
		resolution: SubjectResolution{
			Committed: []SubjectRef{subject},
			Candidates: []SubjectCandidate{{
				ReceiptID: receiptID, Subject: subject, State: contractsv1.ContextFabricResolutionCommitted,
				MatchedTerms: []string{"service"}, MatchReasons: []string{"matched"}, Confidence: 1,
				EvidenceRefIDs: []string{},
			}},
		},
		bases: bases,
	}
}

// substitutionEmptyResponse commits nothing and offers nothing.
func substitutionEmptyResponse() needTurnResponse {
	return needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}}
}

// substitutionCohortResponse commits two subjects, so neither is
// unambiguously "the subject this turn is about".
func substitutionCohortResponse() needTurnResponse {
	bases := CommitBasisSet{}
	bases.Record(substitutionRepoOne, CommitBasisAuthoritativeIdentity)
	bases.Record(substitutionRepoTwo, CommitBasisAuthoritativeIdentity)
	return needTurnResponse{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{substitutionRepoOne, substitutionRepoTwo}},
		bases:      bases,
	}
}

// lastSubstitution is the guard decision the turn under test published on
// its own confirmed-need-ledger line.
func lastSubstitution(t *testing.T, outcome needTurnOutcome) ConfirmedNeedLedgerEvent {
	t.Helper()
	if len(outcome.ledgers) != 1 {
		t.Fatalf("confirmed-need-ledger lines = %d, want exactly 1", len(outcome.ledgers))
	}
	return outcome.ledgers[0]
}

// assertGuard checks the whole published decision, never the verdict alone:
// a line that names the verdict but loses either identity cannot rebuild the
// decision, which is the bar this axis exists to meet.
func assertGuard(t *testing.T, event ConfirmedNeedLedgerEvent, wantOutcome SubjectSubstitutionOutcome, wantOrigin SubjectSubstitutionOrigin, wantParent, wantCommitted SubjectRef) {
	t.Helper()
	if event.SubstitutionGuard != wantOutcome {
		t.Errorf("substitution_guard = %q, want %q", event.SubstitutionGuard, wantOutcome)
	}
	if event.SubstitutionOrigin != wantOrigin {
		t.Errorf("substitution_origin = %q, want %q", event.SubstitutionOrigin, wantOrigin)
	}
	if event.SubstitutionParentKind != wantParent.Kind || event.SubstitutionParentValueHash != confirmedNeedValueHash(wantParent.CanonicalID) {
		t.Errorf("parent identity = (%q,%q), want (%q,%q)", event.SubstitutionParentKind, event.SubstitutionParentValueHash, wantParent.Kind, confirmedNeedValueHash(wantParent.CanonicalID))
	}
	if event.SubstitutionCommittedKind != wantCommitted.Kind || event.SubstitutionCommittedValueHash != confirmedNeedValueHash(wantCommitted.CanonicalID) {
		t.Errorf("committed identity = (%q,%q), want (%q,%q)", event.SubstitutionCommittedKind, event.SubstitutionCommittedValueHash, wantCommitted.Kind, confirmedNeedValueHash(wantCommitted.CanonicalID))
	}
}

// assertServedNothing is the invariant itself: the turn committed no
// subject, claimed no fact and read no cohort.
func assertServedNothing(t *testing.T, result InvestigationResult) {
	t.Helper()
	if len(result.SubjectResolution.Committed) != 0 {
		t.Errorf("committed subjects = %d, want 0", len(result.SubjectResolution.Committed))
	}
	if len(result.ClaimedFacts) != 0 {
		t.Errorf("claimed facts = %d, want 0", len(result.ClaimedFacts))
	}
	if result.Cohort != nil {
		t.Errorf("cohort = %+v, want nil", result.Cohort)
	}
	if len(result.SubjectResolution.CommitDecisionDigests) != 0 {
		t.Errorf("commit decision digests = %d, want 0", len(result.SubjectResolution.CommitDecisionDigests))
	}
}

// substitutionTurns runs the two-turn shape at the heart of this axis: turn
// one commits parent, turn two names it and commits child. mutate adapts the
// second request before it runs.
func substitutionTurns(t *testing.T, h *needTurnHarness, idPrefix string, parent, child needTurnResponse, mutate func(*InvestigationRequest)) (needTurnOutcome, needTurnOutcome) {
	t.Helper()
	one := h.turn(needTurnRequest(idPrefix+"_one", true), parent)
	two := continuingNeedTurn(needTurnRequest(idPrefix+"_two", true), one.result.ResultID)
	two.Question = "And how does the second one compare over the same period?"
	if mutate != nil {
		mutate(&two)
	}
	return one, h.turn(two, child)
}

// TestSubstitutionGuardClarifiesAResolverOriginSubjectChange is the
// acceptance cell: a follow-up that names the parent, carries NO hint and no
// receipt, and whose own resolution commits a different repository, is not
// served. The remembered subject is offered first.
func TestSubstitutionGuardClarifiesAResolverOriginSubjectChange(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	_, two := substitutionTurns(t, h, "request_5917_resolver",
		substitutionResponse(substitutionRepoOne, "receipt_5917_one"),
		substitutionResponse(substitutionRepoTwo, "receipt_5917_two"), nil)
	if two.result.Status != InvestigationClarificationRequired {
		t.Fatalf("status = %q, want %q", two.result.Status, InvestigationClarificationRequired)
	}
	assertServedNothing(t, two.result)
	candidates := two.result.SubjectResolution.Candidates
	if len(candidates) != 2 {
		t.Fatalf("candidates = %d, want 2 (remembered plus this turn's own)", len(candidates))
	}
	if candidates[0].Subject.CanonicalID != substitutionRepoOne.CanonicalID {
		t.Errorf("first candidate = %q, want the remembered %q", candidates[0].Subject.CanonicalID, substitutionRepoOne.CanonicalID)
	}
	if candidates[1].Subject.CanonicalID != substitutionRepoTwo.CanonicalID {
		t.Errorf("second candidate = %q, want this turn's own %q", candidates[1].Subject.CanonicalID, substitutionRepoTwo.CanonicalID)
	}
	for i, candidate := range candidates {
		if candidate.State == contractsv1.ContextFabricResolutionCommitted {
			t.Errorf("candidate %d state = committed beside an empty committed list", i)
		}
	}
	assertGuard(t, lastSubstitution(t, two), SubjectSubstitutionClarified, SubjectSubstitutionOriginResolver, substitutionRepoOne, substitutionRepoTwo)
}

// TestSubstitutionGuardClarifiesAHintOriginSubjectChange is the same cell
// reached from the caller's own hint channel. Enumerated separately, decided
// identically.
func TestSubstitutionGuardClarifiesAHintOriginSubjectChange(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	_, two := substitutionTurns(t, h, "request_5917_hint",
		substitutionResponse(substitutionRepoOne, "receipt_5917_hint_one"),
		substitutionResponse(substitutionRepoTwo, "receipt_5917_hint_two"),
		func(request *InvestigationRequest) {
			request.RequestedScope.SubjectHints = []SubjectHint{{
				Kind: substitutionRepoTwo.Kind, ID: substitutionRepoTwo.CanonicalID,
				Label: substitutionRepoTwo.Label, Source: "caller",
			}}
		})
	if two.result.Status != InvestigationClarificationRequired {
		t.Fatalf("status = %q, want %q", two.result.Status, InvestigationClarificationRequired)
	}
	assertServedNothing(t, two.result)
	assertGuard(t, lastSubstitution(t, two), SubjectSubstitutionClarified, SubjectSubstitutionOriginCallerHint, substitutionRepoOne, substitutionRepoTwo)
}

// TestSubstitutionGuardClarifiesAReceiptFromAnotherResult: a redeemed
// receipt minted by some OTHER result is a hint like any other. It says
// nothing about the exchange this turn continues, so it does not license the
// change.
func TestSubstitutionGuardClarifiesAReceiptFromAnotherResult(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	stranger := h.turn(needTurnRequest("request_5917_stranger", true), substitutionResponse(substitutionRepoTwo, "receipt_5917_stranger"))
	one := h.turn(needTurnRequest("request_5917_receipt_one", true), substitutionResponse(substitutionRepoOne, "receipt_5917_recv_one"))
	two := continuingNeedTurn(needTurnRequest("request_5917_receipt_two", true), one.result.ResultID)
	two.Question = "And how does the second one compare over the same period?"
	two.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: stranger.result.ResultID, ReceiptID: "receipt_5917_stranger"}}
	outcome := h.turn(two, substitutionResponse(substitutionRepoTwo, "receipt_5917_recv_two"))
	if outcome.result.Status != InvestigationClarificationRequired {
		t.Fatalf("status = %q, want %q", outcome.result.Status, InvestigationClarificationRequired)
	}
	assertServedNothing(t, outcome.result)
	assertGuard(t, lastSubstitution(t, outcome), SubjectSubstitutionClarified, SubjectSubstitutionOriginPriorReceipt, substitutionRepoOne, substitutionRepoTwo)
}

// TestSubstitutionGuardServesAChoiceRedeemedFromTheNamedParent is the one
// permitted substitution, and the reason the clarification above can ever be
// answered: the caller redeems an offer the named parent itself minted.
func TestSubstitutionGuardServesAChoiceRedeemedFromTheNamedParent(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	one := h.turn(needTurnRequest("request_5917_choice_one", true), needTurnResponse{
		resolution: SubjectResolution{
			Committed: []SubjectRef{substitutionRepoOne},
			Candidates: []SubjectCandidate{
				{ReceiptID: "receipt_5917_choice_a", Subject: substitutionRepoOne, State: contractsv1.ContextFabricResolutionCommitted, MatchedTerms: []string{"service"}, MatchReasons: []string{"matched"}, Confidence: 1, EvidenceRefIDs: []string{}},
				{ReceiptID: "receipt_5917_choice_b", Subject: substitutionRepoTwo, State: contractsv1.ContextFabricResolutionAmbiguous, MatchedTerms: []string{"service"}, MatchReasons: []string{"matched"}, Confidence: 0.4, EvidenceRefIDs: []string{}},
			},
		},
		bases: func() CommitBasisSet {
			bases := CommitBasisSet{}
			bases.Record(substitutionRepoOne, CommitBasisAuthoritativeIdentity)
			return bases
		}(),
	})
	two := continuingNeedTurn(needTurnRequest("request_5917_choice_two", true), one.result.ResultID)
	two.Question = "And how does the second one compare over the same period?"
	two.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: one.result.ResultID, ReceiptID: "receipt_5917_choice_b"}}
	outcome := h.turn(two, substitutionResponse(substitutionRepoTwo, "receipt_5917_choice_served"))
	if outcome.result.Status == InvestigationClarificationRequired {
		t.Fatalf("status = %q, want the chosen subject served", outcome.result.Status)
	}
	if len(outcome.result.SubjectResolution.Committed) != 1 || outcome.result.SubjectResolution.Committed[0].CanonicalID != substitutionRepoTwo.CanonicalID {
		t.Fatalf("committed = %+v, want the chosen %q", outcome.result.SubjectResolution.Committed, substitutionRepoTwo.CanonicalID)
	}
	// The re-read decides what may be LISTED, and this turn lists nothing:
	// a served turn never pays for a keyed graph call it cannot use.
	if len(h.candidates) != 0 {
		t.Errorf("candidate verifier calls = %d on a served redeemed choice, want 0", len(h.candidates))
	}
	assertGuard(t, lastSubstitution(t, outcome), SubjectSubstitutionRedeemedChoice, SubjectSubstitutionOriginPriorReceipt, substitutionRepoOne, substitutionRepoTwo)
}

// TestSubstitutionGuardRefusesWhenTheCallerCannotBeAsked: a caller that
// declined clarification is told why, and is still not served the other
// subject.
func TestSubstitutionGuardRefusesWhenTheCallerCannotBeAsked(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	_, two := substitutionTurns(t, h, "request_5917_refuse",
		substitutionResponse(substitutionRepoOne, "receipt_5917_refuse_one"),
		substitutionResponse(substitutionRepoTwo, "receipt_5917_refuse_two"),
		func(request *InvestigationRequest) { request.Options.AllowClarification = false })
	if two.result.Status != InvestigationNoMatch {
		t.Fatalf("status = %q, want %q", two.result.Status, InvestigationNoMatch)
	}
	assertServedNothing(t, two.result)
	if len(h.candidates) != 1 {
		t.Errorf("candidate verifier calls = %d, want 1: the remembered subject is re-authorized before it is listed", len(h.candidates))
	}
	candidates := two.result.SubjectResolution.Candidates
	if len(candidates) != 2 || candidates[0].Subject.CanonicalID != substitutionRepoOne.CanonicalID {
		t.Fatalf("candidates = %+v, want both identities with the remembered one first", candidates)
	}
	assertGuard(t, lastSubstitution(t, two), SubjectSubstitutionRefused, SubjectSubstitutionOriginResolver, substitutionRepoOne, substitutionRepoTwo)
}

// TestSubstitutionGuardWithholdsAnUnreadableRememberedSubject: the
// remembered subject no longer re-reads and re-authorizes, so it is not
// offered -- and the substitute is still not served.
func TestSubstitutionGuardWithholdsAnUnreadableRememberedSubject(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	one := h.turn(needTurnRequest("request_5917_gone_one", true), substitutionResponse(substitutionRepoOne, "receipt_5917_gone_one"))
	h.refuseCandidates = true
	two := continuingNeedTurn(needTurnRequest("request_5917_gone_two", true), one.result.ResultID)
	two.Question = "And how does the second one compare over the same period?"
	outcome := h.turn(two, substitutionResponse(substitutionRepoTwo, "receipt_5917_gone_two"))
	assertServedNothing(t, outcome.result)
	for _, candidate := range outcome.result.SubjectResolution.Candidates {
		if candidate.Subject.CanonicalID == substitutionRepoOne.CanonicalID {
			t.Errorf("the remembered subject was offered although it no longer re-reads for this principal")
		}
	}
	assertGuard(t, lastSubstitution(t, outcome), SubjectSubstitutionClarifiedRememberedUnavailable, SubjectSubstitutionOriginResolver, substitutionRepoOne, substitutionRepoTwo)
}

// TestSubstitutionGuardWithholdsWhenNoVerifierIsWired: a deployment with no
// candidate verifier cannot prove the remembered subject is still readable,
// so it withholds the offer -- and still refuses to serve the substitute.
func TestSubstitutionGuardWithholdsWhenNoVerifierIsWired(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil, withoutNeedVerifiers())
	_, two := substitutionTurns(t, h, "request_5917_unwired",
		substitutionResponse(substitutionRepoOne, "receipt_5917_unwired_one"),
		substitutionResponse(substitutionRepoTwo, "receipt_5917_unwired_two"), nil)
	assertServedNothing(t, two.result)
	assertGuard(t, lastSubstitution(t, two), SubjectSubstitutionClarifiedRememberedUnavailable, SubjectSubstitutionOriginResolver, substitutionRepoOne, substitutionRepoTwo)
}

// TestSubstitutionGuardClarifiesACrossKindSubjectChange: a follow-up whose
// own resolution commits a subject of a DIFFERENT kind is still a different
// subject.
func TestSubstitutionGuardClarifiesACrossKindSubjectChange(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	_, two := substitutionTurns(t, h, "request_5917_crosskind",
		substitutionResponse(substitutionRepoOne, "receipt_5917_ck_one"),
		substitutionResponse(substitutionOtherKind, "receipt_5917_ck_two"), nil)
	assertServedNothing(t, two.result)
	assertGuard(t, lastSubstitution(t, two), SubjectSubstitutionClarified, SubjectSubstitutionOriginResolver, substitutionRepoOne, substitutionOtherKind)
}

// TestSubstitutionGuardServesTheSameSubjectUnchanged: the ordinary
// continuation. Nothing about it changes.
func TestSubstitutionGuardServesTheSameSubjectUnchanged(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	_, two := substitutionTurns(t, h, "request_5917_same",
		substitutionResponse(substitutionRepoOne, "receipt_5917_same_one"),
		substitutionResponse(substitutionRepoOne, "receipt_5917_same_two"), nil)
	if two.result.Status == InvestigationClarificationRequired {
		t.Fatalf("status = %q, want the same subject served", two.result.Status)
	}
	if len(two.result.SubjectResolution.Committed) != 1 {
		t.Fatalf("committed = %d, want 1", len(two.result.SubjectResolution.Committed))
	}
	assertGuard(t, lastSubstitution(t, two), SubjectSubstitutionSameSubject, SubjectSubstitutionOriginResolver, substitutionRepoOne, substitutionRepoOne)
}

// TestSubstitutionGuardReportsNoParentReference: a first turn has nothing to
// contradict.
func TestSubstitutionGuardReportsNoParentReference(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	one := h.turn(needTurnRequest("request_5917_root", true), substitutionResponse(substitutionRepoOne, "receipt_5917_root"))
	if len(one.result.SubjectResolution.Committed) != 1 {
		t.Fatalf("committed = %d, want 1", len(one.result.SubjectResolution.Committed))
	}
	assertGuard(t, lastSubstitution(t, one), SubjectSubstitutionNoParentReference, SubjectSubstitutionOriginResolver, SubjectRef{}, substitutionRepoOne)
}

// TestSubstitutionGuardReportsAnUnreadableParent: a named parent that does
// not read is a different fact from a parent that held nothing, and the turn
// is served rather than refused for evidence the guard never had.
func TestSubstitutionGuardReportsAnUnreadableParent(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	request := continuingNeedTurn(needTurnRequest("request_5917_missing_parent", true), "result_need_turn_9999")
	request.Question = "And how does the second one compare over the same period?"
	outcome := h.turn(request, substitutionResponse(substitutionRepoTwo, "receipt_5917_missing"))
	if len(outcome.result.SubjectResolution.Committed) != 1 {
		t.Fatalf("committed = %d, want 1: an unreadable parent never refuses a turn", len(outcome.result.SubjectResolution.Committed))
	}
	assertGuard(t, lastSubstitution(t, outcome), SubjectSubstitutionParentUnreadable, SubjectSubstitutionOriginResolver, SubjectRef{}, substitutionRepoTwo)
}

// TestSubstitutionGuardReportsAParentThatHeldNoIdentity: a parent that
// committed nothing asserted nothing.
func TestSubstitutionGuardReportsAParentThatHeldNoIdentity(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	_, two := substitutionTurns(t, h, "request_5917_parentless",
		substitutionEmptyResponse(),
		substitutionResponse(substitutionRepoTwo, "receipt_5917_parentless_two"), nil)
	if len(two.result.SubjectResolution.Committed) != 1 {
		t.Fatalf("committed = %d, want 1", len(two.result.SubjectResolution.Committed))
	}
	assertGuard(t, lastSubstitution(t, two), SubjectSubstitutionParentNoIdentity, SubjectSubstitutionOriginResolver, SubjectRef{}, substitutionRepoTwo)
}

// TestSubstitutionGuardReportsACohortParentAsNoIdentity: a parent that
// committed two subjects names no single identity, and the guard reports
// that rather than picking one by slice order.
func TestSubstitutionGuardReportsACohortParentAsNoIdentity(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	_, two := substitutionTurns(t, h, "request_5917_cohort_parent",
		substitutionCohortResponse(),
		substitutionResponse(substitutionOtherKind, "receipt_5917_cohort_two"), nil)
	if len(two.result.SubjectResolution.Committed) != 1 {
		t.Fatalf("committed = %d, want 1", len(two.result.SubjectResolution.Committed))
	}
	assertGuard(t, lastSubstitution(t, two), SubjectSubstitutionParentNoIdentity, SubjectSubstitutionOriginResolver, SubjectRef{}, substitutionOtherKind)
}

// TestSubstitutionGuardReportsATurnThatCommitsNothing: nothing is being
// served about a different subject, because nothing is being served about a
// subject at all.
func TestSubstitutionGuardReportsATurnThatCommitsNothing(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	_, two := substitutionTurns(t, h, "request_5917_nocommit",
		substitutionResponse(substitutionRepoOne, "receipt_5917_nocommit_one"),
		substitutionEmptyResponse(), nil)
	assertGuard(t, lastSubstitution(t, two), SubjectSubstitutionNoCommittedSubject, SubjectSubstitutionOriginNotApplicable, substitutionRepoOne, SubjectRef{})
}

// TestSubstitutionGuardReportsAnAmbiguousTurnAsNoCommittedSubject: two
// committed subjects name no single identity this turn either, so the guard
// leaves the existing ambiguity machinery to describe it.
func TestSubstitutionGuardReportsAnAmbiguousTurnAsNoCommittedSubject(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	_, two := substitutionTurns(t, h, "request_5917_ambiguous",
		substitutionResponse(substitutionOtherKind, "receipt_5917_amb_one"),
		substitutionCohortResponse(), nil)
	assertGuard(t, lastSubstitution(t, two), SubjectSubstitutionNoCommittedSubject, SubjectSubstitutionOriginNotApplicable, substitutionOtherKind, SubjectRef{})
}

// TestSubstitutionGuardIsNotEvaluatedOnATurnThatEndsFirst: the window gate
// ends the turn above the guard's own decision point, and the line says
// exactly that rather than reading as a guard that ran and found nothing.
func TestSubstitutionGuardIsNotEvaluatedOnATurnThatEndsFirst(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	one := h.turn(needTurnRequest("request_5917_gated_one", true), substitutionResponse(substitutionRepoOne, "receipt_5917_gated_one"))
	two := continuingNeedTurn(needTurnRequest("request_5917_gated_two", false), one.result.ResultID)
	two.Question = "And how does the second one compare over the same period?"
	outcome := h.turn(two, substitutionResponse(substitutionRepoTwo, "receipt_5917_gated_two"))
	assertGuard(t, lastSubstitution(t, outcome), SubjectSubstitutionNotEvaluated, SubjectSubstitutionOriginNotApplicable, SubjectRef{}, SubjectRef{})
}

// TestSubstitutionGuardWithholdsTheOfferOnACancelledContext: the re-read
// cannot be completed, so the offer is withheld -- and the substitute is
// still not served. The cancellation is observed, never swallowed into a
// "this subject is gone" claim.
func TestSubstitutionGuardWithholdsTheOfferOnACancelledContext(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if h.engine.rememberedSubjectReadable(ctx, acceptancePrincipal(), needTurnRequest("request_5917_cancelled", true), ResolvedGraphBinding{GraphKey: "need-turn-key"}, substitutionRepoOne) {
		t.Fatal("rememberedSubjectReadable() = true on a cancelled context, want false")
	}
	if len(h.candidates) != 0 {
		t.Errorf("candidate verifier calls = %d on a cancelled context, want 0", len(h.candidates))
	}
}

// TestSubstitutionGuardEmitsEveryFieldAtProductionInfo drives the real
// producer through the production JSON sink and reads the emitted line, so
// the six fields are proven present with their real values rather than
// asserted over a struct literal.
func TestSubstitutionGuardEmitsEveryFieldAtProductionInfo(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	one := h.turn(needTurnRequest("request_5917_emit_one", true), substitutionResponse(substitutionRepoOne, "receipt_5917_emit_one"))
	buf := swapToJSONLedgerTelemetry(h)
	two := continuingNeedTurn(needTurnRequest("request_5917_emit_two", true), one.result.ResultID)
	two.Question = "And how does the second one compare over the same period?"
	h.graph.response = substitutionResponse(substitutionRepoTwo, "receipt_5917_emit_two")
	if _, err := h.engine.Investigate(context.Background(), acceptancePrincipal(), two); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	line := lastLedgerJSONLine(t, buf)
	want := map[string]string{
		"substitution_guard":                string(SubjectSubstitutionClarified),
		"substitution_origin":               string(SubjectSubstitutionOriginResolver),
		"substitution_parent_kind":          string(substitutionRepoOne.Kind),
		"substitution_parent_value_hash":    confirmedNeedValueHash(substitutionRepoOne.CanonicalID),
		"substitution_committed_kind":       string(substitutionRepoTwo.Kind),
		"substitution_committed_value_hash": confirmedNeedValueHash(substitutionRepoTwo.CanonicalID),
	}
	for key, value := range want {
		got, ok := line[key]
		if !ok {
			t.Errorf("emitted line is missing %q", key)
			continue
		}
		if got != value {
			t.Errorf("%s = %v, want %q", key, got, value)
		}
	}
	if want["substitution_parent_value_hash"] == want["substitution_committed_value_hash"] {
		t.Fatal("fixture defect: the two hashed identities coincide, so neither is pinned")
	}
}

// lastLedgerJSONLine returns the LAST confirmed-need-ledger line the
// production JSON sink wrote, decoded.
func lastLedgerJSONLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var found map[string]any
	for _, raw := range bytes.Split(bytes.TrimRight(buf.Bytes(), "\n"), []byte("\n")) {
		if len(raw) == 0 {
			continue
		}
		var line map[string]any
		if err := json.Unmarshal(raw, &line); err != nil {
			t.Fatalf("production line is not JSON: %v", err)
		}
		if line["msg"] == "context fabric confirmed need ledger" {
			found = line
		}
	}
	if found == nil {
		t.Fatal("no confirmed-need-ledger line was emitted at production Info level")
	}
	return found
}

// TestDecideSubjectSubstitutionOverTheWholeInputSpace is the pure decision's
// own table: every parent state crossed with every this-turn state, every
// evidence class and both caller capabilities.
func TestDecideSubjectSubstitutionOverTheWholeInputSpace(t *testing.T) {
	t.Parallel()
	held := parentAnchorEvidence{Referenced: true, Loaded: true, Subject: substitutionRepoOne}
	cases := []struct {
		name  string
		in    subjectSubstitutionInput
		want  SubjectSubstitutionOutcome
		fired bool
	}{
		{"parent absent", subjectSubstitutionInput{HaveCommitted: true, Committed: substitutionRepoTwo, AllowClarification: true}, SubjectSubstitutionNoParentReference, false},
		{"parent unloadable", subjectSubstitutionInput{Parent: parentAnchorEvidence{Referenced: true}, HaveCommitted: true, Committed: substitutionRepoTwo, AllowClarification: true}, SubjectSubstitutionParentUnreadable, false},
		{"parent held nothing", subjectSubstitutionInput{Parent: parentAnchorEvidence{Referenced: true, Loaded: true}, HaveCommitted: true, Committed: substitutionRepoTwo, AllowClarification: true}, SubjectSubstitutionParentNoIdentity, false},
		{"parent subject missing a kind", subjectSubstitutionInput{Parent: parentAnchorEvidence{Referenced: true, Loaded: true, Subject: SubjectRef{CanonicalID: "repository:alpha-service"}}, HaveCommitted: true, Committed: substitutionRepoTwo, AllowClarification: true}, SubjectSubstitutionParentNoIdentity, false},
		{"this turn committed nothing", subjectSubstitutionInput{Parent: held, AllowClarification: true}, SubjectSubstitutionNoCommittedSubject, false},
		{"same identity", subjectSubstitutionInput{Parent: held, HaveCommitted: true, Committed: substitutionRepoOne, AllowClarification: true, RememberedAvailable: true}, SubjectSubstitutionSameSubject, false},
		{"different identity same kind", subjectSubstitutionInput{Parent: held, HaveCommitted: true, Committed: substitutionRepoTwo, AllowClarification: true, RememberedAvailable: true}, SubjectSubstitutionClarified, true},
		{"different kind", subjectSubstitutionInput{Parent: held, HaveCommitted: true, Committed: substitutionOtherKind, AllowClarification: true, RememberedAvailable: true}, SubjectSubstitutionClarified, true},
		{"different identity, remembered unreadable", subjectSubstitutionInput{Parent: held, HaveCommitted: true, Committed: substitutionRepoTwo, AllowClarification: true}, SubjectSubstitutionClarifiedRememberedUnavailable, true},
		{"different identity, caller cannot be asked", subjectSubstitutionInput{Parent: held, HaveCommitted: true, Committed: substitutionRepoTwo, RememberedAvailable: true}, SubjectSubstitutionRefused, true},
		{"different identity, caller cannot be asked, remembered unreadable", subjectSubstitutionInput{Parent: held, HaveCommitted: true, Committed: substitutionRepoTwo}, SubjectSubstitutionRefusedRememberedUnavailable, true},
		{"redeemed choice beats clarification", subjectSubstitutionInput{Parent: held, HaveCommitted: true, Committed: substitutionRepoTwo, RedeemedChoice: true, AllowClarification: true, RememberedAvailable: true}, SubjectSubstitutionRedeemedChoice, false},
		{"redeemed choice beats refusal", subjectSubstitutionInput{Parent: held, HaveCommitted: true, Committed: substitutionRepoTwo, RedeemedChoice: true}, SubjectSubstitutionRedeemedChoice, false},
		{"same identity is never a subject change", subjectSubstitutionInput{Parent: held, HaveCommitted: true, Committed: substitutionRepoOne, RedeemedChoice: true, AllowClarification: true}, SubjectSubstitutionSameSubject, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := decideSubjectSubstitution(tc.in)
			if got.Outcome != tc.want {
				t.Fatalf("outcome = %q, want %q", got.Outcome, tc.want)
			}
			if got.Outcome.Fired() != tc.fired {
				t.Errorf("Fired() = %t, want %t", got.Outcome.Fired(), tc.fired)
			}
			if !ValidSubjectSubstitutionOutcome(got.Outcome) {
				t.Errorf("outcome %q is outside the closed vocabulary", got.Outcome)
			}
			if !ValidSubjectSubstitutionOrigin(got.Origin) {
				t.Errorf("origin %q is outside the closed vocabulary", got.Origin)
			}
		})
	}
}

// TestDecideSubjectSubstitutionReportsEveryOriginWithoutConsultingIt pins
// the split this guard turns on: origin travels onto the decision and never
// changes it.
func TestDecideSubjectSubstitutionReportsEveryOriginWithoutConsultingIt(t *testing.T) {
	t.Parallel()
	held := parentAnchorEvidence{Referenced: true, Loaded: true, Subject: substitutionRepoOne}
	for _, origin := range SubjectSubstitutionOriginVocabulary() {
		origin := origin
		t.Run(string(origin), func(t *testing.T) {
			t.Parallel()
			got := decideSubjectSubstitution(subjectSubstitutionInput{
				Parent: held, HaveCommitted: true, Committed: substitutionRepoTwo,
				Origin: origin, AllowClarification: true, RememberedAvailable: true,
			})
			if got.Outcome != SubjectSubstitutionClarified {
				t.Fatalf("outcome for origin %q = %q, want %q", origin, got.Outcome, SubjectSubstitutionClarified)
			}
			if got.Origin != origin {
				t.Errorf("origin = %q, want %q reported unchanged", got.Origin, origin)
			}
		})
	}
}

// TestDecideSubjectSubstitutionDefaultsAnUnrecordedOriginToTheResolver: an
// origin nothing recorded reads as the strict one, the population this guard
// was added for.
func TestDecideSubjectSubstitutionDefaultsAnUnrecordedOriginToTheResolver(t *testing.T) {
	t.Parallel()
	got := decideSubjectSubstitution(subjectSubstitutionInput{
		Parent:        parentAnchorEvidence{Referenced: true, Loaded: true, Subject: substitutionRepoOne},
		HaveCommitted: true, Committed: substitutionRepoTwo, Origin: "invented_by_a_caller",
		AllowClarification: true, RememberedAvailable: true,
	})
	if got.Origin != SubjectSubstitutionOriginResolver {
		t.Fatalf("origin = %q, want %q", got.Origin, SubjectSubstitutionOriginResolver)
	}
}

// TestSubjectSubstitutionOriginOfReadsReceiptsBeforeCallerHints: the two
// channels share one slice by the time resolution sees them, so a redemption
// must not be reported as the caller's own hint.
func TestSubjectSubstitutionOriginOfReadsReceiptsBeforeCallerHints(t *testing.T) {
	t.Parallel()
	hint := SubjectHint{Kind: substitutionRepoTwo.Kind, ID: substitutionRepoTwo.CanonicalID, Label: substitutionRepoTwo.Label, Source: "prior_subject_receipt"}
	carried := map[contractsv1.ContextFabricStructureNeedKind]confirmedStructureMember{
		contractsv1.ContextFabricStructureNeedSubjectAnchor: {
			Member:      contractsv1.ContextFabricStructureNeedSubjectAnchor,
			AppliedKind: substitutionRepoTwo.Kind, AppliedValue: substitutionRepoTwo.CanonicalID,
			Basis: ConfirmedNeedBasisEngineCommitted,
		},
	}
	if got := subjectSubstitutionOriginOf(substitutionRepoTwo, []SubjectHint{hint}, []SubjectHint{hint}, carried); got != SubjectSubstitutionOriginPriorReceipt {
		t.Errorf("origin = %q, want %q", got, SubjectSubstitutionOriginPriorReceipt)
	}
	if got := subjectSubstitutionOriginOf(substitutionRepoTwo, nil, []SubjectHint{hint}, carried); got != SubjectSubstitutionOriginCallerHint {
		t.Errorf("origin = %q, want %q", got, SubjectSubstitutionOriginCallerHint)
	}
	if got := subjectSubstitutionOriginOf(substitutionRepoTwo, nil, nil, carried); got != SubjectSubstitutionOriginEngineCarry {
		t.Errorf("origin = %q, want %q", got, SubjectSubstitutionOriginEngineCarry)
	}
	if got := subjectSubstitutionOriginOf(substitutionRepoTwo, nil, nil, nil); got != SubjectSubstitutionOriginResolver {
		t.Errorf("origin = %q, want %q", got, SubjectSubstitutionOriginResolver)
	}
	other := SubjectHint{Kind: substitutionRepoOne.Kind, ID: substitutionRepoOne.CanonicalID, Label: substitutionRepoOne.Label, Source: "caller"}
	otherCarried := map[contractsv1.ContextFabricStructureNeedKind]confirmedStructureMember{
		contractsv1.ContextFabricStructureNeedSubjectAnchor: {
			Member:      contractsv1.ContextFabricStructureNeedSubjectAnchor,
			AppliedKind: substitutionRepoOne.Kind, AppliedValue: substitutionRepoOne.CanonicalID,
		},
	}
	if got := subjectSubstitutionOriginOf(substitutionRepoTwo, []SubjectHint{other}, []SubjectHint{other}, otherCarried); got != SubjectSubstitutionOriginResolver {
		t.Errorf("origin for channels naming another subject = %q, want %q", got, SubjectSubstitutionOriginResolver)
	}
}

// TestSubstitutionGuardReportsTheEngineCarryOrigin drives the CHAOS-5788
// carry rig: turn one commits an anchor the engine captures, turn two asks
// the SAME question, the ledger admits, and the carried entry is what names
// the identity resolution then commits. The line reports that channel by
// name rather than collapsing it into the resolver's.
func TestSubstitutionGuardReportsTheEngineCarryOrigin(t *testing.T) {
	t.Parallel()
	e, g, store, telemetry := buildCommittedAnchorEngineWithTelemetry(t)
	one := needTurnRequest("request_5917_carry_one", true)
	parent, _ := committedAnchorTurn(t, e, g, store, one, reviewCarryIdentityResponse(committedAnchorRepo))
	two := continuingNeedTurn(needTurnRequest("request_5917_carry_two", true), parent.ResultID)
	mark := len(telemetry.confirmedNeedLedgers)
	committedAnchorTurn(t, e, g, store, two, reviewCarryIdentityResponse(committedAnchorRepo))
	lines := telemetry.confirmedNeedLedgers[mark:]
	if len(lines) != 1 {
		t.Fatalf("confirmed-need-ledger lines = %d, want 1", len(lines))
	}
	if lines[0].SubstitutionOrigin != SubjectSubstitutionOriginEngineCarry {
		t.Fatalf("substitution_origin = %q, want %q", lines[0].SubstitutionOrigin, SubjectSubstitutionOriginEngineCarry)
	}
	if lines[0].SubstitutionGuard != SubjectSubstitutionSameSubject {
		t.Errorf("substitution_guard = %q, want %q", lines[0].SubstitutionGuard, SubjectSubstitutionSameSubject)
	}
}

// reviewCarryIdentityResponse commits subject on a proven identity, matching
// the counting frame's own anchor term, so the capture gate records it.
func reviewCarryIdentityResponse(s SubjectRef) needTurnResponse {
	bases := CommitBasisSet{}
	bases.Record(s, CommitBasisAuthoritativeIdentity)
	return needTurnResponse{resolution: SubjectResolution{
		Committed: []SubjectRef{s}, Candidates: []SubjectCandidate{{
			ReceiptID: "receipt_5917_carry", Subject: s, State: contractsv1.ContextFabricResolutionCommitted,
			MatchedTerms: []string{"a"}, MatchReasons: []string{"matched"}, Confidence: 1,
			EvidenceRefIDs: []string{},
		}},
	}, bases: bases}
}

// TestSubjectSubstitutionRedeemedChoiceRequiresTheNamedParentsOwnReceipt.
func TestSubjectSubstitutionRedeemedChoiceRequiresTheNamedParentsOwnReceipt(t *testing.T) {
	t.Parallel()
	hint := SubjectHint{Kind: substitutionRepoTwo.Kind, ID: substitutionRepoTwo.CanonicalID, Label: substitutionRepoTwo.Label, Source: "prior_subject_receipt"}
	parentReceipt := []BoundSubjectReceipt{{ResultID: "result_parent_0001", ReceiptID: "receipt_x"}}
	strangerReceipt := []BoundSubjectReceipt{{ResultID: "result_other_0002", ReceiptID: "receipt_y"}}
	if !subjectSubstitutionRedeemedChoice(substitutionRepoTwo, "result_parent_0001", parentReceipt, []SubjectHint{hint}) {
		t.Error("a receipt minted by the named parent is an explicit choice")
	}
	if subjectSubstitutionRedeemedChoice(substitutionRepoTwo, "result_parent_0001", strangerReceipt, []SubjectHint{hint}) {
		t.Error("a receipt from another result is not an explicit choice")
	}
	if subjectSubstitutionRedeemedChoice(substitutionRepoTwo, "", parentReceipt, []SubjectHint{hint}) {
		t.Error("no named parent cannot make a choice explicit")
	}
	if subjectSubstitutionRedeemedChoice(substitutionRepoTwo, "result_parent_0001", parentReceipt, nil) {
		t.Error("a validated receipt that produced no hint for this subject is not a choice of it")
	}
	if subjectSubstitutionRedeemedChoice(substitutionRepoTwo, "result_parent_0001", nil, []SubjectHint{hint}) {
		t.Error("a hint with no validated receipt behind it is not a choice")
	}
}

// TestCommittedSubjectIdentityOfReadsTheAnchorThenTheSoleCommit.
func TestCommittedSubjectIdentityOfReadsTheAnchorThenTheSoleCommit(t *testing.T) {
	t.Parallel()
	anchor := confirmedStructureMember{Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedKind: substitutionRepoOne.Kind, AppliedValue: substitutionRepoOne.CanonicalID}
	both := SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{substitutionRepoTwo, substitutionRepoOne}}
	got, ok := committedSubjectIdentityOf(anchor, true, both)
	if !ok || got.CanonicalID != substitutionRepoOne.CanonicalID {
		t.Errorf("anchor branch = (%+v,%t), want the anchor subject", got, ok)
	}
	if _, ok := committedSubjectIdentityOf(anchor, true, SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{substitutionRepoTwo}}); ok {
		t.Error("an anchor absent from the served commit list reports no identity")
	}
	sole := SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{substitutionRepoTwo}}
	if got, ok := committedSubjectIdentityOf(confirmedStructureMember{}, false, sole); !ok || got.CanonicalID != substitutionRepoTwo.CanonicalID {
		t.Errorf("sole-commit branch = (%+v,%t), want the sole committed subject", got, ok)
	}
	if _, ok := committedSubjectIdentityOf(confirmedStructureMember{}, false, both); ok {
		t.Error("two committed subjects name no single identity")
	}
	if _, ok := committedSubjectIdentityOf(confirmedStructureMember{}, false, SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}); ok {
		t.Error("no committed subject names no identity")
	}
}

// TestSubjectSubstitutionResolutionOffersTheRememberedSubjectFirst pins the
// served shape: order is the offer, nothing stays committed, and this turn's
// own candidates survive.
func TestSubjectSubstitutionResolutionOffersTheRememberedSubjectFirst(t *testing.T) {
	t.Parallel()
	original := SubjectResolution{
		Committed: []SubjectRef{substitutionRepoTwo},
		Candidates: []SubjectCandidate{{
			ReceiptID: "receipt_original", Subject: substitutionRepoTwo, State: contractsv1.ContextFabricResolutionCommitted,
			MatchedTerms: []string{"service"}, MatchReasons: []string{"matched"}, Confidence: 1, EvidenceRefIDs: []string{},
		}},
		CommitDecisionDigests: []contractsv1.ContextFabricCommitDecisionDigest{{Subject: substitutionRepoTwo, CommitGate: "identity_fast_path", IdentityProven: true}},
	}
	decision := subjectSubstitutionDecision{Outcome: SubjectSubstitutionClarified, Parent: substitutionRepoOne, Substituted: substitutionRepoTwo, RememberedListed: true}
	guarded := subjectSubstitutionResolution(original, decision, "result_parent_0001")
	if len(guarded.Committed) != 0 || guarded.CommitDecisionDigests != nil {
		t.Fatalf("guarded resolution still commits: %+v", guarded)
	}
	if len(guarded.Candidates) != 2 || guarded.Candidates[0].Subject.CanonicalID != substitutionRepoOne.CanonicalID {
		t.Fatalf("candidates = %+v, want the remembered subject first", guarded.Candidates)
	}
	if guarded.Candidates[1].State != contractsv1.ContextFabricResolutionAmbiguous {
		t.Errorf("this turn's own candidate state = %q, want it re-stated beside an empty commit list", guarded.Candidates[1].State)
	}
	if err := guarded.Validate(); err != nil {
		t.Fatalf("guarded resolution violates the wire contract: %v", err)
	}
	withheld := subjectSubstitutionResolution(original, subjectSubstitutionDecision{Outcome: SubjectSubstitutionClarifiedRememberedUnavailable, Parent: substitutionRepoOne, Substituted: substitutionRepoTwo}, "result_parent_0001")
	if len(withheld.Candidates) != 1 {
		t.Fatalf("withheld candidates = %d, want only this turn's own", len(withheld.Candidates))
	}
	if withheld.ClarificationPrompt == guarded.ClarificationPrompt {
		t.Error("a withheld remembered subject must not be described as something to pick")
	}
	if err := withheld.Validate(); err != nil {
		t.Fatalf("withheld resolution violates the wire contract: %v", err)
	}
}

// TestSubjectSubstitutionReceiptIDIsDeterministicAndUnique.
func TestSubjectSubstitutionReceiptIDIsDeterministicAndUnique(t *testing.T) {
	t.Parallel()
	first := subjectSubstitutionReceiptID("result_parent_0001", substitutionRepoOne)
	if first != subjectSubstitutionReceiptID("result_parent_0001", substitutionRepoOne) {
		t.Error("the same parent and subject must mint the same receipt id")
	}
	if first == subjectSubstitutionReceiptID("result_parent_0001", substitutionRepoTwo) {
		t.Error("two subjects must never share a receipt id")
	}
	if first == subjectSubstitutionReceiptID("result_parent_0002", substitutionRepoOne) {
		t.Error("two parents must never share a receipt id")
	}
	if len(first) < 8 {
		t.Errorf("receipt id length = %d, below the v1 bound", len(first))
	}
}

// TestSubjectSubstitutionVocabulariesAreClosedAndDistinct.
func TestSubjectSubstitutionVocabulariesAreClosedAndDistinct(t *testing.T) {
	t.Parallel()
	seen := map[SubjectSubstitutionOutcome]bool{}
	for _, member := range SubjectSubstitutionOutcomeVocabulary() {
		if seen[member] {
			t.Errorf("outcome %q is declared twice", member)
		}
		seen[member] = true
		if !ValidSubjectSubstitutionOutcome(member) {
			t.Errorf("declared outcome %q is not reported valid", member)
		}
	}
	if ValidSubjectSubstitutionOutcome("invented") {
		t.Error("an undeclared outcome must not be reported valid")
	}
	origins := map[SubjectSubstitutionOrigin]bool{}
	for _, member := range SubjectSubstitutionOriginVocabulary() {
		if origins[member] {
			t.Errorf("origin %q is declared twice", member)
		}
		origins[member] = true
		if !ValidSubjectSubstitutionOrigin(member) {
			t.Errorf("declared origin %q is not reported valid", member)
		}
	}
	if ValidSubjectSubstitutionOrigin("invented") {
		t.Error("an undeclared origin must not be reported valid")
	}
}

// TestSubstitutionGuardClarifiesWhenTheRedeemedChoiceIsNotWhatCommitted is
// the cell that separates "a receipt is present" from "the user chose THIS
// subject": the caller redeems the named parent's own offer for one subject
// and the turn commits a THIRD, different one. The exception keys on
// identity equality with the redeemed choice, so it does not apply.
func TestSubstitutionGuardClarifiesWhenTheRedeemedChoiceIsNotWhatCommitted(t *testing.T) {
	t.Parallel()
	third := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:gamma-service", Label: "gamma-service"}
	h := newNeedTurnHarness(t, nil)
	one := h.turn(needTurnRequest("request_5917_mismatch_one", true), needTurnResponse{
		resolution: SubjectResolution{
			Committed: []SubjectRef{substitutionRepoOne},
			Candidates: []SubjectCandidate{
				{ReceiptID: "receipt_5917_mismatch_a", Subject: substitutionRepoOne, State: contractsv1.ContextFabricResolutionCommitted, MatchedTerms: []string{"service"}, MatchReasons: []string{"matched"}, Confidence: 1, EvidenceRefIDs: []string{}},
				{ReceiptID: "receipt_5917_mismatch_b", Subject: substitutionRepoTwo, State: contractsv1.ContextFabricResolutionAmbiguous, MatchedTerms: []string{"service"}, MatchReasons: []string{"matched"}, Confidence: 0.4, EvidenceRefIDs: []string{}},
			},
		},
		bases: func() CommitBasisSet {
			bases := CommitBasisSet{}
			bases.Record(substitutionRepoOne, CommitBasisAuthoritativeIdentity)
			return bases
		}(),
	})
	two := continuingNeedTurn(needTurnRequest("request_5917_mismatch_two", true), one.result.ResultID)
	two.Question = "And how does the second one compare over the same period?"
	// Redeems the parent's own offer for the SECOND subject...
	two.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: one.result.ResultID, ReceiptID: "receipt_5917_mismatch_b"}}
	// ...while resolution commits a THIRD.
	outcome := h.turn(two, substitutionResponse(third, "receipt_5917_mismatch_c"))
	if outcome.result.Status != InvestigationClarificationRequired {
		t.Fatalf("status = %q, want %q: a receipt for one subject does not license committing another", outcome.result.Status, InvestigationClarificationRequired)
	}
	assertServedNothing(t, outcome.result)
	assertGuard(t, lastSubstitution(t, outcome), SubjectSubstitutionClarified, SubjectSubstitutionOriginResolver, substitutionRepoOne, third)
}

// TestSubstitutionGuardRefusesAndWithholdsAnUnreadableRememberedSubject: the
// non-clarifying arm with a remembered subject that no longer re-reads lists
// only this turn's own proposal, and says so on its own line.
func TestSubstitutionGuardRefusesAndWithholdsAnUnreadableRememberedSubject(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	one := h.turn(needTurnRequest("request_5917_refuse_gone_one", true), substitutionResponse(substitutionRepoOne, "receipt_5917_refuse_gone_one"))
	h.refuseCandidates = true
	two := continuingNeedTurn(needTurnRequest("request_5917_refuse_gone_two", true), one.result.ResultID)
	two.Question = "And how does the second one compare over the same period?"
	two.Options.AllowClarification = false
	outcome := h.turn(two, substitutionResponse(substitutionRepoTwo, "receipt_5917_refuse_gone_two"))
	if outcome.result.Status != InvestigationNoMatch {
		t.Fatalf("status = %q, want %q", outcome.result.Status, InvestigationNoMatch)
	}
	assertServedNothing(t, outcome.result)
	for _, candidate := range outcome.result.SubjectResolution.Candidates {
		if candidate.Subject.CanonicalID == substitutionRepoOne.CanonicalID {
			t.Errorf("the remembered subject was listed although it no longer re-reads for this principal")
		}
	}
	assertGuard(t, lastSubstitution(t, outcome), SubjectSubstitutionRefusedRememberedUnavailable, SubjectSubstitutionOriginResolver, substitutionRepoOne, substitutionRepoTwo)
}

// substitutionSharedIDRepo/Team are one canonical id under two kinds: the
// pair that separates "the identity comparison reads kind AND id" from "it
// reads the id".
var (
	substitutionSharedIDRepo = SubjectRef{Kind: SubjectRepository, CanonicalID: "svc-delta", Label: "delta (repository)"}
	substitutionSharedIDTeam = SubjectRef{Kind: SubjectTeam, CanonicalID: "svc-delta", Label: "delta (team)"}
)

// TestSubstitutionGuardClarifiesWhenOnlyTheKindChanges: one canonical id
// under two kinds is two subjects, and answering about the wrong one is the
// same substitution as any other.
func TestSubstitutionGuardClarifiesWhenOnlyTheKindChanges(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	_, two := substitutionTurns(t, h, "request_5917_sharedid",
		substitutionResponse(substitutionSharedIDRepo, "receipt_5917_sharedid_one"),
		substitutionResponse(substitutionSharedIDTeam, "receipt_5917_sharedid_two"), nil)
	if two.result.Status != InvestigationClarificationRequired {
		t.Fatalf("status = %q, want %q: the same canonical id under another kind is another subject", two.result.Status, InvestigationClarificationRequired)
	}
	assertServedNothing(t, two.result)
	assertGuard(t, lastSubstitution(t, two), SubjectSubstitutionClarified, SubjectSubstitutionOriginResolver, substitutionSharedIDRepo, substitutionSharedIDTeam)
}

// TestSameSubjectIdentityReadsKindAndCanonicalID is the comparison's own
// unit statement, over the pair that differs in exactly one half at a time.
func TestSameSubjectIdentityReadsKindAndCanonicalID(t *testing.T) {
	t.Parallel()
	if !sameSubjectIdentity(substitutionSharedIDRepo, SubjectRef{Kind: SubjectRepository, CanonicalID: "svc-delta", Label: "another label"}) {
		t.Error("the same kind and canonical id is the same subject, whatever the label")
	}
	if sameSubjectIdentity(substitutionSharedIDRepo, substitutionSharedIDTeam) {
		t.Error("one canonical id under two kinds is two subjects")
	}
	if sameSubjectIdentity(substitutionRepoOne, substitutionRepoTwo) {
		t.Error("two canonical ids under one kind is two subjects")
	}
	if sameSubjectIdentity(SubjectRef{Kind: SubjectRepository, CanonicalID: "Svc-Delta"}, SubjectRef{Kind: SubjectRepository, CanonicalID: "svc-delta"}) {
		t.Error("identifiers are case-sensitive")
	}
}

// TestSubjectSubstitutionRedeemedChoiceRefusesAnUnnamedParent: with no
// parent named, a receipt whose own result id is equally empty must not
// match it into a licence to change subject.
func TestSubjectSubstitutionRedeemedChoiceRefusesAnUnnamedParent(t *testing.T) {
	t.Parallel()
	hint := SubjectHint{Kind: substitutionRepoTwo.Kind, ID: substitutionRepoTwo.CanonicalID, Label: substitutionRepoTwo.Label, Source: "prior_subject_receipt"}
	emptyIDReceipt := []BoundSubjectReceipt{{ResultID: "", ReceiptID: "receipt_z"}}
	if subjectSubstitutionRedeemedChoice(substitutionRepoTwo, "", emptyIDReceipt, []SubjectHint{hint}) {
		t.Error("an unnamed parent and an unnamed receipt must not match each other")
	}
}

// TestSubstitutionGuardWithholdsWhenTheReReadIsCancelledMidCall: the verifier
// answers, and the context is gone by the time it does. The answer is not
// trusted -- a subject checked against a cancelled read was not checked.
func TestSubstitutionGuardWithholdsWhenTheReReadIsCancelledMidCall(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	h.engine.candidateVerifier = func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, contractsv1.ContextFabricSubjectKind, string) (bool, CandidateVerificationReason) {
		cancel()
		return true, CandidateVerificationValid
	}
	if h.engine.rememberedSubjectReadable(ctx, acceptancePrincipal(), needTurnRequest("request_5917_midcall", true), ResolvedGraphBinding{GraphKey: "need-turn-key"}, substitutionRepoOne) {
		t.Fatal("rememberedSubjectReadable() = true although the context died during the read, want false")
	}
}

// TestSubstitutionGuardWithholdsOnANonValidVerificationReason: the verifier
// answers true while naming a reason that is not `valid`. Both halves of its
// answer are read, so an affirmative beside an unverified reason withholds
// the listing rather than standing in for a proof.
func TestSubstitutionGuardWithholdsOnANonValidVerificationReason(t *testing.T) {
	t.Parallel()
	for _, reason := range []CandidateVerificationReason{CandidateVerificationClaimLost, CandidateVerificationGraphUnverifiable} {
		reason := reason
		t.Run(string(reason), func(t *testing.T) {
			t.Parallel()
			h := newNeedTurnHarness(t, nil)
			h.engine.candidateVerifier = func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, contractsv1.ContextFabricSubjectKind, string) (bool, CandidateVerificationReason) {
				return true, reason
			}
			if h.engine.rememberedSubjectReadable(context.Background(), acceptancePrincipal(), needTurnRequest("request_5917_reason_"+string(reason), true), ResolvedGraphBinding{GraphKey: "need-turn-key"}, substitutionRepoOne) {
				t.Fatalf("rememberedSubjectReadable() = true for reason %q, want false", reason)
			}
		})
	}
}

// TestSubstitutionGuardOffersTheParentsOwnLabel drives the CHAOS-5788 carry
// rig, where the parent's ledger records the anchor it bound: the remembered
// subject listed back to the caller carries the LABEL that parent served, not
// a canonical id standing in for one.
func TestSubstitutionGuardOffersTheParentsOwnLabel(t *testing.T) {
	t.Parallel()
	e, g, store, telemetry := buildCommittedAnchorEngineWithTelemetry(t)
	e.candidateVerifier = func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, contractsv1.ContextFabricSubjectKind, string) (bool, CandidateVerificationReason) {
		return true, CandidateVerificationValid
	}
	one := needTurnRequest("request_5917_label_one", true)
	parent, _ := committedAnchorTurn(t, e, g, store, one, reviewCarryIdentityResponse(committedAnchorRepo))
	saved := store.states[parent.ResultID]
	if saved == nil {
		t.Fatal("fixture defect: the parent saved no semantic state")
	}
	anchored := false
	for _, entry := range saved.ConfirmedNeeds {
		if entry.Member == contractsv1.ContextFabricStructureNeedSubjectAnchor {
			anchored = true
		}
	}
	if !anchored {
		t.Fatal("fixture defect: the parent recorded no subject_anchor ledger entry, so this cell proves nothing")
	}
	two := continuingNeedTurn(needTurnRequest("request_5917_label_two", true), parent.ResultID)
	two.Question = "And how does the second one compare over the same period?"
	mark := len(telemetry.confirmedNeedLedgers)
	child, _ := committedAnchorTurn(t, e, g, store, two, reviewCarryIdentityResponse(committedAnchorRepoOther))
	if child.Status != InvestigationClarificationRequired {
		t.Fatalf("status = %q, want %q", child.Status, InvestigationClarificationRequired)
	}
	assertServedNothing(t, child)
	candidates := child.SubjectResolution.Candidates
	if len(candidates) == 0 || candidates[0].Subject.CanonicalID != committedAnchorRepo.CanonicalID {
		t.Fatalf("candidates = %+v, want the remembered subject first", candidates)
	}
	if candidates[0].Subject.Label != committedAnchorRepo.Label {
		t.Fatalf("remembered label = %q, want the label the parent served (%q)", candidates[0].Subject.Label, committedAnchorRepo.Label)
	}
	lines := telemetry.confirmedNeedLedgers[mark:]
	if len(lines) != 1 || lines[0].SubstitutionGuard != SubjectSubstitutionClarified {
		t.Fatalf("ledger lines = %+v, want one clarified line", lines)
	}
}

// SubjectSubstitutionRealProducerScenarios names every guard outcome, in
// SubjectSubstitutionOutcomeVocabulary's own declared order, so a member
// added there reaches the certification sweep without a second,
// independently maintained list.
func SubjectSubstitutionRealProducerScenarios() []string {
	names := make([]string, 0, SubjectSubstitutionOutcomeCount)
	for _, outcome := range SubjectSubstitutionOutcomeVocabulary() {
		names = append(names, string(outcome))
	}
	return names
}

// substitutionJSONTurn runs ONE turn through the production JSON sink and
// returns what it wrote. The scripted response is set here rather than by
// needTurnHarness.turn, which the guarded exits cannot use: it asserts on
// telemetry this scenario has deliberately swapped away.
func substitutionJSONTurn(t *testing.T, h *needTurnHarness, buf *bytes.Buffer, request InvestigationRequest, response needTurnResponse) []byte {
	t.Helper()
	h.graph.response = response
	if _, err := h.engine.Investigate(context.Background(), acceptancePrincipal(), request); err != nil {
		t.Fatalf("Investigate(%s) error = %v", request.RequestID, err)
	}
	return buf.Bytes()
}

// substitutionFollowUp is the second turn of the two-turn shape, named on
// the parent and asking a different question.
func substitutionFollowUp(requestID, parentResultID string, statedWindow bool) InvestigationRequest {
	request := continuingNeedTurn(needTurnRequest(requestID, statedWindow), parentResultID)
	request.Question = "And how does the second one compare over the same period?"
	return request
}

// RunSubjectSubstitutionRealProducerScenarioForTest drives ONE named guard
// outcome through the REAL Engine.Investigate and returns the production
// slog JSON bytes the run wrote. Every scenario is a real exit; none builds
// an event by hand.
func RunSubjectSubstitutionRealProducerScenarioForTest(t *testing.T, scenario string) (log []byte, orgID string) {
	t.Helper()
	orgID = "org_acceptance"
	parent := substitutionResponse(substitutionRepoOne, "receipt_5917_scenario_one")
	child := substitutionResponse(substitutionRepoTwo, "receipt_5917_scenario_two")

	switch SubjectSubstitutionOutcome(scenario) {
	case SubjectSubstitutionNotEvaluated:
		// The class-default window gate ends the turn above the guard's own
		// decision point.
		h := newNeedTurnHarness(t, nil)
		one := h.turn(needTurnRequest("request_5917_sc_gated_one", true), parent)
		buf := swapToJSONLedgerTelemetry(h)
		return substitutionJSONTurn(t, h, buf, substitutionFollowUp("request_5917_sc_gated_two", one.result.ResultID, false), child), orgID

	case SubjectSubstitutionNoParentReference:
		h := newNeedTurnHarness(t, nil)
		buf := swapToJSONLedgerTelemetry(h)
		return substitutionJSONTurn(t, h, buf, needTurnRequest("request_5917_sc_root", true), parent), orgID

	case SubjectSubstitutionParentUnreadable:
		h := newNeedTurnHarness(t, nil)
		buf := swapToJSONLedgerTelemetry(h)
		return substitutionJSONTurn(t, h, buf, substitutionFollowUp("request_5917_sc_unreadable", "result_need_turn_9999", true), child), orgID

	case SubjectSubstitutionParentNoIdentity:
		h := newNeedTurnHarness(t, nil)
		one := h.turn(needTurnRequest("request_5917_sc_noid_one", true), substitutionEmptyResponse())
		buf := swapToJSONLedgerTelemetry(h)
		return substitutionJSONTurn(t, h, buf, substitutionFollowUp("request_5917_sc_noid_two", one.result.ResultID, true), child), orgID

	case SubjectSubstitutionNoCommittedSubject:
		h := newNeedTurnHarness(t, nil)
		one := h.turn(needTurnRequest("request_5917_sc_nocommit_one", true), parent)
		buf := swapToJSONLedgerTelemetry(h)
		return substitutionJSONTurn(t, h, buf, substitutionFollowUp("request_5917_sc_nocommit_two", one.result.ResultID, true), substitutionEmptyResponse()), orgID

	case SubjectSubstitutionSameSubject:
		h := newNeedTurnHarness(t, nil)
		one := h.turn(needTurnRequest("request_5917_sc_same_one", true), parent)
		buf := swapToJSONLedgerTelemetry(h)
		return substitutionJSONTurn(t, h, buf, substitutionFollowUp("request_5917_sc_same_two", one.result.ResultID, true), substitutionResponse(substitutionRepoOne, "receipt_5917_scenario_same")), orgID

	case SubjectSubstitutionRedeemedChoice:
		h := newNeedTurnHarness(t, nil)
		offering := needTurnResponse{
			resolution: SubjectResolution{
				Committed: []SubjectRef{substitutionRepoOne},
				Candidates: []SubjectCandidate{
					{ReceiptID: "receipt_5917_sc_choice_a", Subject: substitutionRepoOne, State: contractsv1.ContextFabricResolutionCommitted, MatchedTerms: []string{"service"}, MatchReasons: []string{"matched"}, Confidence: 1, EvidenceRefIDs: []string{}},
					{ReceiptID: "receipt_5917_sc_choice_b", Subject: substitutionRepoTwo, State: contractsv1.ContextFabricResolutionAmbiguous, MatchedTerms: []string{"service"}, MatchReasons: []string{"matched"}, Confidence: 0.4, EvidenceRefIDs: []string{}},
				},
			},
			bases: func() CommitBasisSet {
				bases := CommitBasisSet{}
				bases.Record(substitutionRepoOne, CommitBasisAuthoritativeIdentity)
				return bases
			}(),
		}
		one := h.turn(needTurnRequest("request_5917_sc_choice_one", true), offering)
		buf := swapToJSONLedgerTelemetry(h)
		request := substitutionFollowUp("request_5917_sc_choice_two", one.result.ResultID, true)
		request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: one.result.ResultID, ReceiptID: "receipt_5917_sc_choice_b"}}
		return substitutionJSONTurn(t, h, buf, request, child), orgID

	case SubjectSubstitutionClarified:
		h := newNeedTurnHarness(t, nil)
		one := h.turn(needTurnRequest("request_5917_sc_clarify_one", true), parent)
		buf := swapToJSONLedgerTelemetry(h)
		return substitutionJSONTurn(t, h, buf, substitutionFollowUp("request_5917_sc_clarify_two", one.result.ResultID, true), child), orgID

	case SubjectSubstitutionClarifiedRememberedUnavailable:
		h := newNeedTurnHarness(t, nil)
		one := h.turn(needTurnRequest("request_5917_sc_gone_one", true), parent)
		h.refuseCandidates = true
		buf := swapToJSONLedgerTelemetry(h)
		return substitutionJSONTurn(t, h, buf, substitutionFollowUp("request_5917_sc_gone_two", one.result.ResultID, true), child), orgID

	case SubjectSubstitutionRefused:
		h := newNeedTurnHarness(t, nil)
		one := h.turn(needTurnRequest("request_5917_sc_refuse_one", true), parent)
		buf := swapToJSONLedgerTelemetry(h)
		request := substitutionFollowUp("request_5917_sc_refuse_two", one.result.ResultID, true)
		request.Options.AllowClarification = false
		return substitutionJSONTurn(t, h, buf, request, child), orgID

	case SubjectSubstitutionRefusedRememberedUnavailable:
		h := newNeedTurnHarness(t, nil)
		one := h.turn(needTurnRequest("request_5917_sc_refuse_gone_one", true), parent)
		h.refuseCandidates = true
		buf := swapToJSONLedgerTelemetry(h)
		request := substitutionFollowUp("request_5917_sc_refuse_gone_two", one.result.ResultID, true)
		request.Options.AllowClarification = false
		return substitutionJSONTurn(t, h, buf, request, child), orgID
	}
	t.Fatalf("no real producer is wired for guard outcome %q", scenario)
	return nil, orgID
}
