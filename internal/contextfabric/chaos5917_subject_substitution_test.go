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
	"strings"
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
	if event.SubstitutionParentKind != wantParent.Kind || event.SubstitutionParentID != wantParent.CanonicalID {
		t.Errorf("parent identity = (%q,%q), want (%q,%q)", event.SubstitutionParentKind, event.SubstitutionParentID, wantParent.Kind, wantParent.CanonicalID)
	}
	want := []string{}
	if wantCommitted.CanonicalID != "" {
		want = []string{string(wantCommitted.Kind) + ":" + wantCommitted.CanonicalID}
	}
	if !equalStrings(event.SubstitutionCommittedIDs, want) {
		t.Errorf("committed ids = %v, want %v", event.SubstitutionCommittedIDs, want)
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

// TestResolveTerminalStatusNeverRefusesAClarifyingCallerOnTheSubstitutionOutcome
// is a crafted-input, predicate-level pin for a guard clause that restates an
// invariant enforced by construction at a different site: today
// decideSubjectSubstitution only ever PRODUCES
// SubjectSubstitutionRefused/RefusedRememberedUnavailable when
// AllowClarification is false, so resolveTerminalStatus's own
// AllowClarification check is a second enforcement of an invariant already
// held at that different site -- defence in depth, not dead code, and this
// is what proves it: an input combination the real guard cannot produce,
// exercised directly against resolveTerminalStatus, so the check's own
// removal is observable even though no real turn can reach it.
func TestResolveTerminalStatusNeverRefusesAClarifyingCallerOnTheSubstitutionOutcome(t *testing.T) {
	t.Parallel()
	for _, outcome := range []SubjectSubstitutionOutcome{SubjectSubstitutionRefused, SubjectSubstitutionRefusedRememberedUnavailable} {
		request := InvestigationRequest{Options: InvestigationOptions{AllowClarification: true}}
		resolution := SubjectResolution{}
		status, limitation := resolveTerminalStatus(request, &resolution, nil, false, declaredKindDecision{}, outcome, OfferFloorOutcome{})
		if limitation == subjectIdentityUnconfirmedTerminalLimitation {
			t.Fatalf("outcome %q with AllowClarification=true reached the non-clarifying refusal sentence (status %q) -- an invariant only decideSubjectSubstitution enforces was silently trusted here", outcome, status)
		}
	}
}

// TestTheSubjectIdentityUnconfirmedBasisOverItsWholeVocabularyDomain
// executes the new member's own domain on both sides of every rule that
// classifies a basis, same shape
// TestTheOrganizationScopeBasisOverItsWholeVocabularyDomain uses for its own
// member (role_answerability_test.go) -- except this member DOES carry its
// own basis token in its fixed sentence, the continuation_context_unverifiable
// convention, not the organization-scope one.
func TestTheSubjectIdentityUnconfirmedBasisOverItsWholeVocabularyDomain(t *testing.T) {
	t.Parallel()
	basis := subjectIdentityUnconfirmedTerminalBasis
	if basis != contractsv1.ContextFabricRefusalBasisSubjectIdentityUnconfirmed || string(basis) != "subject_identity_unconfirmed" {
		t.Fatalf("subjectIdentityUnconfirmedTerminalBasis = %q", basis)
	}
	if !contractsv1.ValidContextFabricRefusalBasis(basis) {
		t.Fatalf("%q is not a vocabulary member", basis)
	}
	seen := 0
	for _, member := range contractsv1.ContextFabricRefusalBasisVocabulary() {
		if member == basis {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("member occurs %d times in the vocabulary, want exactly 1", seen)
	}
	if contractsv1.ValidContextFabricFrameRefusalBasis(basis) {
		t.Fatal("admitted to the FRAME refusal allow-list: the frame validated, the gate passed, and resolution committed a real identity")
	}
	for _, nearMiss := range []contractsv1.ContextFabricRefusalBasis{"subject_identity_unconfirmed_", "SUBJECT_IDENTITY_UNCONFIRMED", "subject_identity", ""} {
		if contractsv1.ValidContextFabricRefusalBasis(nearMiss) {
			t.Fatalf("near miss %q was accepted", nearMiss)
		}
	}
	sentence := subjectIdentityUnconfirmedTerminalLimitation
	if !contractsv1.IsContextFabricServiceAuthoredLimitation(sentence) {
		t.Fatalf("the sentence is not service-authored: %q", sentence)
	}
	for _, fragment := range []string{"Name the subject directly", "no canonical facts were read"} {
		if !strings.Contains(sentence, fragment) {
			t.Fatalf("the sentence lacks %q: it must name what happened and how to continue", fragment)
		}
	}
	// UNLIKE organization_scope_unsupported/declared_kind_unmatched: this
	// sentence DOES carry its own basis token, the continuation_context_
	// unverifiable convention -- chris's exact accepted wording (dictations
	// 1998/1999) ends with it.
	if !strings.Contains(sentence, string(basis)) {
		t.Fatalf("the sentence must carry its basis token, chris's accepted wording: %q", sentence)
	}
}

// TestSubjectIdentityUnconfirmedValidatorHoldsBasisAndSentenceTogether
// takes the REAL served refusal document from
// TestSubstitutionGuardRefusesWhenTheCallerCannotBeAsked's own shape and
// proves the one-direction validation pin against it: the canonical
// document validates, and a copy with the fixed sentence stripped (basis
// left in place) is refused, naming this basis.
func TestSubjectIdentityUnconfirmedValidatorHoldsBasisAndSentenceTogether(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	_, two := substitutionTurns(t, h, "request_5926_validator",
		substitutionResponse(substitutionRepoOne, "receipt_5926_validator_one"),
		substitutionResponse(substitutionRepoTwo, "receipt_5926_validator_two"),
		func(request *InvestigationRequest) { request.Options.AllowClarification = false })
	served := two.result
	if served.RefusalBasis != contractsv1.ContextFabricRefusalBasisSubjectIdentityUnconfirmed {
		t.Fatalf("precondition failed: refusal_basis = %q", served.RefusalBasis)
	}
	if err := contractsv1.ValidateStoredResult(served); err != nil {
		t.Fatalf("the canonical served document does not validate: %v", err)
	}
	stripped := served
	stripped.Limitations = []string{ambiguousNoClarificationLimitation}
	if err := contractsv1.ValidateStoredResult(stripped); err == nil {
		t.Fatal("basis present, sentence stripped: want a validation error, got none")
	} else if !strings.Contains(err.Error(), "requires its fixed limitation sentence") {
		t.Fatalf("error = %v, want the fixed-sentence pin", err)
	}
}

// TestTheRefusalSentenceMakesNoClaimAboutTheFollowUpText serves the
// non-clarifying refusal over every way the guard can be reached -- the
// follow-up's own words naming the new subject, an "it"-style follow-up whose
// resolution still lands elsewhere, a caller hint, and a remembered subject
// that cannot be re-read -- and holds the ONE fixed sentence to what the
// guard actually decided: the identity moved. The guard is origin-blind, so a
// sentence that says anything about whether the follow-up's own text named a
// subject is false for part of that domain.
func TestTheRefusalSentenceMakesNoClaimAboutTheFollowUpText(t *testing.T) {
	t.Parallel()
	forbidden := []string{"own text", "named no subject", "named a subject", "names no subject", "names a subject"}
	for _, fragment := range forbidden {
		if strings.Contains(subjectIdentityUnconfirmedTerminalLimitation, fragment) {
			t.Fatalf("the fixed sentence claims something about the follow-up's text (%q): %q", fragment, subjectIdentityUnconfirmedTerminalLimitation)
		}
	}
	cases := []struct {
		name           string
		question       string
		hint           bool
		rememberedGone bool
	}{
		{name: "explicit_name", question: "How is " + substitutionRepoTwo.CanonicalID + " doing?"},
		{name: "it_style", question: "And how is it doing over the same period?"},
		{name: "caller_hint", question: "And how does that compare?", hint: true},
		{name: "remembered_unreadable", question: "How is " + substitutionRepoTwo.CanonicalID + " doing?", rememberedGone: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newNeedTurnHarness(t, nil)
			one := h.turn(needTurnRequest("request_refusal_text_"+tc.name+"_one", true), substitutionResponse(substitutionRepoOne, "receipt_refusal_text_"+tc.name+"_one"))
			h.refuseCandidates = tc.rememberedGone
			two := continuingNeedTurn(needTurnRequest("request_refusal_text_"+tc.name+"_two", true), one.result.ResultID)
			two.Question = tc.question
			two.Options.AllowClarification = false
			if tc.hint {
				two.RequestedScope.SubjectHints = []SubjectHint{{
					Kind: substitutionRepoTwo.Kind, ID: substitutionRepoTwo.CanonicalID,
					Label: substitutionRepoTwo.Label, Source: "caller",
				}}
			}
			out := h.turn(two, substitutionResponse(substitutionRepoTwo, "receipt_refusal_text_"+tc.name+"_two"))
			if out.result.Status != InvestigationNoMatch || out.result.RefusalBasis != contractsv1.ContextFabricRefusalBasisSubjectIdentityUnconfirmed {
				t.Fatalf("status=%q basis=%q, want the named refusal", out.result.Status, out.result.RefusalBasis)
			}
			assertLimitationPresent(t, out.result.Limitations, contractsv1.ContextFabricSubjectIdentityUnconfirmedLimitation)
			for _, limitation := range out.result.Limitations {
				for _, fragment := range forbidden {
					if strings.Contains(limitation, fragment) {
						t.Errorf("served limitation claims something about the follow-up's text (%q): %q", fragment, limitation)
					}
				}
			}
		})
	}
}

// assertLimitationPresent checks a fixed disclosure sentence is
// present, exact-match, among a served result's limitations.
func assertLimitationPresent(t *testing.T, limitations []string, want string) {
	t.Helper()
	for _, limitation := range limitations {
		if limitation == want {
			return
		}
	}
	t.Fatalf("limitations = %v, want to contain %q", limitations, want)
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
	// A caller that CAN clarify never reads the non-clarifying
	// caller's named reason -- same guard, same fixed prompt, different
	// RefusalBasis outcome entirely (empty: this is not a refusal).
	if two.result.RefusalBasis != "" {
		t.Fatalf("refusal_basis = %q, want empty: a clarifying caller is not refused", two.result.RefusalBasis)
	}
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
	event := lastSubstitution(t, outcome)
	assertGuard(t, event, SubjectSubstitutionRedeemedChoice, SubjectSubstitutionOriginPriorReceipt, substitutionRepoOne, substitutionRepoTwo)
	if event.SubstitutionOriginResultID != one.result.ResultID || event.SubstitutionOriginReceiptID != "receipt_5917_choice_b" {
		t.Errorf("origin receipt = (%q,%q), want the named parent's (%q,%q)", event.SubstitutionOriginResultID, event.SubstitutionOriginReceiptID, one.result.ResultID, "receipt_5917_choice_b")
	}
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
	// The non-clarifying caller reads a NAMED reason, not the
	// generic ambiguous-candidate ending.
	if two.result.RefusalBasis != contractsv1.ContextFabricRefusalBasisSubjectIdentityUnconfirmed {
		t.Fatalf("refusal_basis = %q, want %q", two.result.RefusalBasis, contractsv1.ContextFabricRefusalBasisSubjectIdentityUnconfirmed)
	}
	if two.result.Completeness.RefusalBasis != two.result.RefusalBasis {
		t.Fatalf("completeness.refusal_basis = %q, must mirror result.refusal_basis %q", two.result.Completeness.RefusalBasis, two.result.RefusalBasis)
	}
	assertLimitationPresent(t, two.result.Limitations, contractsv1.ContextFabricSubjectIdentityUnconfirmedLimitation)
}

// TestSubstitutionGuardWithholdsAnUnreadableRememberedSubject: the
// remembered subject fails its re-read and re-authorization, so it is not
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
			t.Errorf("the remembered subject was offered although it fails its re-read for this principal")
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

// cohortResponse commits every subject given, each on a proven identity.
func cohortResponse(subjects ...SubjectRef) needTurnResponse {
	bases := CommitBasisSet{}
	for _, subject := range subjects {
		bases.Record(subject, CommitBasisAuthoritativeIdentity)
	}
	return needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: append([]SubjectRef(nil), subjects...)}, bases: bases}
}

// TestSubstitutionGuardComparesTheWholeCommittedSet runs every committed-set
// shape against a parent that asserted one identity: another subject, a
// cohort of others, a cohort that adds others beside the parent's, and the
// parent's alone. Only the parent's alone is served; every other set is the
// same silent change by another shape, and its line lists the whole set.
func TestSubstitutionGuardComparesTheWholeCommittedSet(t *testing.T) {
	t.Parallel()
	gamma := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:gamma-service", Label: "gamma-service"}
	id := func(s SubjectRef) string { return string(s.Kind) + ":" + s.CanonicalID }
	for _, tc := range []struct {
		name   string
		child  []SubjectRef
		want   SubjectSubstitutionOutcome
		served bool
	}{
		{"one other subject", []SubjectRef{substitutionRepoTwo}, SubjectSubstitutionClarified, false},
		{"a cohort of others", []SubjectRef{substitutionRepoTwo, gamma}, SubjectSubstitutionClarified, false},
		{"others of two kinds", []SubjectRef{substitutionRepoTwo, substitutionOtherKind}, SubjectSubstitutionClarified, false},
		{"the parent beside others", []SubjectRef{substitutionRepoOne, substitutionRepoTwo}, SubjectSubstitutionClarified, false},
		{"others beside the parent", []SubjectRef{substitutionRepoTwo, substitutionRepoOne}, SubjectSubstitutionClarified, false},
		{"the parent alone", []SubjectRef{substitutionRepoOne}, SubjectSubstitutionSameSubject, true},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newNeedTurnHarness(t, nil)
			_, two := substitutionTurns(t, h, "request_5917_set", substitutionResponse(substitutionRepoOne, "receipt_5917_set_one"), cohortResponse(tc.child...), nil)
			event := lastSubstitution(t, two)
			if event.SubstitutionGuard != tc.want {
				t.Fatalf("substitution_guard = %q, want %q", event.SubstitutionGuard, tc.want)
			}
			wantIDs := make([]string, 0, len(tc.child))
			for _, subject := range tc.child {
				wantIDs = append(wantIDs, id(subject))
			}
			if !equalStrings(event.SubstitutionCommittedIDs, wantIDs) {
				t.Errorf("substitution_committed_ids = %v, want the whole set %v", event.SubstitutionCommittedIDs, wantIDs)
			}
			if event.SubstitutionParentID != substitutionRepoOne.CanonicalID {
				t.Errorf("substitution_parent_id = %q, want %q", event.SubstitutionParentID, substitutionRepoOne.CanonicalID)
			}
			if tc.served {
				if len(two.result.SubjectResolution.Committed) != len(tc.child) {
					t.Errorf("committed = %d, want the parent served", len(two.result.SubjectResolution.Committed))
				}
				return
			}
			if two.result.Status != InvestigationClarificationRequired {
				t.Errorf("status = %q, want %q", two.result.Status, InvestigationClarificationRequired)
			}
			assertServedNothing(t, two.result)
		})
	}
}

// TestSubstitutionGuardRefusesMixedReceiptsThatNeverChoseTheCommit is the
// executed shape of the one-receipt rule: the caller redeems the named
// parent's own receipt for the PARENT's subject, and another result's
// receipt for a second subject, and the turn commits the second. The parent
// never offered it, so it is not a redeemed choice and is not served.
func TestSubstitutionGuardRefusesMixedReceiptsThatNeverChoseTheCommit(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	stranger := h.turn(needTurnRequest("request_5917_mixed_stranger", true), substitutionResponse(substitutionRepoTwo, "receipt_5917_mixed_str"))
	one := h.turn(needTurnRequest("request_5917_mixed_one", true), substitutionResponse(substitutionRepoOne, "receipt_5917_mixed_a"))
	two := continuingNeedTurn(needTurnRequest("request_5917_mixed_two", true), one.result.ResultID)
	two.Question = "And how does the second one compare over the same period?"
	two.PriorSubjectReceipts = []BoundSubjectReceipt{
		{ResultID: one.result.ResultID, ReceiptID: "receipt_5917_mixed_a"},
		{ResultID: stranger.result.ResultID, ReceiptID: "receipt_5917_mixed_str"},
	}
	outcome := h.turn(two, substitutionResponse(substitutionRepoTwo, "receipt_5917_mixed_srv"))
	if outcome.result.Status != InvestigationClarificationRequired {
		t.Fatalf("status = %q, want %q: the parent never offered the committed subject", outcome.result.Status, InvestigationClarificationRequired)
	}
	assertServedNothing(t, outcome.result)
	event := lastSubstitution(t, outcome)
	assertGuard(t, event, SubjectSubstitutionClarified, SubjectSubstitutionOriginPriorReceipt, substitutionRepoOne, substitutionRepoTwo)
	// The line names the receipt that carried the committed subject, and the
	// result that issued it -- the other result, never the named parent.
	if event.SubstitutionOriginResultID != stranger.result.ResultID || event.SubstitutionOriginReceiptID != "receipt_5917_mixed_str" {
		t.Errorf("origin receipt = (%q,%q), want (%q,%q)", event.SubstitutionOriginResultID, event.SubstitutionOriginReceiptID, stranger.result.ResultID, "receipt_5917_mixed_str")
	}
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
	if got := h.engine.rememberedSubjectReadable(ctx, acceptancePrincipal(), needTurnRequest("request_5917_cancelled", true), ResolvedGraphBinding{GraphKey: "need-turn-key"}, substitutionRepoOne); got.readable() || got.Check != SubjectSubstitutionRememberedCancelledBefore || got.ContextError != context.Canceled.Error() {
		t.Fatalf("rememberedSubjectReadable() = %+v on a cancelled context, want cancelled_before_check carrying the context error", got)
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
		"substitution_guard":                    string(SubjectSubstitutionClarified),
		"substitution_origin":                   string(SubjectSubstitutionOriginResolver),
		"substitution_parent_kind":              string(substitutionRepoOne.Kind),
		"substitution_parent_id":                substitutionRepoOne.CanonicalID,
		"substitution_parent_result_id":         one.result.ResultID,
		"substitution_origin_issued_for":        "",
		"substitution_remembered_check":         string(SubjectSubstitutionRememberedReadable),
		"substitution_remembered_reason":        string(CandidateVerificationValid),
		"substitution_remembered_context_error": "",
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
	for _, key := range []string{"substitution_origin_result_id", "substitution_origin_receipt_id"} {
		if value, present := line[key]; !present || value != "" {
			t.Errorf("%s = %v (present=%t), want an empty string for a resolver-origin subject", key, value, present)
		}
	}
	ids, ok := line["substitution_committed_ids"].([]any)
	if !ok || len(ids) != 1 || ids[0] != string(substitutionRepoTwo.Kind)+":"+substitutionRepoTwo.CanonicalID {
		t.Errorf("substitution_committed_ids = %v, want [%s:%s]", line["substitution_committed_ids"], substitutionRepoTwo.Kind, substitutionRepoTwo.CanonicalID)
	}
	if substitutionRepoOne.CanonicalID == substitutionRepoTwo.CanonicalID {
		t.Fatal("fixture defect: the two identities coincide, so neither is pinned")
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
	if found["level"] != "INFO" {
		t.Fatalf("confirmed-need-ledger line level = %v, want INFO", found["level"])
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
		{"parent absent", subjectSubstitutionInput{Committed: []SubjectRef{substitutionRepoTwo}, AllowClarification: true}, SubjectSubstitutionNoParentReference, false},
		{"parent unloadable", subjectSubstitutionInput{Parent: parentAnchorEvidence{Referenced: true}, Committed: []SubjectRef{substitutionRepoTwo}, AllowClarification: true}, SubjectSubstitutionParentUnreadable, false},
		{"parent held nothing", subjectSubstitutionInput{Parent: parentAnchorEvidence{Referenced: true, Loaded: true}, Committed: []SubjectRef{substitutionRepoTwo}, AllowClarification: true}, SubjectSubstitutionParentNoIdentity, false},
		{"parent subject missing a kind", subjectSubstitutionInput{Parent: parentAnchorEvidence{Referenced: true, Loaded: true, Subject: SubjectRef{CanonicalID: "repository:alpha-service"}}, Committed: []SubjectRef{substitutionRepoTwo}, AllowClarification: true}, SubjectSubstitutionParentNoIdentity, false},
		{"this turn committed nothing", subjectSubstitutionInput{Parent: held, AllowClarification: true}, SubjectSubstitutionNoCommittedSubject, false},
		{"same identity", subjectSubstitutionInput{Parent: held, Committed: []SubjectRef{substitutionRepoOne}, AllowClarification: true, RememberedAvailable: true}, SubjectSubstitutionSameSubject, false},
		{"different identity same kind", subjectSubstitutionInput{Parent: held, Committed: []SubjectRef{substitutionRepoTwo}, AllowClarification: true, RememberedAvailable: true}, SubjectSubstitutionClarified, true},
		{"different kind", subjectSubstitutionInput{Parent: held, Committed: []SubjectRef{substitutionOtherKind}, AllowClarification: true, RememberedAvailable: true}, SubjectSubstitutionClarified, true},
		{"different identity, remembered unreadable", subjectSubstitutionInput{Parent: held, Committed: []SubjectRef{substitutionRepoTwo}, AllowClarification: true}, SubjectSubstitutionClarifiedRememberedUnavailable, true},
		{"different identity, caller cannot be asked", subjectSubstitutionInput{Parent: held, Committed: []SubjectRef{substitutionRepoTwo}, RememberedAvailable: true}, SubjectSubstitutionRefused, true},
		{"different identity, caller cannot be asked, remembered unreadable", subjectSubstitutionInput{Parent: held, Committed: []SubjectRef{substitutionRepoTwo}}, SubjectSubstitutionRefusedRememberedUnavailable, true},
		{"redeemed choice beats clarification", subjectSubstitutionInput{Parent: held, Committed: []SubjectRef{substitutionRepoTwo}, RedeemedChoice: true, AllowClarification: true, RememberedAvailable: true}, SubjectSubstitutionRedeemedChoice, false},
		{"redeemed choice beats refusal", subjectSubstitutionInput{Parent: held, Committed: []SubjectRef{substitutionRepoTwo}, RedeemedChoice: true}, SubjectSubstitutionRedeemedChoice, false},
		{"same identity is never a subject change", subjectSubstitutionInput{Parent: held, Committed: []SubjectRef{substitutionRepoOne}, RedeemedChoice: true, AllowClarification: true}, SubjectSubstitutionSameSubject, false},
		{"guard-issued parent, remembered unlisted", subjectSubstitutionInput{Parent: parentAnchorEvidence{Referenced: true, Loaded: true, GuardIssued: true}, Committed: []SubjectRef{substitutionRepoTwo}, AllowClarification: true, RememberedAvailable: true}, SubjectSubstitutionClarifiedRememberedUnavailable, true},
		{"guard-issued parent, remembered unlisted, caller cannot be asked", subjectSubstitutionInput{Parent: parentAnchorEvidence{Referenced: true, Loaded: true, GuardIssued: true}, Committed: []SubjectRef{substitutionRepoTwo}}, SubjectSubstitutionRefusedRememberedUnavailable, true},
		{"guard-issued parent, remembered unlisted, redeemed choice", subjectSubstitutionInput{Parent: parentAnchorEvidence{Referenced: true, Loaded: true, GuardIssued: true}, Committed: []SubjectRef{substitutionRepoTwo}, RedeemedChoice: true}, SubjectSubstitutionRedeemedChoice, false},
		{"guard-issued parent, remembered unlisted, commits nothing", subjectSubstitutionInput{Parent: parentAnchorEvidence{Referenced: true, Loaded: true, GuardIssued: true}, AllowClarification: true}, SubjectSubstitutionNoCommittedSubject, false},
		{"a redeemed choice never licenses a set of several", subjectSubstitutionInput{Parent: held, Committed: []SubjectRef{substitutionRepoTwo, substitutionOtherKind}, RedeemedChoice: true, AllowClarification: true, RememberedAvailable: true}, SubjectSubstitutionClarified, true},
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
				Parent: held, Committed: []SubjectRef{substitutionRepoTwo},
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
		Parent:    parentAnchorEvidence{Referenced: true, Loaded: true, Subject: substitutionRepoOne},
		Committed: []SubjectRef{substitutionRepoTwo}, Origin: "invented_by_a_caller",
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
	hint := SubjectHint{Kind: substitutionRepoTwo.Kind, ID: substitutionRepoTwo.CanonicalID, Label: substitutionRepoTwo.Label, Source: "caller"}
	redeemed := []priorSubjectReceiptOutcome{redeemedOutcome("result_other_0002", substitutionRepoTwo)}
	carried := map[contractsv1.ContextFabricStructureNeedKind]confirmedStructureMember{
		contractsv1.ContextFabricStructureNeedSubjectAnchor: {
			Member:      contractsv1.ContextFabricStructureNeedSubjectAnchor,
			AppliedKind: substitutionRepoTwo.Kind, AppliedValue: substitutionRepoTwo.CanonicalID,
			Basis: ConfirmedNeedBasisEngineCommitted,
		},
	}
	got := subjectOriginOf(substitutionRepoTwo, parentReceiptIssuers{named: "result_parent_0001"}, redeemed, []SubjectHint{hint}, carried)
	if got.Origin != SubjectSubstitutionOriginPriorReceipt || got.ReceiptResultID != "result_other_0002" || got.ReceiptID != redeemed[0].receipt.ReceiptID {
		t.Errorf("origin = %+v, want the redeemed receipt of result_other_0002", got)
	}
	if got := subjectOriginOf(substitutionRepoTwo, parentReceiptIssuers{named: ""}, nil, []SubjectHint{hint}, carried); got.Origin != SubjectSubstitutionOriginCallerHint || got.ReceiptResultID != "" {
		t.Errorf("origin = %+v, want %q with no receipt", got, SubjectSubstitutionOriginCallerHint)
	}
	if got := subjectOriginOf(substitutionRepoTwo, parentReceiptIssuers{named: ""}, nil, nil, carried); got.Origin != SubjectSubstitutionOriginEngineCarry {
		t.Errorf("origin = %+v, want %q", got, SubjectSubstitutionOriginEngineCarry)
	}
	if got := subjectOriginOf(substitutionRepoTwo, parentReceiptIssuers{named: ""}, nil, nil, nil); got.Origin != SubjectSubstitutionOriginResolver {
		t.Errorf("origin = %+v, want %q", got, SubjectSubstitutionOriginResolver)
	}
	dropped := []priorSubjectReceiptOutcome{redeemedOutcome("result_other_0002", substitutionRepoTwo)}
	dropped[0].droppedByHintBudget = true
	if got := subjectOriginOf(substitutionRepoTwo, parentReceiptIssuers{named: ""}, dropped, nil, nil); got.Origin != SubjectSubstitutionOriginResolver {
		t.Errorf("origin for a budget-dropped redemption = %+v, want %q", got, SubjectSubstitutionOriginResolver)
	}
	other := SubjectHint{Kind: substitutionRepoOne.Kind, ID: substitutionRepoOne.CanonicalID, Label: substitutionRepoOne.Label, Source: "caller"}
	otherCarried := map[contractsv1.ContextFabricStructureNeedKind]confirmedStructureMember{
		contractsv1.ContextFabricStructureNeedSubjectAnchor: {
			Member:      contractsv1.ContextFabricStructureNeedSubjectAnchor,
			AppliedKind: substitutionRepoOne.Kind, AppliedValue: substitutionRepoOne.CanonicalID,
		},
	}
	if got := subjectOriginOf(substitutionRepoTwo, parentReceiptIssuers{named: ""}, []priorSubjectReceiptOutcome{redeemedOutcome("r", substitutionRepoOne)}, []SubjectHint{other}, otherCarried); got.Origin != SubjectSubstitutionOriginResolver {
		t.Errorf("origin for channels naming another subject = %+v, want %q", got, SubjectSubstitutionOriginResolver)
	}
}

// TestSubjectOriginOfNamesTheParentsReceiptWhenSeveralCarriedTheSubject: the
// receipt a redeemed choice stands on is the one the line names.
func TestSubjectOriginOfNamesTheParentsReceiptWhenSeveralCarriedTheSubject(t *testing.T) {
	t.Parallel()
	outcomes := []priorSubjectReceiptOutcome{
		redeemedOutcome("result_other_0002", substitutionRepoTwo),
		redeemedOutcome("result_parent_0001", substitutionRepoTwo),
	}
	got := subjectOriginOf(substitutionRepoTwo, parentReceiptIssuers{named: "result_parent_0001"}, outcomes, nil, nil)
	if got.ReceiptResultID != "result_parent_0001" {
		t.Errorf("origin receipt result = %q, want the named parent's", got.ReceiptResultID)
	}
	got = subjectOriginOf(substitutionRepoTwo, parentReceiptIssuers{named: "result_elsewhere"}, outcomes, nil, nil)
	if got.ReceiptResultID != "result_other_0002" {
		t.Errorf("with no parent receipt, origin receipt result = %q, want the first redemption's", got.ReceiptResultID)
	}
}

// TestSubstitutionGuardReportsTheEngineCarryOrigin drives the
// committed-anchor carry rig: turn one commits an anchor the engine captures, turn two asks
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

// TestSubjectSubstitutionResolutionOffersTheRememberedSubjectFirst pins the
// served shape: order is the offer, nothing stays committed, and this turn's
// own candidates survive.
func TestSubjectSubstitutionResolutionOffersTheRememberedSubjectFirst(t *testing.T) {
	t.Parallel()
	committing := SubjectResolution{
		Committed: []SubjectRef{substitutionRepoTwo},
		Candidates: []SubjectCandidate{{
			ReceiptID: "receipt_original", Subject: substitutionRepoTwo, State: contractsv1.ContextFabricResolutionCommitted,
			MatchedTerms: []string{"service"}, MatchReasons: []string{"matched"}, Confidence: 1, EvidenceRefIDs: []string{},
		}},
		CommitDecisionDigests: []contractsv1.ContextFabricCommitDecisionDigest{{Subject: substitutionRepoTwo, CommitGate: "identity_fast_path", IdentityProven: true}},
	}
	decision := subjectSubstitutionDecision{Outcome: SubjectSubstitutionClarified, Parent: substitutionRepoOne, Committed: []SubjectRef{substitutionRepoTwo}, RememberedListed: true}
	guarded := subjectSubstitutionResolution(committing, decision, "result_parent_0001")
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
	withheld := subjectSubstitutionResolution(committing, subjectSubstitutionDecision{Outcome: SubjectSubstitutionClarifiedRememberedUnavailable, Parent: substitutionRepoOne, Committed: []SubjectRef{substitutionRepoTwo}}, "result_parent_0001")
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
// non-clarifying arm with a remembered subject that fails its re-read lists
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
			t.Errorf("the remembered subject was listed although it fails its re-read for this principal")
		}
	}
	assertGuard(t, lastSubstitution(t, outcome), SubjectSubstitutionRefusedRememberedUnavailable, SubjectSubstitutionOriginResolver, substitutionRepoOne, substitutionRepoTwo)
	// An unreadable remembered subject still gets the SAME named
	// reason -- one fixed sentence for both firing outcomes, per chris's
	// acceptance (dictation 1999), not a second wording keyed on readability.
	if outcome.result.RefusalBasis != contractsv1.ContextFabricRefusalBasisSubjectIdentityUnconfirmed {
		t.Fatalf("refusal_basis = %q, want %q", outcome.result.RefusalBasis, contractsv1.ContextFabricRefusalBasisSubjectIdentityUnconfirmed)
	}
	assertLimitationPresent(t, outcome.result.Limitations, contractsv1.ContextFabricSubjectIdentityUnconfirmedLimitation)
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
	if got := h.engine.rememberedSubjectReadable(ctx, acceptancePrincipal(), needTurnRequest("request_5917_midcall", true), ResolvedGraphBinding{GraphKey: "need-turn-key"}, substitutionRepoOne); got.readable() || got.Check != SubjectSubstitutionRememberedCancelledDuring || got.Reason != CandidateVerificationValid || got.ContextError != context.Canceled.Error() {
		t.Fatalf("rememberedSubjectReadable() = %+v although the context died during the read, want cancelled_during_check carrying the verifier reason and the context error", got)
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
			if got := h.engine.rememberedSubjectReadable(context.Background(), acceptancePrincipal(), needTurnRequest("request_5917_reason_"+string(reason), true), ResolvedGraphBinding{GraphKey: "need-turn-key"}, substitutionRepoOne); got.readable() || got.Check != SubjectSubstitutionRememberedRefused || got.Reason != reason || got.ContextError != "" {
				t.Fatalf("rememberedSubjectReadable() = %+v for reason %q, want refused carrying that reason", got, reason)
			}
		})
	}
}

// TestSubstitutionGuardOffersTheParentsOwnLabel drives the committed-anchor carry
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

// TestSubstitutionGuardLeavesTheShadowBinderOnItsServedDocument pins the
// guard's one effect on the shadow anchor binder: the binder reads the
// subjects the saved document still commits (servedResolutionProof), so a
// guarded turn -- which commits none -- never records a binding to the
// substitute it declined to serve. A same-subject continuation reaches the
// binder exactly as resolution returned it.
func TestSubstitutionGuardLeavesTheShadowBinderOnItsServedDocument(t *testing.T) {
	t.Parallel()
	e, g, store, telemetry := buildCommittedAnchorEngineWithTelemetry(t)
	e.candidateVerifier = func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, contractsv1.ContextFabricSubjectKind, string) (bool, CandidateVerificationReason) {
		return true, CandidateVerificationValid
	}
	parent, _ := committedAnchorTurn(t, e, g, store, needTurnRequest("request_5917_shadow_one", true), reviewCarryIdentityResponse(committedAnchorRepo))
	parentMark := len(telemetry.anchorBindingTransitions)

	guarded := continuingNeedTurn(needTurnRequest("request_5917_shadow_two", true), parent.ResultID)
	guarded.Question = "And how does the second one compare over the same period?"
	child, _ := committedAnchorTurn(t, e, g, store, guarded, reviewCarryIdentityResponse(committedAnchorRepoOther))
	if child.Status != InvestigationClarificationRequired {
		t.Fatalf("status = %q, want the guard to fire", child.Status)
	}
	guardedLines := telemetry.anchorBindingTransitions[parentMark:]
	if len(guardedLines) == 0 {
		t.Fatal("the shadow binder emitted no transition for the guarded turn")
	}
	for _, line := range guardedLines {
		if line.To.CanonicalID == committedAnchorRepoOther.CanonicalID {
			t.Errorf("the shadow binder recorded the substitute %q on a turn that did not serve it: %+v", committedAnchorRepoOther.CanonicalID, line.To)
		}
	}

	sameMark := len(telemetry.anchorBindingTransitions)
	same := continuingNeedTurn(needTurnRequest("request_5917_shadow_three", true), parent.ResultID)
	committedAnchorTurn(t, e, g, store, same, reviewCarryIdentityResponse(committedAnchorRepo))
	sameLines := telemetry.anchorBindingTransitions[sameMark:]
	if len(sameLines) == 0 {
		t.Fatal("the shadow binder emitted no transition for the same-subject turn")
	}
	weighed := false
	for _, line := range sameLines {
		for _, committed := range line.CommittedSubjects {
			if committed == string(committedAnchorRepo.Kind)+":"+committedAnchorRepo.CanonicalID+"="+string(CommitBasisAuthoritativeIdentity) {
				weighed = true
			}
		}
	}
	if !weighed {
		t.Errorf("the shadow binder did not weigh the same-subject commit: %+v", sameLines)
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

// equalStrings compares two string lists element by element, nil and empty
// reading the same.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// redeemedOutcome is one receipt redemption as resolvePriorSubjectHints
// records it: the receipt, the result that issued it, and the hint its
// redemption produced.
func redeemedOutcome(issuer string, subject SubjectRef) priorSubjectReceiptOutcome {
	return priorSubjectReceiptOutcome{
		receipt: BoundSubjectReceipt{ResultID: issuer, ReceiptID: "receipt_for_" + subject.CanonicalID},
		hint:    SubjectHint{Kind: subject.Kind, ID: subject.CanonicalID, Label: subject.Label, Source: "prior_subject_receipt"},
		hasHint: true,
	}
}

// TestSubjectSubstitutionRedeemedChoiceIsOnePredicateOverOneReceipt runs the
// whole grid: which receipts were redeemed {one from the named parent for X,
// one from another result for Y, both} against what the turn commits {X, Y,
// a third Z, X and Y together}. Only the parent's own receipt for exactly
// the one identity committed is a choice of it.
func TestSubjectSubstitutionRedeemedChoiceIsOnePredicateOverOneReceipt(t *testing.T) {
	t.Parallel()
	const parent = "result_parent_0001"
	x, y := substitutionRepoTwo, substitutionOtherKind
	z := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:gamma-service", Label: "gamma-service"}
	fromParentForX := redeemedOutcome(parent, x)
	fromOtherForY := redeemedOutcome("result_other_0002", y)
	receipts := map[string][]priorSubjectReceiptOutcome{
		"parent_for_x":         {fromParentForX},
		"other_for_y":          {fromOtherForY},
		"parent_for_x+other_y": {fromParentForX, fromOtherForY},
	}
	commits := map[string][]SubjectRef{"x": {x}, "y": {y}, "z": {z}, "x+y": {x, y}}
	want := map[string]bool{"parent_for_x/x": true, "parent_for_x+other_y/x": true}
	for rname, outcomes := range receipts {
		for cname, committed := range commits {
			cell := rname + "/" + cname
			got := subjectSubstitutionRedeemedChoice(committed, parentReceiptIssuers{named: parent}, outcomes)
			if got != want[cell] {
				t.Errorf("%s: redeemed choice = %t, want %t", cell, got, want[cell])
			}
		}
	}
}

// TestSubjectSubstitutionRedeemedChoiceRefusesWhatNeverReachedResolution:
// no named parent, a receipt without a hint, and a hint the budget dropped
// are none of them a choice.
func TestSubjectSubstitutionRedeemedChoiceRefusesWhatNeverReachedResolution(t *testing.T) {
	t.Parallel()
	x := []SubjectRef{substitutionRepoTwo}
	unnamed := redeemedOutcome("", substitutionRepoTwo)
	if subjectSubstitutionRedeemedChoice(x, parentReceiptIssuers{named: ""}, []priorSubjectReceiptOutcome{unnamed}) {
		t.Error("an unnamed parent and an unnamed receipt must not match each other")
	}
	noHint := redeemedOutcome("result_parent_0001", substitutionRepoTwo)
	noHint.hasHint = false
	if subjectSubstitutionRedeemedChoice(x, parentReceiptIssuers{named: "result_parent_0001"}, []priorSubjectReceiptOutcome{noHint}) {
		t.Error("a receipt whose redemption produced no hint chose nothing")
	}
	dropped := redeemedOutcome("result_parent_0001", substitutionRepoTwo)
	dropped.droppedByHintBudget = true
	if subjectSubstitutionRedeemedChoice(x, parentReceiptIssuers{named: "result_parent_0001"}, []priorSubjectReceiptOutcome{dropped}) {
		t.Error("a hint the budget dropped never reached resolution")
	}
	padded := redeemedOutcome(" result_parent_0001 ", substitutionRepoTwo)
	if !subjectSubstitutionRedeemedChoice(x, parentReceiptIssuers{named: "result_parent_0001"}, []priorSubjectReceiptOutcome{padded}) {
		t.Error("the issuer is compared as resolvePriorSubjectHints loads it: trimmed")
	}
}

// TestSubjectSubstitutionOriginOfDescribesTheSubstitutingSubject: over a set,
// the origin names the channel of the first committed identity that is not
// the parent's.
func TestSubjectSubstitutionOriginOfDescribesTheSubstitutingSubject(t *testing.T) {
	t.Parallel()
	hint := SubjectHint{Kind: substitutionRepoTwo.Kind, ID: substitutionRepoTwo.CanonicalID, Label: substitutionRepoTwo.Label, Source: "caller"}
	parentRedeemed := []priorSubjectReceiptOutcome{redeemedOutcome("result_parent_0001", substitutionRepoOne)}
	both := []SubjectRef{substitutionRepoOne, substitutionRepoTwo}
	if got := subjectSubstitutionOriginOf(both, substitutionRepoOne, parentReceiptIssuers{named: "result_parent_0001"}, parentRedeemed, []SubjectHint{hint}, nil); got.Origin != SubjectSubstitutionOriginCallerHint {
		t.Errorf("origin = %+v, want the substituting subject's %q", got, SubjectSubstitutionOriginCallerHint)
	}
	if got := subjectSubstitutionOriginOf([]SubjectRef{substitutionRepoOne}, substitutionRepoOne, parentReceiptIssuers{named: "result_parent_0001"}, parentRedeemed, nil, nil); got.Origin != SubjectSubstitutionOriginPriorReceipt {
		t.Errorf("origin for the parent alone = %+v, want %q", got, SubjectSubstitutionOriginPriorReceipt)
	}
	if got := subjectSubstitutionOriginOf(nil, substitutionRepoOne, parentReceiptIssuers{named: ""}, nil, nil, nil); got.Origin != SubjectSubstitutionOriginNotApplicable {
		t.Errorf("origin for nothing committed = %+v, want %q", got, SubjectSubstitutionOriginNotApplicable)
	}
}

// TestCommittedIsExactlyAndCommittedIDs pins the set's two statements.
func TestCommittedIsExactlyAndCommittedIDs(t *testing.T) {
	t.Parallel()
	if !committedIsExactly([]SubjectRef{substitutionRepoOne}, substitutionRepoOne) {
		t.Error("the parent alone is exactly the parent")
	}
	for name, set := range map[string][]SubjectRef{
		"nothing": nil, "another": {substitutionRepoTwo},
		"parent plus another": {substitutionRepoOne, substitutionRepoTwo},
		"another plus parent": {substitutionRepoTwo, substitutionRepoOne},
	} {
		if committedIsExactly(set, substitutionRepoOne) {
			t.Errorf("%s: reported as exactly the parent", name)
		}
	}
	if got := committedIDs([]SubjectRef{substitutionRepoOne, substitutionOtherKind}); !equalStrings(got, []string{"repository:repository:alpha-service", "team:team:platform"}) {
		t.Errorf("committedIDs = %v", got)
	}
	if got := committedIDs(nil); got == nil || len(got) != 0 {
		t.Errorf("committedIDs(nil) = %#v, want a non-nil empty list", got)
	}
}

// TestSubstitutionGuardReadsTheParentFromItsPayload: the parent's semantic
// snapshot is malformed, or absent, while its stored result payload reads.
// The guard compares against the subject that payload served exactly as it
// would with a readable snapshot, and never reads the parent as unreadable.
func TestSubstitutionGuardReadsTheParentFromItsPayload(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		plant func(store *staticResultStore, parentID string)
	}{
		{"snapshot malformed", func(store *staticResultStore, parentID string) {
			delete(store.states, parentID)
			if store.legacyStates == nil {
				store.legacyStates = map[string][]byte{}
			}
			store.legacyStates[parentID] = []byte(`{"format_version":"semantic-state.v1","family":"not-a-family"}`)
		}},
		{"snapshot absent", func(store *staticResultStore, parentID string) {
			delete(store.states, parentID)
		}},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newNeedTurnHarness(t, nil)
			one := h.turn(needTurnRequest("request_5917_payload_one", true), substitutionResponse(substitutionRepoOne, "receipt_5917_payload_one"))
			tc.plant(h.store, one.result.ResultID)
			stored, err := h.store.Get(context.Background(), acceptancePrincipal(), one.result.ResultID)
			if err != nil || stored.SemanticStateRead == SemanticStateReadAvailable {
				t.Fatalf("fixture defect: parent read err=%v snapshot=%s, want a readable payload beside an unreadable snapshot", err, stored.SemanticStateRead)
			}
			two := continuingNeedTurn(needTurnRequest("request_5917_payload_two", true), one.result.ResultID)
			two.Question = "And how does the second one compare over the same period?"
			outcome := h.turn(two, substitutionResponse(substitutionRepoTwo, "receipt_5917_payload_two"))
			if outcome.result.Status != InvestigationClarificationRequired {
				t.Fatalf("status = %q, want %q: the parent's payload still says what it served", outcome.result.Status, InvestigationClarificationRequired)
			}
			assertServedNothing(t, outcome.result)
			assertGuard(t, lastSubstitution(t, outcome), SubjectSubstitutionClarified, SubjectSubstitutionOriginResolver, substitutionRepoOne, substitutionRepoTwo)
		})
	}
}
