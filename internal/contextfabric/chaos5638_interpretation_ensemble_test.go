package contextfabric

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"log/slog"
	"sort"
	"sync"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// concurrentReceiptSink is a receipt sink that is safe to call from several
// samples at once.
//
// The package's existing fakeReceiptSink appends to a slice with no lock,
// which is correct for a single-sample interpreter and a data race for an
// ensemble. Using it here would make every ensemble test flaky under -race
// for a reason that has nothing to do with the code under test -- and would
// also hide the real question, which is whether the PRODUCTION sinks are safe.
// They are: pgmodelreceipts.Store holds a *sql.DB, and SlogEngineTelemetry
// holds a *slog.Logger.
type concurrentReceiptSink struct {
	mu       sync.Mutex
	recorded []ModelExecutionReceipt
}

func (s *concurrentReceiptSink) RecordModelExecution(_ context.Context, _ storage.Principal, receipt ModelExecutionReceipt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recorded = append(s.recorded, receipt)
	return nil
}

func (s *concurrentReceiptSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.recorded)
}

// sampledRuntimeStub answers each sample index from a fixed script, and
// records which indices it was asked for.
type sampledRuntimeStub struct {
	mu     sync.Mutex
	asked  []int
	perIdx map[int]InterpretedQuestion
	errIdx map[int]error
	// receipt is what every sample returns unless receiptIdx names one for
	// that index. Per-sample receipts are what let a test tell the WINNING
	// sample's receipt from another sample's -- with one shared receipt the
	// two are indistinguishable, and a mutation that stamped the frame from
	// the wrong sample survives.
	receipt    ModelExecutionReceipt
	receiptIdx map[int]ModelExecutionReceipt
}

func (s *sampledRuntimeStub) InterpretQuestionForSample(_ context.Context, _ storage.Principal, _ InvestigationRequest, sample int) (InterpretedQuestion, ModelExecutionReceipt, error) {
	s.mu.Lock()
	s.asked = append(s.asked, sample)
	s.mu.Unlock()
	if err, ok := s.errIdx[sample]; ok {
		return InterpretedQuestion{}, s.receiptFor(sample), err
	}
	return s.perIdx[sample], s.receiptFor(sample), nil
}

func (s *sampledRuntimeStub) receiptFor(sample int) ModelExecutionReceipt {
	if receipt, ok := s.receiptIdx[sample]; ok {
		return receipt
	}
	return s.receipt
}

func (s *sampledRuntimeStub) askedIndices() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]int(nil), s.asked...)
	sort.Ints(out)
	return out
}

// ensembleQuestion builds a valid interpretation whose Shape decides its
// family (precedence row 4 vs rows 5+6) and whose RequestedJudgment makes the
// sample identifiable in an assertion.
func ensembleQuestion(shape InvestigationShape, judgment string) InterpretedQuestion {
	return InterpretedQuestion{
		Shape:             shape,
		RequestedJudgment: judgment,
		TimeContext:       TimeContext{Axis: TemporalCurrent},
		FactRequirements:  []FactRequirement{{Kind: FactStatus}},
	}
}

// THE PREMISE every arm below rests on, asserted rather than assumed: the two
// shapes really do resolve to two different families. If the precedence table
// ever stops distinguishing them, the majority arms would pass while proving
// nothing, and this fails first and says so.
func TestTheTwoEnsembleFixtureShapesResolveToDifferentFamilies(t *testing.T) {
	t.Parallel()
	cohort := ResolveQuestionFamily([]FamilySample{familySampleFrom(ensembleQuestion(ShapeDiscoveredCohort, "a"), ModelExecutionReceipt{})})
	single := ResolveQuestionFamily([]FamilySample{familySampleFrom(ensembleQuestion(ShapeSingleSubject, "a"), ModelExecutionReceipt{})})
	if cohort.Family == single.Family {
		t.Fatalf("fixture defect: both shapes resolve to %q, so no arm below can distinguish a majority", cohort.Family)
	}
}

// A CONFIGURED SIZE WITH NO SAMPLED RUNTIME IS LOUD.
func TestAnEnsembleWithNoSampledRuntimeFailsLoudly(t *testing.T) {
	t.Parallel()
	interpreter := RuntimeQuestionInterpreter{
		Runtime:      fakeModelRuntime{interpreted: ensembleQuestion(ShapeSingleSubject, "x"), receipt: validModelReceiptFixture(ModelOperationInterpret)},
		Sink:         &concurrentReceiptSink{},
		EnsembleSize: 3,
	}
	_, _, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if !errors.Is(err, ErrEnsembleRuntimeMissing) {
		t.Fatalf("Interpret() error = %v, want ErrEnsembleRuntimeMissing -- a configured ensemble must never quietly fall back to the single-sample runtime", err)
	}
}

// THE CONTROL on that: unconfigured, the single-sample path is unchanged and
// the sampled runtime is never touched.
func TestAnUnconfiguredInterpreterNeverSamples(t *testing.T) {
	t.Parallel()
	for _, size := range []int{0, 1} {
		t.Run(fmt.Sprintf("size=%d", size), func(t *testing.T) {
			t.Parallel()
			sampled := &sampledRuntimeStub{perIdx: map[int]InterpretedQuestion{}, receipt: validModelReceiptFixture(ModelOperationInterpret)}
			sink := &concurrentReceiptSink{}
			interpreter := RuntimeQuestionInterpreter{
				Runtime:        fakeModelRuntime{interpreted: ensembleQuestion(ShapeSingleSubject, "single"), receipt: validModelReceiptFixture(ModelOperationInterpret)},
				SampledRuntime: sampled,
				Sink:           sink,
				EnsembleSize:   size,
			}
			got, outcome, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
			if err != nil {
				t.Fatalf("Interpret() error = %v", err)
			}
			if len(sampled.askedIndices()) != 0 {
				t.Fatalf("the sampled runtime was called %d time(s) at EnsembleSize=%d", len(sampled.askedIndices()), size)
			}
			if got.RequestedJudgment != "single" {
				t.Fatalf("question came from the wrong runtime: %q", got.RequestedJudgment)
			}
			if outcome.Source != QuestionFamilySourceModel {
				t.Fatalf("source = %q, want %q -- the N=1 degrade path is unchanged", outcome.Source, QuestionFamilySourceModel)
			}
			if sink.count() != 1 {
				t.Fatalf("receipts recorded = %d, want exactly 1", sink.count())
			}
		})
	}
}

// ONE SAMPLE PER INDEX, and every receipt persisted.
func TestTheEnsembleDrawsOneSamplePerIndexAndRecordsEachReceipt(t *testing.T) {
	t.Parallel()
	sampled := &sampledRuntimeStub{
		perIdx: map[int]InterpretedQuestion{
			0: ensembleQuestion(ShapeSingleSubject, "s0"),
			1: ensembleQuestion(ShapeSingleSubject, "s1"),
			2: ensembleQuestion(ShapeSingleSubject, "s2"),
		},
		receipt: validModelReceiptFixture(ModelOperationInterpret),
	}
	sink := &concurrentReceiptSink{}
	interpreter := RuntimeQuestionInterpreter{SampledRuntime: sampled, Sink: sink, EnsembleSize: 3}

	if _, _, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest()); err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	asked := sampled.askedIndices()
	if len(asked) != 3 || asked[0] != 0 || asked[1] != 1 || asked[2] != 2 {
		t.Fatalf("sample indices = %v, want each of 0,1,2 exactly once", asked)
	}
	if sink.count() != 3 {
		t.Fatalf("receipts recorded = %d, want 3 -- every sample is a real model call and owes its own receipt", sink.count())
	}
}

// THE WINNER IS A WHOLE SAMPLE. The majority family decides, and the question
// returned is one of the samples that HELD it -- never the minority's.
func TestTheEnsembleReturnsAWinningSamplesOwnQuestion(t *testing.T) {
	t.Parallel()
	sampled := &sampledRuntimeStub{
		// THE MINORITY IS SAMPLE 0 DELIBERATELY. With the majority at
		// index 0 this test would pass against an implementation that
		// ignored WinningSampleIndex and always returned the first
		// successful sample -- the exact defect it exists to catch.
		perIdx: map[int]InterpretedQuestion{
			0: ensembleQuestion(ShapeDiscoveredCohort, "minority"),
			1: ensembleQuestion(ShapeSingleSubject, "majority-a"),
			2: ensembleQuestion(ShapeSingleSubject, "majority-b"),
		},
		receipt: validModelReceiptFixture(ModelOperationInterpret),
	}
	interpreter := RuntimeQuestionInterpreter{SampledRuntime: sampled, Sink: &concurrentReceiptSink{}, EnsembleSize: 3}

	got, outcome, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if outcome.Source != QuestionFamilySourceModelConsensus {
		t.Fatalf("source = %q, want %q", outcome.Source, QuestionFamilySourceModelConsensus)
	}
	if outcome.Family != QuestionFamilySubjectInvestigation {
		t.Fatalf("family = %q, want the 2-of-3 majority", outcome.Family)
	}
	if got.RequestedJudgment == "minority" {
		t.Fatal("the minority sample's question was returned beside the majority family -- a plan no model proposed")
	}
	if got.RequestedJudgment != "majority-a" && got.RequestedJudgment != "majority-b" {
		t.Fatalf("question = %q, want one of the majority samples", got.RequestedJudgment)
	}
	if got.Shape != ShapeSingleSubject {
		t.Fatalf("shape = %q, want the majority's own shape", got.Shape)
	}
}

// A FAILED SAMPLE IS DROPPED, NOT SUBSTITUTED: the denominator is the samples
// that succeeded, so 2 survivors agreeing is a majority.
func TestAFailedSampleIsDroppedNotSubstituted(t *testing.T) {
	t.Parallel()
	sampled := &sampledRuntimeStub{
		perIdx: map[int]InterpretedQuestion{
			0: ensembleQuestion(ShapeSingleSubject, "lived-a"),
			2: ensembleQuestion(ShapeSingleSubject, "lived-b"),
		},
		errIdx:  map[int]error{1: errors.New("sample 1 upstream failure")},
		receipt: validModelReceiptFixture(ModelOperationInterpret),
	}
	interpreter := RuntimeQuestionInterpreter{SampledRuntime: sampled, Sink: &concurrentReceiptSink{}, EnsembleSize: 3}

	got, outcome, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Interpret() error = %v -- one failed sample must not fail the turn", err)
	}
	if outcome.Family != QuestionFamilySubjectInvestigation {
		t.Fatalf("family = %q, want the surviving samples' agreed family", outcome.Family)
	}
	if got.RequestedJudgment != "lived-a" && got.RequestedJudgment != "lived-b" {
		t.Fatalf("question = %q, want one of the samples that succeeded", got.RequestedJudgment)
	}
	if len(outcome.Samples) != 2 {
		t.Fatalf("outcome carries %d sample row(s), want 2 -- a dropped sample must not appear as a vote", len(outcome.Samples))
	}
}

// EVERY SAMPLE FAILING IS AN ERROR, not an outcome resolved over nothing. An
// interpretation that could not be produced is not an unclassified family.
func TestAnEnsembleWhoseSamplesAllFailReturnsTheError(t *testing.T) {
	t.Parallel()
	boom := errors.New("upstream is down")
	sampled := &sampledRuntimeStub{
		perIdx:  map[int]InterpretedQuestion{},
		errIdx:  map[int]error{0: boom, 1: boom, 2: boom},
		receipt: validModelReceiptFixture(ModelOperationInterpret),
	}
	interpreter := RuntimeQuestionInterpreter{SampledRuntime: sampled, Sink: &concurrentReceiptSink{}, EnsembleSize: 3}

	_, outcome, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if err == nil {
		t.Fatal("Interpret() returned no error with every sample failed")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want it to carry the sample failure", err)
	}
	if outcome.Family != "" {
		t.Fatalf("a failed ensemble returned family %q -- it must return no outcome at all", outcome.Family)
	}
}

// A SPLIT ENSEMBLE REFUSES THE FAMILY and still returns ONE WHOLE sample.
// unclassified with source=model_plurality_rejected is the resolver working;
// the caller still needs an interpretation, and it must be a single sample
// rather than a merge of the three.
func TestASplitEnsembleRefusesTheFamilyAndReturnsOneWholeSample(t *testing.T) {
	t.Parallel()
	split := ensembleQuestion(ShapeExplicitCohort, "split")
	sampled := &sampledRuntimeStub{
		perIdx: map[int]InterpretedQuestion{
			0: ensembleQuestion(ShapeSingleSubject, "s0"),
			1: ensembleQuestion(ShapeDiscoveredCohort, "s1"),
			2: split,
		},
		receipt: validModelReceiptFixture(ModelOperationInterpret),
	}
	interpreter := RuntimeQuestionInterpreter{SampledRuntime: sampled, Sink: &concurrentReceiptSink{}, EnsembleSize: 3}

	got, outcome, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Interpret() error = %v -- a split is a resolved outcome, not a failure", err)
	}
	if outcome.Source != QuestionFamilySourcePluralityRejected {
		t.Fatalf("source = %q, want %q", outcome.Source, QuestionFamilySourcePluralityRejected)
	}
	if outcome.Family != QuestionFamilyUnclassified {
		t.Fatalf("family = %q, want unclassified on a refused split", outcome.Family)
	}
	// One WHOLE sample: whichever it is, its judgment and shape must come
	// from the same script entry, never a mix of two.
	for index, want := range map[string]InvestigationShape{"s0": ShapeSingleSubject, "s1": ShapeDiscoveredCohort, "split": ShapeExplicitCohort} {
		if got.RequestedJudgment == index && got.Shape != want {
			t.Fatalf("returned question mixes samples: judgment %q with shape %q", got.RequestedJudgment, got.Shape)
		}
	}
	if got.RequestedJudgment == "" {
		t.Fatal("a refused split returned no interpretation at all")
	}
}

// THE GATE IS STAMPED FROM THE WINNING SAMPLE'S OWN RECEIPT.
//
// Every sample carries a DIFFERENT frame verdict and the majority sits away
// from index 0, so this distinguishes three things a weaker fixture cannot:
// that the stamp happens at all, that it comes from the winner rather than
// from the first sample, and that it is not simply the last one to finish.
// A shared receipt makes all three indistinguishable: an implementation that
// stamped from succeeded[0] instead of the winner would pass, because the
// receipt it read would be byte-identical to the one it should have read.
//
// Publishing another sample's frame verdict beside the winner's family would
// be the same field-wise mixing the winner rule exists to prevent, and the
// engine's refusal gate reads exactly this value.
func TestTheEnsembleStampsTheFrameGateFromTheWinningSamplesReceipt(t *testing.T) {
	t.Parallel()
	// THE FRAME ITSELF IS THE DISCRIMINATOR, not a pre-set gate outcome.
	// interpretOneSample runs resolveFrame over every sample, and that
	// OVERWRITES FrameGateOutcome from the receipt's own QuestionFrame -- so
	// two receipts differing only in a hand-set gate value are identical by
	// the time the stamp happens, and a mutant reading the wrong sample
	// survives. Giving the minority a real frame and the majority none makes
	// the two genuinely different at the point the stamp is taken.
	minorityReceipt := validModelReceiptFixture(ModelOperationInterpret)
	minorityReceipt.QuestionFrame = namedSubjectFrame()
	majorityReceipt := validModelReceiptFixture(ModelOperationInterpret)
	majorityReceipt.QuestionFrame = nil

	sampled := &sampledRuntimeStub{
		perIdx: map[int]InterpretedQuestion{
			0: ensembleQuestion(ShapeDiscoveredCohort, "minority"),
			1: ensembleQuestion(ShapeSingleSubject, "majority-a"),
			2: ensembleQuestion(ShapeSingleSubject, "majority-b"),
		},
		receiptIdx: map[int]ModelExecutionReceipt{
			0: minorityReceipt,
			1: majorityReceipt,
			2: majorityReceipt,
		},
		receipt: validModelReceiptFixture(ModelOperationInterpret),
	}
	interpreter := RuntimeQuestionInterpreter{SampledRuntime: sampled, Sink: &concurrentReceiptSink{}, EnsembleSize: 3}

	got, outcome, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if outcome.Family != QuestionFamilySubjectInvestigation {
		t.Fatalf("fixture defect: family = %q, want the 2-of-3 majority", outcome.Family)
	}
	if got.RequestedJudgment == "minority" {
		t.Fatal("fixture defect: the minority question was returned, so the gate assertion proves nothing")
	}
	if outcome.Frame != nil {
		t.Fatal("the frame was carried from the MINORITY sample's receipt; the winning samples proposed none")
	}
	if outcome.Gate.Outcome != FrameGateNotProposed {
		t.Fatalf("gate outcome = %q, want the winning sample's own %q -- the minority's frame would have gated differently",
			outcome.Gate.Outcome, FrameGateNotProposed)
	}
	if len(outcome.FrameObligations) != 0 {
		t.Fatalf("obligations = %v, carried from a sample that did not win", outcome.FrameObligations)
	}
}

// N IS BOUNDED. A caller asking for more than the ceiling gets the ceiling,
// not the number it asked for: interpret x N is paid on every turn 1.
func TestTheEnsembleSizeIsBounded(t *testing.T) {
	t.Parallel()
	perIdx := map[int]InterpretedQuestion{}
	for i := 0; i < QuestionFamilyEnsembleMax+5; i++ {
		perIdx[i] = ensembleQuestion(ShapeSingleSubject, fmt.Sprintf("s%d", i))
	}
	sampled := &sampledRuntimeStub{perIdx: perIdx, receipt: validModelReceiptFixture(ModelOperationInterpret)}
	interpreter := RuntimeQuestionInterpreter{SampledRuntime: sampled, Sink: &concurrentReceiptSink{}, EnsembleSize: QuestionFamilyEnsembleMax + 5}

	if _, _, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest()); err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if got := len(sampled.askedIndices()); got != QuestionFamilyEnsembleMax {
		t.Fatalf("samples drawn = %d, want the ceiling %d", got, QuestionFamilyEnsembleMax)
	}
}

// ensembleTelemetrySpy captures the composition event.
type ensembleTelemetrySpy struct {
	mu        sync.Mutex
	ensembles []InterpretationEnsembleEvent
}

func (s *ensembleTelemetrySpy) RecordQuestionFamilyResolution(context.Context, storage.Principal, QuestionFamilyResolutionEvent) {
}

func (s *ensembleTelemetrySpy) RecordInterpretationEnsemble(_ context.Context, _ storage.Principal, event InterpretationEnsembleEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensembles = append(s.ensembles, event)
}

func (s *ensembleTelemetrySpy) only(t *testing.T) InterpretationEnsembleEvent {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.ensembles) != 1 {
		t.Fatalf("ensemble events = %d, want exactly 1 per ensemble turn", len(s.ensembles))
	}
	return s.ensembles[0]
}

// fallbackReceipt is a sample that came back from the FALLBACK provider.
func fallbackReceipt() ModelExecutionReceipt {
	receipt := validModelReceiptFixture(ModelOperationInterpret)
	receipt.FallbackUsed = true
	return receipt
}

// A FALLBACK-SERVED SAMPLE IS NOT A VOTE.
//
// The runtime's own port documents the obligation on its caller: the sample
// index governs the PRIMARY attempt only, the fallback entry point takes no
// index, so N fallback responses are ONE answer counted N times. Counting them
// reports agreement that was never measured -- and reports it as
// `model_consensus`, which is the single word a measurement run keys on.
func TestFallbackServedSamplesNeverVote(t *testing.T) {
	t.Parallel()
	spy := &ensembleTelemetrySpy{}
	sampled := &sampledRuntimeStub{
		perIdx: map[int]InterpretedQuestion{
			0: ensembleQuestion(ShapeDiscoveredCohort, "fell-back"),
			1: ensembleQuestion(ShapeSingleSubject, "primary-a"),
			2: ensembleQuestion(ShapeSingleSubject, "primary-b"),
		},
		receiptIdx: map[int]ModelExecutionReceipt{0: fallbackReceipt()},
		receipt:    validModelReceiptFixture(ModelOperationInterpret),
	}
	interpreter := RuntimeQuestionInterpreter{
		SampledRuntime: sampled, Sink: &concurrentReceiptSink{}, EnsembleSize: 3, FamilyTelemetry: spy,
	}

	got, outcome, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if got.RequestedJudgment == "fell-back" {
		t.Fatal("a fallback-served sample won the vote")
	}
	if len(outcome.Samples) != 2 {
		t.Fatalf("outcome carries %d votes, want 2 -- the fallback sample must not be one", len(outcome.Samples))
	}
	event := spy.only(t)
	if event.FallbackServed != 1 {
		t.Fatalf("fallback_served = %d, want 1 -- a dropped sample must still be counted", event.FallbackServed)
	}
	if event.PrimarySucceeded != 2 || event.Requested != 3 || event.Failed != 0 {
		t.Fatalf("event = %+v, want requested=3 primary=2 failed=0", event)
	}
	if !event.QuorumMet {
		t.Fatal("2 primary samples of 3 requested is a strict majority; quorum should be met")
	}
}

// EVERY SAMPLE FALLING BACK IS ITS OWN OUTCOME. Nothing failed, so there is no
// error to join, and nothing may vote, so there is no consensus to take.
func TestAnAllFallbackEnsembleIsItsOwnNamedFailure(t *testing.T) {
	t.Parallel()
	spy := &ensembleTelemetrySpy{}
	sampled := &sampledRuntimeStub{
		perIdx: map[int]InterpretedQuestion{
			0: ensembleQuestion(ShapeSingleSubject, "s0"),
			1: ensembleQuestion(ShapeSingleSubject, "s1"),
			2: ensembleQuestion(ShapeSingleSubject, "s2"),
		},
		receipt: fallbackReceipt(),
	}
	interpreter := RuntimeQuestionInterpreter{
		SampledRuntime: sampled, Sink: &concurrentReceiptSink{}, EnsembleSize: 3, FamilyTelemetry: spy,
	}

	_, _, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if !errors.Is(err, ErrEnsembleAllSamplesFellBack) {
		t.Fatalf("err = %v, want ErrEnsembleAllSamplesFellBack", err)
	}
	event := spy.only(t)
	if event.FallbackServed != 3 || event.PrimarySucceeded != 0 || event.QuorumMet {
		t.Fatalf("event = %+v, want fallback=3 primary=0 quorum=false", event)
	}
}

// BELOW QUORUM THE TURN IS NOT A CONSENSUS, and says so on both surfaces:
// source=model on the outcome, and a Warn-level composition event naming what
// was requested against what survived.
//
// N=5 WITH TWO AGREEING SURVIVORS IS THE CASE THAT DISCRIMINATES. At N=3
// with one survivor the quorum branch is
// unobservable: ResolveQuestionFamily's own N==1 degrade already reports
// source=model, so removing the branch changes nothing and the test passes
// either way. Two survivors out of five are a strict majority OF THEMSELVES,
// so without the quorum rule they resolve to model_consensus -- a "consensus"
// of two samples for a turn that asked for five.
func TestBelowQuorumTheEnsembleDegradesLoudlyToOneSample(t *testing.T) {
	t.Parallel()
	boom := errors.New("upstream")
	spy := &ensembleTelemetrySpy{}
	sampled := &sampledRuntimeStub{
		perIdx: map[int]InterpretedQuestion{
			3: ensembleQuestion(ShapeSingleSubject, "lived-a"),
			4: ensembleQuestion(ShapeSingleSubject, "lived-b"),
		},
		errIdx:  map[int]error{0: boom, 1: boom, 2: boom},
		receipt: validModelReceiptFixture(ModelOperationInterpret),
	}
	interpreter := RuntimeQuestionInterpreter{
		SampledRuntime: sampled, Sink: &concurrentReceiptSink{}, EnsembleSize: 5, FamilyTelemetry: spy,
	}

	got, outcome, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Interpret() error = %v -- a degraded turn still answers", err)
	}
	if got.RequestedJudgment != "lived-a" && got.RequestedJudgment != "lived-b" {
		t.Fatalf("question = %q, want a surviving sample's own", got.RequestedJudgment)
	}
	if outcome.Source == QuestionFamilySourceModelConsensus {
		t.Fatal("2 samples of a requested 5 were reported as a consensus")
	}
	if outcome.Source != QuestionFamilySourceModel {
		t.Fatalf("source = %q, want %q -- the honest label below quorum", outcome.Source, QuestionFamilySourceModel)
	}
	if len(outcome.Samples) != 1 {
		t.Fatalf("outcome carries %d votes; below quorum the turn is a single sample", len(outcome.Samples))
	}
	event := spy.only(t)
	if event.QuorumMet {
		t.Fatal("quorum_met is true with 2 primary samples of 5 requested")
	}
	if event.Requested != 5 || event.PrimarySucceeded != 2 || event.Failed != 3 {
		t.Fatalf("event = %+v, want requested=5 primary=2 failed=3", event)
	}
}

// THE CONTROL: at or above quorum the same shape IS a consensus, so the rule
// above is a threshold and not a blanket refusal.
func TestAtQuorumTheEnsembleIsAConsensus(t *testing.T) {
	t.Parallel()
	boom := errors.New("upstream")
	spy := &ensembleTelemetrySpy{}
	sampled := &sampledRuntimeStub{
		perIdx: map[int]InterpretedQuestion{
			2: ensembleQuestion(ShapeSingleSubject, "lived-a"),
			3: ensembleQuestion(ShapeSingleSubject, "lived-b"),
			4: ensembleQuestion(ShapeSingleSubject, "lived-c"),
		},
		errIdx:  map[int]error{0: boom, 1: boom},
		receipt: validModelReceiptFixture(ModelOperationInterpret),
	}
	interpreter := RuntimeQuestionInterpreter{
		SampledRuntime: sampled, Sink: &concurrentReceiptSink{}, EnsembleSize: 5, FamilyTelemetry: spy,
	}

	_, outcome, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if outcome.Source != QuestionFamilySourceModelConsensus {
		t.Fatalf("source = %q, want %q -- 3 of 5 is quorum", outcome.Source, QuestionFamilySourceModelConsensus)
	}
	if event := spy.only(t); !event.QuorumMet {
		t.Fatalf("quorum_met is false at 3 primary samples of 5: %+v", event)
	}
}

// A REFUSED PLURALITY IS TERMINAL even when the winning sample carries a
// validated frame. The route is still computed and recorded -- it is a
// measurement -- but it may not overwrite the refusal, or the wire carries
// `source=model_plurality_rejected` beside a confident family.
func TestARefusedPluralityIsNotOverwrittenByTheRoute(t *testing.T) {
	t.Parallel()
	framed := validModelReceiptFixture(ModelOperationInterpret)
	framed.QuestionFrame = namedSubjectFrame()
	sampled := &sampledRuntimeStub{
		perIdx: map[int]InterpretedQuestion{
			0: ensembleQuestion(ShapeSingleSubject, "s0"),
			1: ensembleQuestion(ShapeDiscoveredCohort, "s1"),
			2: ensembleQuestion(ShapeExplicitCohort, "s2"),
		},
		receipt: framed,
	}
	interpreter := RuntimeQuestionInterpreter{SampledRuntime: sampled, Sink: &concurrentReceiptSink{}, EnsembleSize: 3}

	_, outcome, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if outcome.Source != QuestionFamilySourcePluralityRejected {
		t.Fatalf("fixture defect: source = %q, want the three-way split to be refused", outcome.Source)
	}
	if outcome.Family != QuestionFamilyUnclassified {
		t.Fatalf("family = %q beside source=%q -- the refusal was overwritten by the route",
			outcome.Family, outcome.Source)
	}
	// The measurement itself survives: suppressing the route would blind the
	// comparison this shadow exists to feed.
	if outcome.Route.Family == "" && outcome.Route.Disposition == "" {
		t.Fatal("the route decision was not recorded at all; only the overwrite should be withheld")
	}
}

// AN ADMITTED CARRIED PLAN WINS OVER A REFUSED PLURALITY, and says why.
//
// A refused plurality -- family=unclassified, source=model_plurality_rejected --
// is this turn's FAILED FRESH PROPOSAL. An independently admitted carried
// context is selected whether the fresh proposal agrees, disagrees or failed;
// the failure is disclosed, never acted on (vol. 2 5465 D-b). So the carry
// applies, and the carry event records the source it replaced: that field is
// the reason the comparison was not evaluated, and without it a carry over a
// refused plurality is indistinguishable from a carry over a turn that produced
// nothing at all.
//
// Driven through Engine.applyAndRecordCarry, the method the engine calls at the
// point it applies a carry, so the pin covers the engine's own path rather than
// the pure helper beneath it.
func TestAnAdmittedCarriedPlanWinsOverARefusedPluralityAndRecordsWhy(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	engine := &Engine{telemetry: telemetry}
	refused := QuestionFamilyOutcome{
		Family: QuestionFamilyUnclassified,
		Source: QuestionFamilySourcePluralityRejected,
	}
	carry := planCarryResult{
		Outcome: PlanCarryHit, Family: QuestionFamilyGroupedCohortStatus,
		GroupKind: SubjectTeam, SourceResultID: "result_prior_turn",
	}

	got := engine.applyAndRecordCarry(context.Background(), storage.Principal{OrgID: "org_1"}, refused, carry)

	if got.Family != QuestionFamilyGroupedCohortStatus {
		t.Fatalf("family = %q, want the admitted carried family -- a failed fresh proposal cannot replace an admitted carrier", got.Family)
	}
	if got.Source != QuestionFamilySourceCarried {
		t.Fatalf("source = %q, want %q", got.Source, QuestionFamilySourceCarried)
	}
	if len(telemetry.planCarries) != 1 {
		t.Fatalf("got %d plan-carry events, want exactly 1 -- the carry must be disclosed", len(telemetry.planCarries))
	}
	event := telemetry.planCarries[0]
	if event.SourceReplaced != QuestionFamilySourcePluralityRejected {
		t.Fatalf("source_replaced = %q, want %q -- the event must say the fresh proposal was a refused plurality",
			event.SourceReplaced, QuestionFamilySourcePluralityRejected)
	}
	if event.FamilyReplaced != QuestionFamilyUnclassified || event.FamilyCarried != QuestionFamilyGroupedCohortStatus {
		t.Fatalf("event = %+v, want unclassified replaced by the carried family", event)
	}
}

// THE CONTROL beside it: the same carry still fills a turn that genuinely
// produced nothing. Without this, a guard that refused EVERY unclassified turn
// would pass the pin above while breaking plan carry for everyone.
func TestAPlanCarryStillFillsATurnThatClassifiedNothing(t *testing.T) {
	t.Parallel()
	engine := &Engine{telemetry: &recordingTelemetry{}}
	nothing := QuestionFamilyOutcome{Family: QuestionFamilyUnclassified, Source: QuestionFamilySourceModel}
	carry := planCarryResult{Outcome: PlanCarryHit, Family: QuestionFamilyGroupedCohortStatus, GroupKind: SubjectTeam}

	got := engine.applyAndRecordCarry(context.Background(), storage.Principal{OrgID: "org_1"}, nothing, carry)
	if got.Family != QuestionFamilyGroupedCohortStatus || got.Source != QuestionFamilySourceCarried {
		t.Fatalf("got family=%q source=%q, want the carry applied to a turn with no reading of its own", got.Family, got.Source)
	}
}

// THE COMPOSITION EVENT CARRIES THE REQUEST ID, on the same terms as every
// other line the production telemetry emits. A below-quorum Warn names a turn
// that served a degraded answer; without the id it cannot be tied to the request
// it degraded. Asserted on the production sink's own bytes, not on a double.
func TestTheEnsembleCompositionEventCarriesTheRequestID(t *testing.T) {
	t.Parallel()
	const requestID = "req_0123456789abcdef0123456789abcdef"
	for _, quorumMet := range []bool{false, true} {
		var buf bytes.Buffer
		telemetry := NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, nil)))
		ctx := observability.WithRequestID(context.Background(), requestID)
		telemetry.RecordInterpretationEnsemble(ctx, storage.Principal{OrgID: "org_1"},
			InterpretationEnsembleEvent{Requested: 3, PrimarySucceeded: 2, Failed: 1, QuorumMet: quorumMet})
		var line map[string]any
		if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &line); err != nil {
			t.Fatalf("quorum_met=%v: not one JSON line: %v (%q)", quorumMet, err, buf.String())
		}
		if line["request_id"] != requestID {
			t.Fatalf("quorum_met=%v: request_id = %v, want %q -- both the Warn and the Info branch must carry it",
				quorumMet, line["request_id"], requestID)
		}
	}
}

// THE CARRY LINE NAMES THE REPLACED SOURCE, on the production sink's own bytes.
// This is the disclosure D-b requires in place of a refusal: an operator reading
// a carry over a refused plurality must be able to see that the fresh proposal
// failed, not merely that a carry happened.
func TestThePlanCarryLineNamesTheReplacedSource(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	telemetry := NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, nil)))
	telemetry.RecordPlanCarry(context.Background(), storage.Principal{OrgID: "org_1"}, PlanCarryEvent{
		FamilyReplaced: QuestionFamilyUnclassified,
		SourceReplaced: QuestionFamilySourcePluralityRejected,
		FamilyCarried:  QuestionFamilyGroupedCohortStatus,
	})
	var line map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &line); err != nil {
		t.Fatalf("not one JSON line: %v (%q)", err, buf.String())
	}
	if line["source_replaced"] != string(QuestionFamilySourcePluralityRejected) {
		t.Fatalf("source_replaced = %v, want %q", line["source_replaced"], QuestionFamilySourcePluralityRejected)
	}
}

// THE ENSEMBLE COMPOSITION LINE LEAKS NO CONTENT, held to the same allow-list
// discipline as the plan-carry and family-resolution lines beside it. Every
// member is an integer count, a boolean, the org id or the request id: nothing
// from the question, no family, no subject, no model identity. An allow-list,
// not a denylist, so a field added later must be admitted deliberately.
func TestInterpretationEnsembleTelemetryLeaksNoContent(t *testing.T) {
	ctx := observability.WithRequestID(context.Background(), "req_0123456789abcdef0123456789abcdef")
	for _, quorumMet := range []bool{false, true} {
		records := captureSlogJSON(t, func(logger *slog.Logger) {
			NewSlogEngineTelemetry(logger).RecordInterpretationEnsemble(ctx, storage.Principal{OrgID: "org_sink_test"},
				InterpretationEnsembleEvent{Requested: 3, PrimarySucceeded: 2, FallbackServed: 0, Failed: 1, QuorumMet: quorumMet})
		})
		if len(records) != 1 {
			t.Fatalf("quorum_met=%v: got %d records, want exactly 1 per ensemble turn", quorumMet, len(records))
		}
		allowed := map[string]bool{
			"time": true, "level": true, "msg": true, "request_id": true, "org_id": true,
			"requested": true, "primary_succeeded": true, "fallback_served": true, "failed": true, "quorum_met": true,
		}
		for key := range records[0] {
			if !allowed[key] {
				t.Errorf("quorum_met=%v: unexpected key %q on the ensemble composition line", quorumMet, key)
			}
		}
		for key := range allowed {
			if _, ok := records[0][key]; !ok {
				t.Errorf("quorum_met=%v: key %q is missing from the ensemble composition line", quorumMet, key)
			}
		}
	}
}
