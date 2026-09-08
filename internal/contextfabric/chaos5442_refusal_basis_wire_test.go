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

// THE DECLARED KIND IS ONE VALUE THAT CROSSES FOUR HOPS, and until the
// battery said so nothing asserted it crossed any of them.
//
// Three arms survived the 34123455192 battery together -- the gate dropping
// it at the decision, the receipt dropping it, and the rebuilt gate dropping
// it -- and they are ONE gap rather than three: every pin in this file and in
// frame_gate_test.go that touches a refusing gate BUILDS the FrameGate as a
// literal and hands the literal to the engine, so the production chain that
// derives the kind, persists it and restores it was never driven end to end.
// Each hop is asserted separately here rather than only at the end, so a
// regression names WHICH hop dropped the value instead of only that the
// sentence went wrong.
//
// The last hop is the one that matters to a caller: an unrecognised
// disclosure is displaceable, so a kind lost anywhere upstream ends as an
// answer that says nothing about having been refused.
func TestTheDeclaredMemberKindSurvivesEveryHopOntoTheServedSentence(t *testing.T) {
	t.Parallel()
	frame := unservableMemberKindFrame()

	decided := DecideFrameGate(ValidateFrame(*frame, nil, ""), true)
	if decided.Outcome != FrameGateRefusedBasis {
		t.Fatalf("DecideFrameGate outcome = %q, want %q -- this pin is about the kind a REFUSING gate names, so any other verdict measures something else", decided.Outcome, FrameGateRefusedBasis)
	}
	if !contractsv1.ValidContextFabricSubjectKind(decided.DeclaredMemberKind) {
		t.Fatalf("DecideFrameGate DeclaredMemberKind = %q, want a subject-kind registry member -- the kind is the one thing the asker can change about the question, and a kind the registry does not name composes a sentence the recogniser rejects", decided.DeclaredMemberKind)
	}

	receipt := ModelExecutionReceipt{QuestionFrame: frame}
	interpreter := RuntimeQuestionInterpreter{}
	interpreter.resolveFrame(context.Background(), storage.Principal{OrgID: "org_1"}, &receipt, "")
	if receipt.FrameGateDeclaredMemberKind != decided.DeclaredMemberKind {
		t.Fatalf("receipt FrameGateDeclaredMemberKind = %q, want %q -- the receipt is the only carrier between interpretation and the engine, so a kind dropped here is gone for the rest of the turn", receipt.FrameGateDeclaredMemberKind, decided.DeclaredMemberKind)
	}

	outcome := interpreter.recordFamilyResolution(context.Background(), storage.Principal{OrgID: "org_1"}, InterpretedQuestion{}, receipt)
	if outcome.Gate.DeclaredMemberKind != receipt.FrameGateDeclaredMemberKind {
		t.Fatalf("carried gate DeclaredMemberKind = %q while the receipt recorded %q -- the engine reads the CARRIED gate, so a kind dropped on the rebuild never reaches the disclosure", outcome.Gate.DeclaredMemberKind, receipt.FrameGateDeclaredMemberKind)
	}

	sentence := refusalLimitation(outcome.Gate, outcome.Gate.RefusalBasis())
	want := contractsv1.ContextFabricRefusalBasisLimitation(decided.DeclaredMemberKind, contractsv1.ContextFabricRefusalBasisMemberKindUnservable)
	if sentence != want {
		t.Fatalf("served sentence = %q, want %q -- a lost kind falls the unservable arm back to the invariant wording, which tells the asker nothing about the population they named", sentence, want)
	}
	if !contractsv1.IsContextFabricServiceAuthoredLimitation(sentence) {
		t.Fatal("the composed sentence is not recognised as service-authored, so the next composer needing a limitation slot may displace it and the answer will state nothing about having been refused")
	}
}

// A KIND THE REGISTRY DOES NOT NAME MUST NOT BE INTERPOLATED, however
// non-empty it is.
//
// Executed on the tip before the fix: a gate carrying
// DeclaredMemberKind "a_kind_no_vocabulary_names" composed
// "This question asked about a population of a_kind_no_vocabulary_names ...",
// for which IsContextFabricServiceAuthoredLimitation returned FALSE while the
// invariant fallback returned TRUE -- an unrecognised, and therefore
// displaceable, disclosure. The non-empty test admitted it; membership does
// not, and the empty value is not a registry member either, so one predicate
// covers both.
func TestAnUnrecognisedDeclaredKindFallsBackToARecognisedSentence(t *testing.T) {
	t.Parallel()
	for _, declared := range []SubjectKind{"", "a_kind_no_vocabulary_names"} {
		gate := FrameGate{
			Outcome:            FrameGateRefusedBasis,
			RefuseBasis:        CohortMemberKindUnservable,
			DeclaredMemberKind: declared,
		}
		sentence := refusalLimitation(gate, gate.RefusalBasis())
		if !contractsv1.IsContextFabricServiceAuthoredLimitation(sentence) {
			t.Fatalf("declared kind %q composed %q, which is not recognised as service-authored -- an unrecognised disclosure is displaceable, so the caller can end up told nothing about the refusal", declared, sentence)
		}
		if sentence != contractsv1.ContextFabricFrameInvariantRefusalLimitation {
			t.Fatalf("declared kind %q composed %q, want the invariant fallback -- a kind the registry does not name must not reach the wire as if it did", declared, sentence)
		}
	}
}

// THE REFUSAL ARM OUTRANKS EVERY LATER ARM, which is the whole reason it is
// written first.
//
// A battery arm reordered `frame_gate_refused` after `ambiguous` and nothing
// failed. The engine cannot currently produce that input -- engine.go's
// refusing branch hands terminalResult a resolution with an EMPTY candidate
// list -- so the swap is unobservable through the engine, and this is
// deliberately a pin on the pure function rather than a claim about a live
// path. It is still the contract: subjectlessTerminalReason is total over its
// inputs, its own doc comment states the refusal arm is checked first because
// every later arm reports on a search that a refused frame never ran, and a
// future caller that reaches it with a populated pool must not be told
// retrieval found something ambiguous. Every later arm is exercised, not just
// the one the mutation happened to promote.
func TestTheRefusalArmOutranksEveryLaterTerminalReason(t *testing.T) {
	t.Parallel()
	refusing := FrameGate{Outcome: FrameGateRefusedBasis, RefuseBasis: CohortMemberKindUnservable}
	candidate := SubjectCandidate{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}, Confidence: 0.5}
	for _, testCase := range []struct {
		name       string
		resolution SubjectResolution
		dropped    int
	}{
		{"a populated candidate pool", SubjectResolution{Candidates: []SubjectCandidate{candidate}, Committed: []SubjectRef{}}, 0},
		{"an offer pool emptied by exclusion", SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}, ClarificationPrompt: "which repository did you mean?"}, 0},
		{"a graph that was never projected", SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}, GraphNotProjected: true}, 0},
		{"candidates dropped by authorization", SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}, 3},
		{"a genuinely empty pool", SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}, 0},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := subjectlessTerminalReason(refusing, testCase.resolution, testCase.dropped); got != "frame_gate_refused" {
				t.Fatalf("subjectlessTerminalReason() = %q, want %q -- the gate refused above retrieval, so reporting %q would describe a search that never happened and would make the refusing class uncountable in the collected logs", got, "frame_gate_refused", got)
			}
		})
	}
}
