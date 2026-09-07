package contextfabric

// CHAOS-5442: A REFUSED FRAME AND AN EMPTY GRAPH ARE NOT THE SAME FACT.
//
// The frame gate decides `member_kind_unservable` before retrieval runs, and
// the engine terminates the turn on that verdict (engine.go's
// familyOutcome.Gate.Refuses() branch). What the CALLER was told, before this
// change, was the ordinary empty-pool sentence -- "retrieval found no
// candidate ... in this organization's graph" -- and what an OPERATOR was
// told was the reason `empty_pool`, the same token a genuinely empty graph
// produces. Six corpus rows terminate this way in 3/3 replicates; the basis
// was decided, logged at Info on the frame-validation line, and then dropped
// on the way out.
//
// These two assertions use ONLY symbols that exist at the parent commit, so
// they RUN and FAIL there rather than failing to build. The wire field the
// same change adds cannot be pinned that way -- naming it is a compile error
// at the parent -- so it is proven by MUTATION instead, in the battery, the
// same split lane-5385-seam used for FrameGate itself.

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// refusingGateInterpreter is the interpreter double that returns a family
// outcome whose GATE REFUSES, which is the one input the terminal path reads
// to learn a frame was refused. It is deliberately a QuestionInterpreter and
// not an interpreterFunc: interpreterFunc hardcodes an unevaluated gate, and
// an unevaluated gate ALLOWS, so the whole condition under test would be
// unreachable through it.
type refusingGateInterpreter struct {
	gate FrameGate
}

func (i refusingGateInterpreter) Interpret(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error) {
	return InterpretedQuestion{
		Shape:             ShapeOpen,
		RequestedJudgment: "status",
		TimeContext:       TimeContext{Axis: TemporalCurrent},
	}, QuestionFamilyOutcome{
		// Unclassified is the honest family for a refused frame: the
		// refusal is exactly the server declining to act on the
		// question it was handed.
		Family: QuestionFamilyUnclassified,
		Source: QuestionFamilySourceNone,
		Gate:   i.gate,
	}, nil
}

// refusedFrameGraphReader fails the test if ANY retrieval method is reached.
//
// That is not defensive decoration -- it is half the claim. The gate refuses
// ABOVE resolution, so a terminal composed after ResolveSubjects ran would be
// a different code path wearing the same result, and an assertion about the
// limitation would then be measuring the empty-pool branch it is supposed to
// be distinguishing itself from.
type refusedFrameGraphReader struct {
	t *testing.T
}

func (g refusedFrameGraphReader) ResolveInvestigationBinding(context.Context, storage.Principal) (ResolvedGraphBinding, error) {
	return ResolvedGraphBinding{GraphKey: "refused-frame-key", Epoch: 0}, nil
}

func (g refusedFrameGraphReader) ResolveSubjects(context.Context, storage.Principal, InvestigationRequest, InterpretedQuestion, ResolvedGraphBinding, *ConfirmedExpectedKind, *ConfirmedAnchorSelection, *QuestionFrame, SubjectKind) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	g.t.Fatal("ResolveSubjects must never run under a refusing frame gate -- the gate refuses ABOVE retrieval, and a terminal reached through resolution is not the terminal under test")
	return SubjectResolution{}, StructureOfferMaterial{}, nil, nil, nil
}

func (g refusedFrameGraphReader) DiscoverContext(context.Context, storage.Principal, GraphDiscoveryRequest) (GraphContext, error) {
	g.t.Fatal("DiscoverContext must never run under a refusing frame gate")
	return GraphContext{}, nil
}

// investigateUnderRefusingGate drives one whole request through a real
// Engine whose interpretation carries the given gate, and returns the served
// terminal plus the telemetry the run recorded.
func investigateUnderRefusingGate(t *testing.T, gate FrameGate) (InvestigationResult, *recordingTelemetry) {
	t.Helper()
	telemetry := &recordingTelemetry{}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: refusingGateInterpreter{gate: gate},
		Graph:       refusedFrameGraphReader{t: t},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			t.Fatal("ReadFacts must not be called on a refused frame")
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			t.Fatal("Synthesize must not be called on a refused frame")
			return InvestigationResult{}, nil
		}),
		Telemetry: telemetry,
	}, EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(300, 0).UTC() }, NewResultID: func() string { return "result_refused_01" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v, want a clean terminal, not a propagated error -- a frame the server will not act on is a product outcome, never a 5xx", err)
	}
	return result, telemetry
}

// THE CALLER-FACING HALF. The empty-graph sentence is affirmatively WRONG on
// this path: retrieval never ran, so it cannot have "found no candidate", and
// a reader told that sentence will go looking for missing data in a graph
// that was never consulted.
func TestARefusedFrameDoesNotClaimRetrievalFoundNoCandidate(t *testing.T) {
	t.Parallel()
	result, _ := investigateUnderRefusingGate(t, FrameGate{
		Outcome:     FrameGateRefusedBasis,
		RefuseBasis: CohortMemberKindUnservable,
	})
	if result.Status != InvestigationNoMatch {
		t.Fatalf("result.Status = %q, want %q -- this pin is about the DISCLOSURE on the refusing terminal, so a different terminal means it is measuring something else", result.Status, InvestigationNoMatch)
	}
	for _, limitation := range result.Limitations {
		if limitation == noMatchLimitationUnproven {
			t.Fatalf("result.Limitations = %#v carries the empty-pool wording on a REFUSED frame; retrieval never ran, so nothing was searched and nothing was 'found' -- an emptied-by-rule outcome must not read as an empty-graph outcome", result.Limitations)
		}
	}
}

// THE OPERATOR-FACING HALF, and the one that makes the class countable. The
// subjectless-terminal reason is the only Info line on this path that speaks
// for the TERMINAL rather than for the frame; reporting `empty_pool` here
// makes a refused frame indistinguishable, in the collected logs, from a
// graph that genuinely held nothing.
func TestARefusedFrameIsNotReportedAsAnEmptyPool(t *testing.T) {
	t.Parallel()
	_, telemetry := investigateUnderRefusingGate(t, FrameGate{
		Outcome:     FrameGateRefusedBasis,
		RefuseBasis: CohortMemberKindUnservable,
	})
	want := []string{"frame_gate_refused"}
	if !stringSlicesEqual(telemetry.subjectlessTerminalReasons, want) {
		t.Fatalf("subjectlessTerminalReasons = %#v, want %#v -- a frame the gate refused and a pool that was genuinely empty are different facts and must not share a reason token", telemetry.subjectlessTerminalReasons, want)
	}
}

// THE NEGATIVE CONTROL, and it is the assertion that keeps the two above
// honest. An ORDINARY empty pool -- no gate refusal, nothing withheld, a
// graph that simply held no candidate -- must keep the empty-pool sentence
// and the `empty_pool` reason exactly as it has them today. A change that
// re-worded every no_match would satisfy both pins above and be a
// regression; this one fails if it does.
func TestAnOrdinaryEmptyPoolKeepsTheEmptyPoolDisclosure(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	engine := mustEngineForTerminalReasonTest(t, graphReaderStub{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
	}, telemetry)
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	found := false
	for _, limitation := range result.Limitations {
		if limitation == noMatchLimitationUnproven {
			found = true
		}
	}
	if !found {
		t.Fatalf("result.Limitations = %#v, want the empty-pool wording retained -- this control is what distinguishes 'the refusing path discloses its basis' from 'every no_match was re-worded'", result.Limitations)
	}
	if want := []string{"empty_pool"}; !stringSlicesEqual(telemetry.subjectlessTerminalReasons, want) {
		t.Fatalf("subjectlessTerminalReasons = %#v, want %#v -- the ordinary empty pool keeps its own token", telemetry.subjectlessTerminalReasons, want)
	}
}
