package contextfabric

import (
	"context"
	"reflect"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// admittableBinding is a held binding every admission check passes for at
// admissionNow.
var admissionNow = time.Unix(10_000, 0).UTC()

func admittableBinding() AnchorBinding {
	b := heldBinding(AnchorBindingBound, bindAlpha)
	b.PolicyVersion, b.ProvenAt, b.Witness = AnchorBindingPolicyVersion, admissionNow.Add(-time.Minute), []string{string(bindAlpha.Kind) + ":" + bindAlpha.ID}
	b.ProvenFreshness = AnchorFreshness{Recorded: true, OwnershipWatermark: "w1"}
	return b
}

// TestAdmitAnchorBindingDecisionTable: every outcome, the order the checks
// run in, and the live re-read asked exactly when every other check passed.
func TestAdmitAnchorBindingDecisionTable(t *testing.T) {
	readable := rememberedSubjectCheck{Check: SubjectSubstitutionRememberedReadable, Reason: CandidateVerificationValid}
	refused := rememberedSubjectCheck{Check: SubjectSubstitutionRememberedRefused, Reason: CandidateVerificationClaimLost}
	with := func(edit func(*anchorBindingAdmissionInput)) anchorBindingAdmissionInput {
		in := anchorBindingAdmissionInput{Held: admittableBinding(), Question: carryOriginSameQuestion, Now: admissionNow, MaxAge: DefaultAnchorBindingMaxAge, Movement: AnchorOwnershipUnmoved}
		edit(&in)
		return in
	}
	held := func(edit func(*AnchorBinding)) func(*anchorBindingAdmissionInput) {
		return func(in *anchorBindingAdmissionInput) { edit(&in.Held) }
	}
	for _, tc := range []struct {
		name        string
		in          anchorBindingAdmissionInput
		reread      *rememberedSubjectCheck
		want        AnchorBindingAdmission
		wantReReads int
	}{
		{"every check passes", with(func(*anchorBindingAdmissionInput) {}), &readable, AnchorBindingAdmissionAdmitted, 1},
		{"a pending binding passes the same checks", with(held(func(b *AnchorBinding) { b.State = AnchorBindingPendingWindowConfirmation })), &readable, AnchorBindingAdmissionAdmitted, 1},
		{"a contested binding passes the same checks", with(held(func(b *AnchorBinding) {
			b.State, b.ContenderKind, b.ContenderID = AnchorBindingContested, bindBeta.Kind, bindBeta.ID
		})), &readable, AnchorBindingAdmissionAdmitted, 1},
		{"nothing held", with(func(in *anchorBindingAdmissionInput) { in.Held = unboundFrom }), &readable, AnchorBindingAdmissionNotApplicable, 0},
		{"no policy", with(held(func(b *AnchorBinding) { b.PolicyVersion = "" })), &readable, AnchorBindingAdmissionRefusedPolicy, 0},
		{"another policy", with(held(func(b *AnchorBinding) { b.PolicyVersion = "anchor-binding.v1" })), &readable, AnchorBindingAdmissionRefusedPolicy, 0},
		{"policy is checked before the question", with(func(in *anchorBindingAdmissionInput) { in.Held.PolicyVersion, in.Question = "", carryOriginDrifted }), &readable, AnchorBindingAdmissionRefusedPolicy, 0},
		{"question changed", with(func(in *anchorBindingAdmissionInput) { in.Question = carryOriginDrifted }), &readable, AnchorBindingAdmissionRefusedQuestionChanged, 0},
		{"question indeterminate", with(func(in *anchorBindingAdmissionInput) { in.Question = carryOriginIndeterminateQuestion }), &readable, AnchorBindingAdmissionRefusedQuestionIndeterminate, 0},
		{"origin unreadable", with(func(in *anchorBindingAdmissionInput) { in.Question = carryOriginUnverifiable }), &readable, AnchorBindingAdmissionRefusedOriginUnverifiable, 0},
		{"question is checked before age", with(func(in *anchorBindingAdmissionInput) { in.Question, in.Held.ProvenAt = carryOriginDrifted, time.Time{} }), &readable, AnchorBindingAdmissionRefusedQuestionChanged, 0},
		{"age exactly the maximum", with(held(func(b *AnchorBinding) { b.ProvenAt = admissionNow.Add(-DefaultAnchorBindingMaxAge) })), &readable, AnchorBindingAdmissionAdmitted, 1},
		{"age past the maximum", with(held(func(b *AnchorBinding) { b.ProvenAt = admissionNow.Add(-DefaultAnchorBindingMaxAge - time.Nanosecond) })), &readable, AnchorBindingAdmissionRefusedExpired, 0},
		{"a configured shorter maximum", with(func(in *anchorBindingAdmissionInput) { in.MaxAge = 30 * time.Second }), &readable, AnchorBindingAdmissionRefusedExpired, 0},
		{"no proof time", with(held(func(b *AnchorBinding) { b.ProvenAt = time.Time{} })), &readable, AnchorBindingAdmissionRefusedExpired, 0},
		{"a proof dated after this turn", with(held(func(b *AnchorBinding) { b.ProvenAt = admissionNow.Add(time.Nanosecond) })), &readable, AnchorBindingAdmissionRefusedExpired, 0},
		{"age is checked before ownership", with(func(in *anchorBindingAdmissionInput) {
			in.Held.ProvenAt, in.Movement = time.Time{}, AnchorOwnershipMoved
		}), &readable, AnchorBindingAdmissionRefusedExpired, 0},
		{"ownership moved", with(func(in *anchorBindingAdmissionInput) { in.Movement = AnchorOwnershipMoved }), &readable, AnchorBindingAdmissionRefusedOwnershipMoved, 0},
		{"proof ownership unrecorded", with(func(in *anchorBindingAdmissionInput) { in.Movement = AnchorOwnershipProvenUnrecorded }), &readable, AnchorBindingAdmissionRefusedOwnershipUnverified, 0},
		{"this turn's ownership unrecorded", with(func(in *anchorBindingAdmissionInput) { in.Movement = AnchorOwnershipNotAcquired }), &readable, AnchorBindingAdmissionRefusedOwnershipUnverified, 0},
		{"no movement measured at all", with(func(in *anchorBindingAdmissionInput) { in.Movement = AnchorOwnershipNotApplicable }), &readable, AnchorBindingAdmissionRefusedOwnershipUnverified, 0},
		{"the re-read refuses", with(func(*anchorBindingAdmissionInput) {}), &refused, AnchorBindingAdmissionRefusedSubjectUnreadable, 1},
		{"no re-read wired", with(func(*anchorBindingAdmissionInput) {}), nil, AnchorBindingAdmissionRefusedSubjectUnreadable, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			if tc.reread != nil {
				answer := *tc.reread
				tc.in.Remembered = func() rememberedSubjectCheck { reads++; return answer }
			}
			got := admitAnchorBinding(tc.in)
			if got.Outcome != tc.want || reads != tc.wantReReads {
				t.Fatalf("admission = %s after %d re-reads, want %s after %d", got.Outcome, reads, tc.want, tc.wantReReads)
			}
			if reads == 0 && got.Remembered.Check != SubjectSubstitutionRememberedNotChecked {
				t.Fatalf("no re-read ran but the decision reports %+v", got.Remembered)
			}
			if reads == 1 && got.Remembered != *tc.reread {
				t.Fatalf("the decision dropped the re-read's answer: %+v, want %+v", got.Remembered, *tc.reread)
			}
		})
	}
}

// TestAdmissionComparesTheProvingTurnsQuestionNotTheParents: a carry is
// admitted against the question of the turn that PROVED the identity. A
// parent that asked this turn's question but carries a binding proven under
// another question is refused, so a binding cannot be laundered through an
// intermediate turn.
func TestAdmissionComparesTheProvingTurnsQuestionNotTheParents(t *testing.T) {
	const otherQuestion = "Which repositories changed their owners?"
	launder := func(store *staticResultStore) {
		for id, result := range store.results {
			if result.Question != otherQuestion {
				continue
			}
			for _, state := range store.states {
				editBindingMember(state, func(b *AnchorBinding) {
					if b.active() {
						b.OriginResultID = id
					}
				})
			}
		}
	}
	turns := runAnchorProbe(t, []anchorProbeStep{
		{request: func([]anchorProbeTurn) InvestigationRequest {
			r := needTurnRequest("request_launder_zero", true)
			r.Question = otherQuestion
			return r
		}, response: emptyProbeResponse()},
		{request: firstTurnRequest("request_launder_one", true), response: identityProvenResponse(probeAlpha)},
		{request: followUp("request_launder_two", "", nil), seed: launder, verified: true, response: emptyProbeResponse()},
	})
	line := turns[2].transitions[0]
	if line.Admission != AnchorBindingAdmissionRefusedQuestionChanged || line.From.OriginResultID != turns[0].result.ResultID {
		t.Fatalf("line admission %s from origin %s, want refused_question_changed against the proving turn %s", line.Admission, line.From.OriginResultID, turns[0].result.ResultID)
	}
	control := runAnchorProbe(t, []anchorProbeStep{
		{request: firstTurnRequest("request_launder_ctl_one", true), response: identityProvenResponse(probeAlpha)},
		{request: followUp("request_launder_ctl_two", "", nil), verified: true, response: emptyProbeResponse()},
		{request: followUp("request_launder_ctl_three", "", nil), verified: true, response: emptyProbeResponse()},
	})
	if line := control[2].transitions[0]; line.Admission != AnchorBindingAdmissionAdmitted || line.From.OriginResultID != control[0].result.ResultID {
		t.Fatalf("control line admission %s origin %s, want admitted against the first turn across two hops", line.Admission, line.From.OriginResultID)
	}
}

// TestTheAdmissionReReadsTheHeldIdentityAtThisTurnsBinding: the live re-read
// is asked for the held identity, at this turn's graph binding, for this
// principal, once; a refusal starts the binder unbound.
func TestTheAdmissionReReadsTheHeldIdentityAtThisTurnsBinding(t *testing.T) {
	rig := newAnchorProbeRig(t, false)
	rig.interpreter.read("", false)
	one := rig.turn(t, needTurnRequest("request_reread_one", true), identityProvenResponse(probeAlpha))
	type call struct {
		org     string
		binding ResolvedGraphBinding
		kind    contractsv1.ContextFabricSubjectKind
		id      string
	}
	var calls []call
	rig.engine.candidateVerifier = func(_ context.Context, principal storage.Principal, _ RequestedScope, binding ResolvedGraphBinding, kind contractsv1.ContextFabricSubjectKind, id string) (bool, CandidateVerificationReason) {
		calls = append(calls, call{principal.OrgID, binding, kind, id})
		return false, CandidateVerificationClaimLost
	}
	two := rig.turn(t, continuingNeedTurn(needTurnRequest("request_reread_two", true), one.result.ResultID), emptyProbeResponse())
	want := call{acceptancePrincipal().OrgID, ResolvedGraphBinding{GraphKey: "need-turn-key", Epoch: 0}, probeAlpha.Kind, probeAlpha.CanonicalID}
	if len(calls) == 0 || calls[0] != want {
		t.Fatalf("re-read calls = %+v, want the first to be %+v", calls, want)
	}
	line := two.transitions[0]
	if line.Admission != AnchorBindingAdmissionRefusedSubjectUnreadable || line.AdmissionRemembered.Check != SubjectSubstitutionRememberedRefused || line.AdmissionRemembered.Reason != CandidateVerificationClaimLost {
		t.Fatalf("line admission %s re-read %+v, want refused_subject_unreadable carrying the verifier's own reason", line.Admission, line.AdmissionRemembered)
	}
	if binding := *bindingMember(two.saved); binding.State != AnchorBindingUnbound {
		t.Fatalf("binding = %+v, want unbound after a refused re-read", binding)
	}
}

// TestAnAgedProofIsRefusedAtTheBoundAndAdmittedWithinIt drives the age check
// through the engine: the stored proof time is moved back to the bound, then
// past it.
func TestAnAgedProofIsRefusedAtTheBoundAndAdmittedWithinIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		back time.Duration
		want AnchorBindingAdmission
	}{
		{"at the bound", DefaultAnchorBindingMaxAge, AnchorBindingAdmissionAdmitted},
		{"one second past", DefaultAnchorBindingMaxAge + time.Second, AnchorBindingAdmissionRefusedExpired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			turns := runAnchorProbe(t, []anchorProbeStep{
				{request: firstTurnRequest("request_age_one", true), response: identityProvenResponse(probeAlpha)},
				{request: followUp("request_age_two", "", nil), verified: true, seed: mutateStoredBindings(func(s *PersistedSemanticState) {
					editBindingMember(s, func(b *AnchorBinding) { b.ProvenAt = b.ProvenAt.Add(-tc.back) })
				}), response: emptyProbeResponse()},
			})
			line := turns[1].transitions[0]
			if line.Admission != tc.want || line.AdmissionAgeSeconds != int64(tc.back/time.Second) {
				t.Fatalf("admission %s age %ds, want %s age %ds", line.Admission, line.AdmissionAgeSeconds, tc.want, int64(tc.back/time.Second))
			}
		})
	}
}

// TestAProvenBindingCarriesItsPolicyProofTimeAndWitness: an identity a turn
// proves is stamped with the policy, the turn's clock and every identity the
// turn proved; two gated runs that differ only in the identity they proved
// hold bindings whose witnesses say so.
func TestAProvenBindingCarriesItsPolicyProofTimeAndWitness(t *testing.T) {
	for _, subject := range []SubjectRef{probeAlpha, probeBeta} {
		turns := runAnchorProbe(t, []anchorProbeStep{{request: firstTurnRequest("request_witness_one", false), windowed: true, response: identityProvenResponse(subject)}})
		binding := *bindingMember(turns[0].saved)
		want := []string{string(subject.Kind) + ":" + subject.CanonicalID}
		if binding.State != AnchorBindingPendingWindowConfirmation || binding.PolicyVersion != AnchorBindingPolicyVersion ||
			!binding.ProvenAt.Equal(time.Unix(500, 0)) || !reflect.DeepEqual(binding.Witness, want) {
			t.Fatalf("gated binding = %+v, want pending under %s at the rig clock with witness %v", binding, AnchorBindingPolicyVersion, want)
		}
	}
	witnessless := admittableBinding()
	witnessless.Witness = []string{"repository:repository:bind-other"}
	if ValidateAnchorBinding(witnessless) == nil {
		t.Fatal("an identity-proven binding whose witness does not name the held identity validated")
	}
	legacy := heldBinding(AnchorBindingBound, bindAlpha)
	if err := ValidateAnchorBinding(legacy); err != nil {
		t.Fatalf("a binding written before the policy existed no longer validates (it must read back and be refused for its policy): %v", err)
	}
	oversized := admittableBinding()
	oversized.Witness = make([]string, AnchorBindingWitnessMax+1)
	for i := range oversized.Witness {
		oversized.Witness[i] = "repository:x"
	}
	if ValidateAnchorBinding(oversized) == nil {
		t.Fatal("a witness over its bound validated")
	}
	if got := anchorBindingWitness(anchorBindingProposal{Proven: make([]anchorRef, AnchorBindingWitnessMax+3)}); len(got) != AnchorBindingWitnessMax {
		t.Fatalf("witness of %d proven identities = %d, want the bound %d", AnchorBindingWitnessMax+3, len(got), AnchorBindingWitnessMax)
	}
}

// TestTheMaximumAgeIsBounded: zero selects the default; anything outside the
// bounds is refused at construction.
func TestTheMaximumAgeIsBounded(t *testing.T) {
	for age, ok := range map[time.Duration]bool{
		0: true, MinAnchorBindingMaxAge: true, MaxAnchorBindingMaxAge: true,
		MinAnchorBindingMaxAge - time.Nanosecond: false, MaxAnchorBindingMaxAge + time.Nanosecond: false, -time.Minute: false,
	} {
		if err := validateAnchorBindingMaxAge(age); (err == nil) != ok {
			t.Errorf("max age %s: error %v, want accepted=%v", age, err, ok)
		}
	}
	if got := (&Engine{}).anchorBindingMaxAge(); got != DefaultAnchorBindingMaxAge {
		t.Fatalf("unset max age = %s, want %s", got, DefaultAnchorBindingMaxAge)
	}
	_, err := NewEngine(EngineDependencies{Interpreter: &anchorProbeInterpreter{}, Graph: &needTurnGraph{}, Facts: failingFactReader{t: t}, Synthesizer: synthesizerFunc(nil)},
		EngineOptions{ServiceVersion: "max-age", NewResultID: resultIDSequence(), AnchorBindingMaxAge: time.Second})
	if err == nil {
		t.Fatal("NewEngine accepted a max age below the bound")
	}
}

// TestAParentEchoOfTheCommittedSubjectNeverChangesTheRequestIdentity: a hint
// echoing the parent's committed subject, on a hinted or an unhinted root,
// mixed with another hint, across three turns, leaves the request identity
// of an unchanged question unchanged.
func TestAParentEchoOfTheCommittedSubjectNeverChangesTheRequestIdentity(t *testing.T) {
	echo := SubjectHint{Kind: probeAlpha.Kind, ID: probeAlpha.CanonicalID, Label: probeAlpha.Label, Source: "ask-dev"}
	choice := SubjectHint{Kind: probeBeta.Kind, ID: probeBeta.CanonicalID, Label: probeBeta.Label, Source: "ask-dev"}
	for name, hops := range map[string][][]SubjectHint{
		"unhinted root":       {nil, {echo}, {echo}},
		"hinted root":         {{echo}, {echo}, {echo}},
		"mixed echo and hint": {nil, {echo, choice}, {choice, echo}},
	} {
		var first SemanticRequestIdentity
		for i, hints := range hops {
			request := needTurnRequest("request_echo_identity", true)
			request.RequestedScope.SubjectHints = hints
			got := SemanticRequestIdentityOf(request, "")
			if i == 0 {
				first = got
				continue
			}
			if got != first {
				t.Errorf("%s: hop %d identity %+v differs from the root's %+v", name, i+1, got, first)
			}
		}
	}
}
