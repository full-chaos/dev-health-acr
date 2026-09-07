package contextfabric

// CHAOS-5442, THE WIRE HALF. These assertions name a field that does not
// exist at the parent commit, so they cannot be shown red there -- a test
// file that fails to BUILD proves nothing about behaviour. They are proven by
// MUTATION instead (the battery's 5442 arms), which is the same split
// lane-5385-seam used for FrameGate's own pins and for the same reason.

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// THE FIELD ITSELF, on both surfaces that carry it.
//
// Asserting the RESULT alone would pass against a build that never populated
// the completeness block, and the block is what the bounded consumer and the
// answer projection actually read; asserting the block alone would pass
// against a build whose stored result carried nothing, so the first
// recomputation on a stored read would drop it. Both, or neither means
// anything.
func TestARefusedFrameNamesItsBasisOnTheServedDocument(t *testing.T) {
	t.Parallel()
	result, _ := investigateUnderRefusingGate(t, FrameGate{
		Outcome:            FrameGateRefusedBasis,
		RefuseBasis:        CohortMemberKindUnservable,
		DeclaredMemberKind: contractsv1.ContextFabricSubjectRepository,
	})
	if got := result.RefusalBasis; got != contractsv1.ContextFabricRefusalBasisMemberKindUnservable {
		t.Fatalf("result.RefusalBasis = %q, want %q -- the gate decided this before retrieval ran and the served document is where a caller can read it", got, contractsv1.ContextFabricRefusalBasisMemberKindUnservable)
	}
	if got := result.Completeness.RefusalBasis; got != contractsv1.ContextFabricRefusalBasisMemberKindUnservable {
		t.Fatalf("result.Completeness.RefusalBasis = %q, want %q -- the disclosure block is what the answer projection copies, so a basis missing from it never reaches a bounded consumer", got, contractsv1.ContextFabricRefusalBasisMemberKindUnservable)
	}
	// The channel classification is UNCHANGED and that is deliberate: the
	// two fields answer different questions, and a change that moved the
	// refusal into terminal_reason would have destroyed the channel
	// information instead of adding the decision.
	if got := result.Completeness.TerminalReason; got != contractsv1.ContextFabricTerminalReasonLimitationDisclosed {
		t.Fatalf("Completeness.TerminalReason = %q, want %q -- the basis is orthogonal to the channel, never a replacement for it", got, contractsv1.ContextFabricTerminalReasonLimitationDisclosed)
	}
}

// THE SENTENCE NAMES THE KIND, and it is recognised as service-authored.
//
// Recognition is not cosmetic. An unrecognised disclosure is DISPLACEABLE:
// the next composer that needs a limitation slot may drop it, and the served
// answer then states nothing about having been refused -- which is the
// shipped round-3 defect the limitations registry's own doc comment records.
func TestTheRefusalSentenceNamesTheKindAndIsServiceAuthored(t *testing.T) {
	t.Parallel()
	result, _ := investigateUnderRefusingGate(t, FrameGate{
		Outcome:            FrameGateRefusedBasis,
		RefuseBasis:        CohortMemberKindUnservable,
		DeclaredMemberKind: contractsv1.ContextFabricSubjectRepository,
	})
	want := contractsv1.ContextFabricRefusalBasisLimitation(contractsv1.ContextFabricSubjectRepository, contractsv1.ContextFabricRefusalBasisMemberKindUnservable)
	found := false
	for _, limitation := range result.Limitations {
		if limitation == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("result.Limitations = %#v, want it to carry %q -- the declared kind is the one thing the asker can change about the question", result.Limitations, want)
	}
	if !contractsv1.IsContextFabricServiceAuthoredLimitation(want) {
		t.Fatal("the refusal disclosure is not recognised as service-authored, so the next composer needing a slot may displace it and the answer will state nothing about having been refused")
	}
}

// THE OTHER REFUSING OUTCOME. An invariant-violated frame has no kind to
// name, and it must still disclose a basis rather than falling through to the
// empty-graph sentence -- otherwise the fix closes one refusing arm and
// leaves its neighbour open, which is how a class fix becomes a site fix.
func TestAFrameRefusedOnAnInvariantAlsoNamesABasis(t *testing.T) {
	t.Parallel()
	result, telemetry := investigateUnderRefusingGate(t, FrameGate{
		Outcome:         FrameGateRejectedInvalid,
		FailedInvariant: FrameInvariantI6,
	})
	if got := result.RefusalBasis; got != contractsv1.ContextFabricRefusalBasisFrameInvariantViolated {
		t.Fatalf("result.RefusalBasis = %q, want %q", got, contractsv1.ContextFabricRefusalBasisFrameInvariantViolated)
	}
	for _, limitation := range result.Limitations {
		if limitation == noMatchLimitationUnproven {
			t.Fatalf("result.Limitations = %#v carries the empty-pool wording on a frame refused for violating an invariant", result.Limitations)
		}
	}
	if want := []string{"frame_gate_refused"}; !stringSlicesEqual(telemetry.subjectlessTerminalReasons, want) {
		t.Fatalf("subjectlessTerminalReasons = %#v, want %#v", telemetry.subjectlessTerminalReasons, want)
	}
}

// THE EXPLICIT-NONE ARM. An unrefused terminal must emit the basis key with
// the token "none", never omit it: a key that appears only on refusals is
// indistinguishable, on an ordinary line, from a build that stopped emitting
// it at all. Missing is not none.
func TestAnUnrefusedTerminalReportsAnExplicitNoneBasis(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	engine := mustEngineForTerminalReasonTest(t, graphReaderStub{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
	}, telemetry)
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.RefusalBasis != "" {
		t.Fatalf("result.RefusalBasis = %q on a turn nothing refused, want empty -- an absent basis is what says 'not refused', so a value here would make every ordinary answer look refused", result.RefusalBasis)
	}
	if want := []string{"none"}; !stringSlicesEqual(telemetry.subjectlessTerminalRefusalBases, want) {
		t.Fatalf("subjectlessTerminalRefusalBases = %#v, want %#v -- the ordinary line carries the explicit token, never a missing key", telemetry.subjectlessTerminalRefusalBases, want)
	}
}

// THE CROSS-LAYER AGREEMENT TEST, in both directions, over the WHOLE gate
// vocabulary rather than the members this change happened to touch.
//
// Refuses() and RefusalBasis() are two readings of one decision, and the
// failure they must make impossible is a gate that refuses a turn while the
// served document says nothing was refused. A future FrameGateOutcome added
// without a line in RefusalBasis()'s allow-list lands in its default arm and
// discloses `unspecified`, which this asserts is a refusal rather than a
// silence -- so the vocabulary can grow without the disclosure going quiet.
func TestTheGateAndTheWireBasisAgree(t *testing.T) {
	t.Parallel()
	for _, outcome := range FrameGateOutcomeVocabulary() {
		gate := FrameGate{Outcome: outcome}
		if outcome == FrameGateRefusedBasis {
			gate.RefuseBasis = CohortMemberKindUnservable
		}
		basis := gate.RefusalBasis()
		if gate.Refuses() != (basis != "") {
			t.Errorf("outcome %q: Refuses()=%t but RefusalBasis()=%q -- a refusing gate must always name a basis and an allowing one must never name one", outcome, gate.Refuses(), basis)
		}
		if basis != "" && !contractsv1.ValidContextFabricRefusalBasis(basis) {
			t.Errorf("outcome %q: RefusalBasis()=%q is not a wire vocabulary member", outcome, basis)
		}
	}
	// The unrecognised-member arm, exercised rather than argued. A member
	// this vocabulary does not name must still disclose, or the next one
	// added refuses turns in silence.
	unknown := FrameGate{Outcome: FrameGateOutcome("a_member_nobody_has_written_yet")}
	if !unknown.Refuses() {
		t.Fatal("an unrecognised gate outcome must refuse -- the permissive default is the failure this seam exists to remove")
	}
	if got := unknown.RefusalBasis(); got != contractsv1.ContextFabricRefusalBasisUnspecified {
		t.Errorf("an unrecognised refusing outcome discloses %q, want %q -- a refusal that reaches the wire with an empty basis is indistinguishable from a turn that was never refused", got, contractsv1.ContextFabricRefusalBasisUnspecified)
	}
}
