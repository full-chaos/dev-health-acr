package contextfabric

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestAWindowGatedTurnStampsItsPendingProofWithTheOwnershipItStoodOn: the
// offers-only resolution a window gate discards still proves the anchor the
// binding holds pending, and the binding records the ownership state that
// proof was read at. The rig wires no answer-reuse snapshotter at all, so
// the read does not depend on reuse.
func TestAWindowGatedTurnStampsItsPendingProofWithTheOwnershipItStoodOn(t *testing.T) {
	on, off := newAnchorProbeRig(t, false), newAnchorProbeRig(t, true)
	if on.engine.reuseSnapshotter != nil || on.engine.reuseEpochSnapshotter != nil {
		t.Fatal("premise: the rig wires answer reuse")
	}
	for _, rig := range []*anchorProbeRig{on, off} {
		rig.interpreter.read("", true)
	}
	gated := on.turn(t, needTurnRequest("request_fresh_gated", false), identityProvenResponse(probeAlpha))
	offGated := off.turn(t, needTurnRequest("request_fresh_gated", false), identityProvenResponse(probeAlpha))
	if string(gated.served) != string(offGated.served) {
		t.Fatalf("served bytes differ with the shadow on:\n on=%s\noff=%s", gated.served, offGated.served)
	}
	if gated.result.Status != InvestigationClarificationRequired {
		t.Fatalf("premise: status %q, want the window gate's clarification", gated.result.Status)
	}
	if on.ownership.calls != 1 {
		t.Fatalf("ownership reads on the gated turn = %d, want exactly 1", on.ownership.calls)
	}
	binding := *bindingMember(gated.saved)
	want := AnchorFreshness{Recorded: true, ServingEpoch: 0, OwnershipWatermark: "ownership-w1"}
	if binding.State != AnchorBindingPendingWindowConfirmation || binding.ProvenFreshness != want {
		t.Fatalf("gated binding = %+v, want alpha pending with proven freshness %+v", binding, want)
	}
	line := gated.transitions[0]
	if line.FreshnessAcquisition != AnchorFreshnessAcquired || line.Freshness != want || line.OwnershipMovement != AnchorOwnershipNotApplicable {
		t.Fatalf("gated line = %+v, want acquired %+v with nothing held to compare", line, want)
	}
	if off.ownership.calls != 0 {
		t.Fatalf("the shadow-off arm read ownership %d times, want 0", off.ownership.calls)
	}
}

// TestTheOwnershipReadPrecedesEveryGraphReadThatCanProveAnAnchor: on a gated
// turn and on a decisive turn, the read happens before ResolveSubjects runs,
// so a proof is never stamped with ownership read after it.
func TestTheOwnershipReadPrecedesEveryGraphReadThatCanProveAnAnchor(t *testing.T) {
	for _, tc := range []struct {
		name     string
		windowed bool
		stated   bool
	}{
		{"window gated", true, false},
		{"decisive", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newAnchorProbeRig(t, false)
			resolvesBeforeRead := -1
			rig.engine.anchorFreshness = anchorFreshnessFunc(func(context.Context, string) (AnchorFreshnessRead, error) {
				resolvesBeforeRead = len(rig.graph.calls)
				return probeOwnershipW1, nil
			})
			rig.interpreter.read("", tc.windowed)
			rig.turn(t, needTurnRequest("request_fresh_order", tc.stated), identityProvenResponse(probeAlpha))
			if resolvesBeforeRead != 0 {
				t.Fatalf("ResolveSubjects ran %d times before the ownership read, want 0 (read never ran: -1)", resolvesBeforeRead)
			}
			if len(rig.graph.calls) == 0 {
				t.Fatal("premise: the turn never resolved")
			}
		})
	}
}

type anchorFreshnessFunc func(context.Context, string) (AnchorFreshnessRead, error)

func (f anchorFreshnessFunc) ReadAnchorFreshness(ctx context.Context, orgID string) (AnchorFreshnessRead, error) {
	return f(ctx, orgID)
}

// TestTheHeldBindingCarriesTheOwnershipItWasProvenUnderAcrossTurns: a
// follow-up compares the held binding's proven freshness with its own read,
// and a binding keeps the freshness of the turn that proved it.
func TestTheHeldBindingCarriesTheOwnershipItWasProvenUnderAcrossTurns(t *testing.T) {
	w2 := probeOwnershipW1
	w2.OwnershipWatermark = "ownership-w2"
	otherEpoch := probeOwnershipW1
	otherEpoch.ServingEpoch = 3
	set := func(read AnchorFreshnessRead, err error) func(*probeOwnership) {
		return func(o *probeOwnership) { o.read, o.err = read, err }
	}
	proven := AnchorFreshness{Recorded: true, OwnershipWatermark: "ownership-w1"}
	for _, tc := range []struct {
		name        string
		first       func(*probeOwnership)
		second      func(*probeOwnership)
		movement    AnchorOwnershipMovement
		acquisition AnchorFreshnessAcquisition
		admission   AnchorBindingAdmission
	}{
		{"same ownership", set(probeOwnershipW1, nil), set(probeOwnershipW1, nil), AnchorOwnershipUnmoved, AnchorFreshnessAcquired, AnchorBindingAdmissionAdmitted},
		{"ownership moved", set(probeOwnershipW1, nil), set(w2, nil), AnchorOwnershipMoved, AnchorFreshnessAcquired, AnchorBindingAdmissionRefusedOwnershipMoved},
		{"read failed", set(probeOwnershipW1, nil), set(AnchorFreshnessRead{}, errors.New("read failed")), AnchorOwnershipNotAcquired, AnchorFreshnessReadFailed, AnchorBindingAdmissionRefusedOwnershipUnverified},
		{"another epoch", set(probeOwnershipW1, nil), set(otherEpoch, nil), AnchorOwnershipNotAcquired, AnchorFreshnessEpochMismatch, AnchorBindingAdmissionRefusedOwnershipUnverified},
		{"proven under no recorded ownership", set(AnchorFreshnessRead{Recorded: false}, nil), set(probeOwnershipW1, nil), AnchorOwnershipProvenUnrecorded, AnchorFreshnessAcquired, AnchorBindingAdmissionRefusedOwnershipUnverified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			turns := runAnchorProbe(t, []anchorProbeStep{
				{request: firstTurnRequest("request_fresh_carry_one", true), ownership: tc.first, response: identityProvenResponse(probeAlpha)},
				{request: followUp("request_fresh_carry_two", "", nil), ownership: tc.second, verified: true, response: emptyProbeResponse()},
			})
			line := turns[1].transitions[0]
			if line.OwnershipMovement != tc.movement || line.FreshnessAcquisition != tc.acquisition || line.Admission != tc.admission {
				t.Fatalf("follow-up line movement/acquisition/admission = %s/%s/%s, want %s/%s/%s", line.OwnershipMovement, line.FreshnessAcquisition, line.Admission, tc.movement, tc.acquisition, tc.admission)
			}
			binding := *bindingMember(turns[1].saved)
			if tc.admission == AnchorBindingAdmissionAdmitted {
				if binding.Reason != AnchorBindingReasonCarriedSilent || binding.ProvenFreshness != proven {
					t.Fatalf("follow-up binding = %+v, want carried_silent keeping proven freshness %+v", binding, proven)
				}
				return
			}
			if binding.State != AnchorBindingUnbound {
				t.Fatalf("follow-up binding = %+v, want unbound: a refused carry is not the starting point", binding)
			}
		})
	}
}

// TestAcquireAnchorFreshnessNamesEveryReadOutcome is the acquisition's
// decision table; only an acquired read stamps a freshness.
func TestAcquireAnchorFreshnessNamesEveryReadOutcome(t *testing.T) {
	oversize := probeOwnershipW1
	oversize.OwnershipWatermark = strings.Repeat("w", SemanticStateMaxTermBytes+1)
	atBound := probeOwnershipW1
	atBound.OwnershipWatermark = strings.Repeat("w", SemanticStateMaxTermBytes)
	cases := []struct {
		name   string
		reader AnchorFreshnessReader
		want   AnchorFreshnessAcquisition
		stamp  AnchorFreshness
	}{
		{"unwired", nil, AnchorFreshnessUnwired, AnchorFreshness{}},
		{"read failed", anchorFreshnessFunc(func(context.Context, string) (AnchorFreshnessRead, error) {
			return probeOwnershipW1, errors.New("boom")
		}), AnchorFreshnessReadFailed, AnchorFreshness{}},
		{"epoch mismatch", &probeOwnership{read: AnchorFreshnessRead{ServingEpoch: 8, OwnershipWatermark: "w", Recorded: true}}, AnchorFreshnessEpochMismatch, AnchorFreshness{}},
		{"source absent", &probeOwnership{read: AnchorFreshnessRead{ServingEpoch: 7}}, AnchorFreshnessSourceAbsent, AnchorFreshness{}},
		{"unstorable", &probeOwnership{read: AnchorFreshnessRead{ServingEpoch: 7, OwnershipWatermark: oversize.OwnershipWatermark, Recorded: true}}, AnchorFreshnessUnstorable, AnchorFreshness{}},
		{"at the bound", &probeOwnership{read: AnchorFreshnessRead{ServingEpoch: 7, OwnershipWatermark: atBound.OwnershipWatermark, Recorded: true}}, AnchorFreshnessAcquired, AnchorFreshness{Recorded: true, ServingEpoch: 7, OwnershipWatermark: atBound.OwnershipWatermark}},
		{"empty watermark recorded", &probeOwnership{read: AnchorFreshnessRead{ServingEpoch: 7, Recorded: true}}, AnchorFreshnessAcquired, AnchorFreshness{Recorded: true, ServingEpoch: 7}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine := &Engine{anchorFreshness: tc.reader}
			tracker := &anchorBindingTracker{epoch: 7}
			engine.acquireAnchorFreshness(context.Background(), "org_fresh", tracker)
			if tracker.acquisition() != tc.want || tracker.freshness != tc.stamp {
				t.Fatalf("acquisition/freshness = %s/%+v, want %s/%+v", tracker.acquisition(), tracker.freshness, tc.want, tc.stamp)
			}
			if err := ValidateAnchorBinding(AnchorBinding{State: AnchorBindingBound, Kind: SubjectRepository, CanonicalID: "repository:x", Proof: AnchorBindingProofIdentityProven, Reason: AnchorBindingReasonIdentityProven, ProvenFreshness: tracker.freshness}); err != nil {
				t.Fatalf("a binding stamped with the acquired freshness does not validate: %v", err)
			}
		})
	}
	engine := &Engine{anchorFreshness: &probeOwnership{read: probeOwnershipW1}}
	engine.acquireAnchorFreshness(context.Background(), "org_fresh", nil)
	if engine.anchorFreshness.(*probeOwnership).calls != 0 {
		t.Fatal("a nil tracker (the shadow off) read ownership")
	}
	if (&anchorBindingTracker{}).acquisition() != AnchorFreshnessNotAttempted {
		t.Fatal("a tracker that never reached the read does not say not_attempted")
	}
}

// TestAProvenFreshnessIsPersistedOnlyWhenRecorded: an unrecorded freshness
// encodes exactly as a binding written before the field existed, a recorded
// one round-trips, and validation refuses every inconsistent shape.
func TestAProvenFreshnessIsPersistedOnlyWhenRecorded(t *testing.T) {
	held := AnchorBinding{State: AnchorBindingBound, Kind: SubjectRepository, CanonicalID: "repository:x", Proof: AnchorBindingProofIdentityProven, Reason: AnchorBindingReasonIdentityProven, GraphEpoch: 2}
	raw, err := json.Marshal(held)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "proven_freshness") {
		t.Fatalf("an unrecorded freshness is encoded: %s", raw)
	}
	held.ProvenFreshness = AnchorFreshness{Recorded: true, ServingEpoch: 2}
	state := BuildSemanticState(SemanticStateInput{
		Outcome:         QuestionFamilyOutcome{Family: QuestionFamilyUnclassified, Source: QuestionFamilySourceNone},
		FamilyVersion:   QuestionFamilyTableVersion,
		RequestIdentity: SemanticRequestIdentityOf(validInvestigationRequest(), ""),
	})
	withBinding, err := withAnchorBindingMember(state, held)
	if err != nil {
		t.Fatal(err)
	}
	back, present, err := storedAnchorBinding(withBinding)
	if err != nil || !present || !reflect.DeepEqual(back, held) {
		t.Fatalf("round trip = %+v present=%v err=%v, want %+v", back, present, err, held)
	}
	for name, bad := range map[string]AnchorBinding{
		"unrecorded with an epoch":    withFreshness(held, AnchorFreshness{ServingEpoch: 3}),
		"unrecorded with a watermark": withFreshness(held, AnchorFreshness{OwnershipWatermark: "w"}),
		"negative epoch":              withFreshness(held, AnchorFreshness{Recorded: true, ServingEpoch: -1}),
		"oversize watermark":          withFreshness(held, AnchorFreshness{Recorded: true, OwnershipWatermark: strings.Repeat("w", SemanticStateMaxTermBytes+1)}),
		"unbound with a freshness":    {State: AnchorBindingUnbound, Proof: AnchorBindingProofNone, Reason: AnchorBindingReasonNoProof, ProvenFreshness: AnchorFreshness{Recorded: true}},
	} {
		if ValidateAnchorBinding(bad) == nil {
			t.Errorf("%s: validated %+v", name, bad)
		}
	}
}

func withFreshness(b AnchorBinding, f AnchorFreshness) AnchorBinding {
	b.ProvenFreshness = f
	return b
}

// TestAnchorOwnershipMovementTable: unmoved needs both sides recorded and
// equal; nothing compares as unmoved by default.
func TestAnchorOwnershipMovementTable(t *testing.T) {
	w1 := AnchorFreshness{Recorded: true, ServingEpoch: 1, OwnershipWatermark: "w1"}
	held := AnchorBinding{State: AnchorBindingBound, Kind: SubjectRepository, CanonicalID: "repository:x", Proof: AnchorBindingProofIdentityProven, ProvenFreshness: w1}
	unbound := AnchorBinding{State: AnchorBindingUnbound}
	for _, tc := range []struct {
		name string
		from AnchorBinding
		now  AnchorFreshness
		want AnchorOwnershipMovement
	}{
		{"nothing held", unbound, w1, AnchorOwnershipNotApplicable},
		{"held without proven freshness", withFreshness(held, AnchorFreshness{}), w1, AnchorOwnershipProvenUnrecorded},
		{"nothing acquired", held, AnchorFreshness{}, AnchorOwnershipNotAcquired},
		{"equal", held, w1, AnchorOwnershipUnmoved},
		{"watermark moved", held, AnchorFreshness{Recorded: true, ServingEpoch: 1, OwnershipWatermark: "w2"}, AnchorOwnershipMoved},
		{"epoch moved", held, AnchorFreshness{Recorded: true, ServingEpoch: 2, OwnershipWatermark: "w1"}, AnchorOwnershipMoved},
		{"pending held", func() AnchorBinding { b := held; b.State = AnchorBindingPendingWindowConfirmation; return b }(), w1, AnchorOwnershipUnmoved},
	} {
		if got := anchorOwnershipMovement(tc.from, tc.now); got != tc.want {
			t.Errorf("%s: movement = %s, want %s", tc.name, got, tc.want)
		}
	}
	if anchorFreshnessDigest(AnchorFreshness{}) != "" || anchorFreshnessEpoch(AnchorFreshness{}) != -1 {
		t.Error("an unrecorded freshness renders a digest or an epoch")
	}
	if a, b := anchorFreshnessDigest(w1), anchorFreshnessDigest(AnchorFreshness{Recorded: true, OwnershipWatermark: "w2"}); a == b || len(a) != 16 || strings.Contains(a, "w1") {
		t.Errorf("digests %q / %q: want 16 hex digits that differ and never the watermark", a, b)
	}
}
