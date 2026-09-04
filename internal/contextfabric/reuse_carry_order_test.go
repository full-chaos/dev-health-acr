package contextfabric

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

// windowReceiptCarryFixture builds the ONE request shape this file is about:
// a turn whose ONLY reference to a prior result is a winr_ window receipt,
// redeemed from a prior result that ALSO carries a receipt-confirmed
// expected_kind.
//
// That shape is the whole defect. `window` is a member of the closed
// ContextFabricStructureNeedKind vocabulary
// (contracts/v1/context_fabric_structure_types.go), but its confirmation is
// canonicalized into windowCanon.ConfirmedMember rather than into
// structureCanon.Confirmed -- and the reuse bypass reads
// structureCanon.Confirmed only. So confirming any of the other four members
// by receipt bypasses reuse, and confirming `window` by receipt does not.
//
// Every other receipt namespace is already covered and this fixture would
// not reproduce anything through them: PriorKindReceipts, PriorAnchorReceipts,
// PriorHandleReceipts and PriorCandidateReceipts all reach
// canonicalizeStructure (structure.go's own hasReceipts) and therefore either
// land in structureCanon.Confirmed or veto the request outright, both of which
// happen before the reuse gate is reached.
func windowReceiptCarryFixture() (InvestigationRequest, *staticResultStore, contractsv1.ContextFabricEffectiveEvidenceWindow) {
	frozenStart := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	frozenEnd := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_prior_reuse_0007"
	priorResult.WindowClarification = &WindowClarification{Options: []WindowOption{
		{ReceiptID: "winr_confirm0007", OptionID: "opt_90d", Label: "the last 90 days", RelativeID: RelativeWindowTrailing90D, Start: &frozenStart, End: &frozenEnd},
	}}
	// The linked prior carries a receipt-confirmed kind, so a carry that is
	// allowed to run at all will hit. Without this the test could not tell
	// "the carry was skipped" from "the carry ran and found nothing".
	priorResult.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{
		confirmedKindEntry(contractsv1.ContextFabricSubjectTeam, "result_prior_reuse_0006", "kindr_confirm0006"),
	}

	request := validInvestigationRequest()
	request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: priorResult.ResultID, ReceiptID: "winr_confirm0007"}}

	// confirmedWindow is what redeeming winr_confirm0007 resolves this turn's
	// effective window to. A reuse candidate must carry the SAME window to be
	// servable at all (tryReuse re-derives the candidate's own window key and
	// compares it byte-for-byte), which is why the harm arm below stamps it
	// onto the stored candidate rather than leaving the default.
	confirmedWindow := contractsv1.ContextFabricEffectiveEvidenceWindow{
		Start: &frozenStart, End: &frozenEnd, RelativeID: RelativeWindowTrailing90D,
		Provenance: WindowQuestionStated,
	}

	return request, &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}, confirmedWindow
}

// TestReuseBypass_AWindowReceiptBypassesReuseSoTheCarryCanApply is the
// red-first test for the reuse/carry misordering.
//
// The reuse gate below returns a HIT unconditionally, and the stored
// candidate it returns carries no confirmed structure of any kind -- a
// perfectly legitimate reuse source under the existing source-eligibility
// rule, and exactly the "generic answer" a caller must not be served here.
// A hit therefore proves the bypass is the ONLY thing standing between this
// request and an answer that never saw the confirmed kind, the same
// construction TestCHAOS3478_PriorSubjectReceiptsBypassReuse uses for its
// own namespace.
//
// Three things are asserted, because the defect has three distinguishable
// symptoms and a fix that produced only one of them would be incomplete:
// the gate is not consulted, the carry's disclosure reaches the wire, and
// the carried kind reaches the ResolveSubjects seam that the pool filter
// hangs off.
func TestReuseBypass_AWindowReceiptBypassesReuseSoTheCarryCanApply(t *testing.T) {
	t.Parallel()

	request, store, confirmedWindow := windowReceiptCarryFixture()
	_, candidate := reusableCandidate()
	// The stored candidate is the ORDINARY conversational predecessor: an
	// earlier turn that answered the same question under the SAME 90-day
	// window, stated in the question rather than confirmed by receipt, and
	// saved with no confirmed structure at all. That combination is a
	// legitimate reuse source today, and it is exactly what makes the defect
	// reachable rather than theoretical -- the window fragment of the reuse
	// key matches, so nothing else stands between this request and the
	// stored answer except the bypass.
	candidate.Interpretation.TimeContext = TimeContext{Axis: TemporalCurrent}
	candidate.EffectiveEvidenceWindow = &confirmedWindow
	reuseGateCalls := 0

	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	graph := &kindRecordingGraphReader{graphReaderStub: graphReaderStub{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
	}}
	telemetry := &recordingTelemetry{}
	freshResult := validInvestigationResult()

	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: graph,
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return freshResult, nil
		}),
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}),
		Results:   store,
		Telemetry: telemetry,
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			reuseGateCalls++
			return candidate, true, nil
		}),
	})

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	if reuseGateCalls != 0 {
		t.Errorf("ReuseGate was called %d times, want 0: a request that confirmed the window axis by receipt names a prior result the carries can walk, and must bypass the reuse lookup entirely rather than be served an answer produced before that confirmation existed", reuseGateCalls)
	}
	if result.Reused {
		t.Errorf("result.Reused = true, want false: a stored answer that never saw the confirmed expected_kind was served to a turn whose own chain confirms one")
	}

	carried := false
	for _, entry := range result.ConfirmedStructure {
		if entry.Member == contractsv1.ContextFabricStructureNeedExpectedKind &&
			entry.Source == contractsv1.ContextFabricStructureSourceCarried {
			carried = true
		}
	}
	if !carried {
		t.Errorf("ConfirmedStructure = %+v, want a source=carried expected_kind entry: the kind carry must run and be disclosed on a turn linked only by a window receipt", result.ConfirmedStructure)
	}

	// The seam the carry actually changes. Asserting on the ResolveSubjects
	// argument rather than on a downstream stub's scripted reply is what
	// makes this an assertion about the engine and not about the fixture --
	// the same reasoning kindRecordingGraphReader's own doc comment gives.
	sawTeam := false
	for _, seen := range graph.seen {
		if seen != nil && seen.Kind == contractsv1.ContextFabricSubjectTeam {
			sawTeam = true
		}
	}
	if !sawTeam {
		t.Errorf("ResolveSubjects saw ConfirmedExpectedKind %+v, want a non-nil team: the carried kind must reach the pool filter, which is the only thing that makes the carry change an answer", graph.seen)
	}
	if len(graph.seen) == 0 {
		t.Error("ResolveSubjects was never called at all -- reuse returned before the decisive path ran, so the two assertions above proved nothing about the carry")
	}
}

// TestReuseBypass_ARequestNamingNoPriorResultStillReuses is the
// over-firing guard, and it is not optional: the failure mode a widened
// bypass invites is not "the bypass does not fire" but "the bypass fires
// for everything", which silently turns answer reuse off and would read as
// a pass on every assertion in the test above.
//
// Identical fixture and identical unconditional-hit gate, with the ONE
// difference that matters: the request names no prior result at all. This
// must still be served from the cache, on both sides of the change.
func TestReuseBypass_ARequestNamingNoPriorResultStillReuses(t *testing.T) {
	t.Parallel()

	_, store, _ := windowReceiptCarryFixture()
	_, candidate := reusableCandidate()
	reuseGateCalls := 0

	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	freshResult := validInvestigationResult()

	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return freshResult, nil
		}),
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}),
		Results: store,
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			reuseGateCalls++
			return candidate, true, nil
		}),
	})

	// validInvestigationRequest names no prior result through ANY of the six
	// receipt fields -- deliberately built fresh here rather than by clearing
	// the fixture's field, so a future field added to the fixture cannot
	// silently leak into this arm.
	request := validInvestigationRequest()

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if reuseGateCalls != 1 {
		t.Fatalf("ReuseGate was called %d times, want 1: a request naming no prior result has nothing to carry, so widening the bypass must not divert it", reuseGateCalls)
	}
	if !result.Reused {
		t.Fatal("result.Reused = false, want true: the widened bypass turned off answer reuse for a request it has no reason to touch")
	}
}

// TestReuseBypassReason_ArmsPartitionInAFixedOrder is a PIN, not a red-first
// test: it fixes the vocabulary and the ORDER rather than reproducing a
// defect. The order is what makes the three counts a partition -- a request
// can satisfy two arms at once (a confirmed kind receipt both fills
// structureCanon.Confirmed and names a prior result), and if the arms were
// evaluated in a different order on a different day the counts would stop
// adding up to the bypassed population.
//
// The last case is the load-bearing one. reuseBypassReason passes nil as
// carryReferencedResultIDs' validated-subject-receipts argument, which is
// sound ONLY because the PriorSubjectReceipts arm has already returned by
// then. This pins that reasoning behaviourally instead of leaving it in a
// comment: a request with PriorSubjectReceipts set must NEVER reach the
// third arm, whatever else it names.
func TestReuseBypassReason_ArmsPartitionInAFixedOrder(t *testing.T) {
	t.Parallel()

	confirmedKind := requestStructureCanonicalization{Confirmed: []confirmedStructureMember{{
		Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: "team",
	}}}
	windowReceipt := []BoundSubjectReceipt{{ResultID: "result_prior_reuse_0007", ReceiptID: "winr_confirm0007"}}
	subjectReceipt := []BoundSubjectReceipt{{ResultID: "result_prior_reuse_0007", ReceiptID: "receipt_abc12345678"}}

	for _, tc := range []struct {
		name   string
		canon  requestStructureCanonicalization
		mutate func(*InvestigationRequest)
		want   AnswerReuseBypassReason
	}{
		{
			name:   "a request naming nothing may consult the cache",
			mutate: func(*InvestigationRequest) {},
			want:   "",
		},
		{
			name:   "a confirmed structure member bypasses",
			canon:  confirmedKind,
			mutate: func(*InvestigationRequest) {},
			want:   AnswerReuseBypassConfirmedStructure,
		},
		{
			name:   "a prior-subject receipt bypasses",
			mutate: func(r *InvestigationRequest) { r.PriorSubjectReceipts = subjectReceipt },
			want:   AnswerReuseBypassPriorSubjectReceipts,
		},
		{
			name:   "a window receipt alone bypasses -- the arm this ticket adds",
			mutate: func(r *InvestigationRequest) { r.PriorWindowReceipts = windowReceipt },
			want:   AnswerReuseBypassPriorResultReference,
		},
		{
			name:   "confirmed structure wins over a prior-result reference",
			canon:  confirmedKind,
			mutate: func(r *InvestigationRequest) { r.PriorWindowReceipts = windowReceipt },
			want:   AnswerReuseBypassConfirmedStructure,
		},
		{
			// THE ONE THAT MAKES THE nil ARGUMENT SOUND. If this ever
			// returned prior_result_reference, reuseBypassReason would be
			// consulting carryReferencedResultIDs with a nil validated set
			// while request.PriorSubjectReceipts was non-empty -- i.e. asking
			// a question whose answer it had deliberately withheld the input
			// for.
			name: "prior-subject receipts win over a prior-result reference",
			mutate: func(r *InvestigationRequest) {
				r.PriorSubjectReceipts = subjectReceipt
				r.PriorWindowReceipts = windowReceipt
			},
			want: AnswerReuseBypassPriorSubjectReceipts,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			request := validInvestigationRequest()
			tc.mutate(&request)
			if got := reuseBypassReason(request, tc.canon); got != tc.want {
				t.Errorf("reuseBypassReason() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestReuseBypass_TheSlogSinkEmitsEachArm VERIFIES THE CONSUMER, not the
// producer: the standing telemetry rule is that a decision is diagnosable
// from the run's own artifacts, and a counter whose sink drops it is not.
// Every arm of the closed vocabulary is asserted to reach the log with its
// own reason value, under a message distinct from the reuse-outcome line so
// the two streams can be counted apart.
func TestReuseBypass_TheSlogSinkEmitsEachArm(t *testing.T) {
	t.Parallel()

	for _, reason := range []AnswerReuseBypassReason{
		AnswerReuseBypassConfirmedStructure,
		AnswerReuseBypassPriorSubjectReceipts,
		AnswerReuseBypassPriorResultReference,
	} {
		t.Run(string(reason), func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			sink := SlogEngineTelemetry{logger: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))}

			sink.RecordAnswerReuseBypass(context.Background(), storage.Principal{OrgID: "org_reuse_bypass"}, reason)

			line := buf.String()
			if !strings.Contains(line, `"reason":"`+string(reason)+`"`) {
				t.Errorf("log line %q does not carry reason=%q -- the bypass arm is undiagnosable from the run's own artifacts", line, reason)
			}
			if !strings.Contains(line, "context fabric answer reuse bypass") {
				t.Errorf("log line %q does not carry the bypass message: a bypass folded into the reuse-outcome stream would corrupt that stream's hit-rate denominator", line)
			}
			if strings.Contains(line, "context fabric answer reuse outcome") {
				t.Errorf("log line %q used the reuse OUTCOME message; the two streams must be countable apart", line)
			}
		})
	}
}
