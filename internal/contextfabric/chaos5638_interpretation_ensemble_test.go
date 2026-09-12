package contextfabric

import (
	"context"
	"errors"
	"fmt"
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
	mu      sync.Mutex
	asked   []int
	perIdx  map[int]InterpretedQuestion
	errIdx  map[int]error
	receipt ModelExecutionReceipt
}

func (s *sampledRuntimeStub) InterpretQuestionForSample(_ context.Context, _ storage.Principal, _ InvestigationRequest, sample int) (InterpretedQuestion, ModelExecutionReceipt, error) {
	s.mu.Lock()
	s.asked = append(s.asked, sample)
	s.mu.Unlock()
	if err, ok := s.errIdx[sample]; ok {
		return InterpretedQuestion{}, s.receipt, err
	}
	return s.perIdx[sample], s.receipt, nil
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

// THE GATE IS STAMPED FROM THE WINNING SAMPLE'S RECEIPT. Every sample here
// carries the same receipt, so this pins that the stamp happens at all on the
// ensemble path -- without it the ensemble would return an outcome whose frame
// verdict was never set, and the engine's refusal gate would read
// not_evaluated on every turn.
func TestTheEnsembleStampsTheFrameGateFromAReceipt(t *testing.T) {
	t.Parallel()
	receipt := validModelReceiptFixture(ModelOperationInterpret)
	receipt.FrameGateOutcome = FrameGateNotProposed
	sampled := &sampledRuntimeStub{
		perIdx: map[int]InterpretedQuestion{
			0: ensembleQuestion(ShapeSingleSubject, "s0"),
			1: ensembleQuestion(ShapeSingleSubject, "s1"),
			2: ensembleQuestion(ShapeSingleSubject, "s2"),
		},
		receipt: receipt,
	}
	interpreter := RuntimeQuestionInterpreter{SampledRuntime: sampled, Sink: &concurrentReceiptSink{}, EnsembleSize: 3}

	_, outcome, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if outcome.Gate.Outcome != FrameGateNotProposed {
		t.Fatalf("gate outcome = %q, want it carried off the winning sample's receipt", outcome.Gate.Outcome)
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
