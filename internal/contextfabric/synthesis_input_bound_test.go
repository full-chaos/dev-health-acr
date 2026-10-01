package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func boundFixtureFacts(kind FactKind, subjectKind SubjectKind, count int) []CanonicalFact {
	facts := make([]CanonicalFact, 0, count)
	for index := 0; index < count; index++ {
		id := fmt.Sprintf("%s_%03d", kind, index)
		facts = append(facts, CanonicalFact{
			Kind: kind, Subject: SubjectRef{Kind: subjectKind, CanonicalID: id, Label: id},
			Fields: map[string]FactValue{"title": StringFactValue(id)}, SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1",
		})
	}
	return facts
}

func subjectIDs(facts []CanonicalFact, kind FactKind) []string {
	var ids []string
	for _, fact := range facts {
		if fact.Kind == kind {
			ids = append(ids, fact.Subject.CanonicalID)
		}
	}
	return ids
}

func TestBoundSynthesisFactsKeepsTheSameShareOfEveryKindInReadOrder(t *testing.T) {
	facts := append(boundFixtureFacts(FactWork, SubjectWorkItem, 100), boundFixtureFacts(FactPullRequests, SubjectPullRequest, 10)...)
	facts = append(facts, boundFixtureFacts(FactHealth, SubjectTeam, 1)...)

	// The input is twice the bound, so 90% of the bound is a share of 45%.
	bounded, reduced := boundSynthesisFacts(facts, nil, &ModelInputOverflow{Bytes: 200, MaxBytes: 100})

	if !reduced {
		t.Fatal("reduced = false, want true")
	}
	work, pulls, health := subjectIDs(bounded, FactWork), subjectIDs(bounded, FactPullRequests), subjectIDs(bounded, FactHealth)
	if len(work) != 45 || len(pulls) != 4 || len(health) != 1 {
		t.Fatalf("kept work=%d pull_requests=%d health=%d, want 45, 4 and 1: the same share of every kind, never less than one", len(work), len(pulls), len(health))
	}
	for index, id := range work {
		if want := fmt.Sprintf("work_%03d", index); id != want {
			t.Fatalf("work fact %d is %s, want %s: the first facts in read order stay", index, id, want)
		}
	}
	if len(facts) != 111 {
		t.Fatalf("the caller's slice has %d facts, want it unchanged at 111", len(facts))
	}
}

func TestBoundSynthesisFactsKeepsEveryFactOfACommittedSubject(t *testing.T) {
	facts := boundFixtureFacts(FactWork, SubjectWorkItem, 10)
	committed := []SubjectRef{facts[9].Subject, facts[8].Subject, facts[7].Subject, facts[6].Subject, facts[5].Subject}

	// A share of 45% of ten facts is four; the five committed subjects are
	// last in read order and all stay.
	bounded, reduced := boundSynthesisFacts(facts, committed, &ModelInputOverflow{Bytes: 200, MaxBytes: 100})

	if !reduced {
		t.Fatal("reduced = false, want true")
	}
	got := subjectIDs(bounded, FactWork)
	want := []string{"work_005", "work_006", "work_007", "work_008", "work_009"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("kept %v, want %v: committed subjects stay and count against the kind's share", got, want)
	}
}

func TestBoundSynthesisFactsReportsWhenNothingCanBeRemoved(t *testing.T) {
	one := boundFixtureFacts(FactHealth, SubjectTeam, 1)
	overflow := &ModelInputOverflow{Bytes: 200, MaxBytes: 100}
	cases := []struct {
		name     string
		facts    []CanonicalFact
		overflow *ModelInputOverflow
	}{
		{name: "one fact per kind", facts: append(boundFixtureFacts(FactWork, SubjectWorkItem, 1), one...), overflow: overflow},
		{name: "no facts", facts: nil, overflow: overflow},
		{name: "no overflow", facts: boundFixtureFacts(FactWork, SubjectWorkItem, 10), overflow: nil},
		{name: "an overflow with no size", facts: boundFixtureFacts(FactWork, SubjectWorkItem, 10), overflow: &ModelInputOverflow{Bytes: 0, MaxBytes: 100}},
		{name: "an overflow with no bound", facts: boundFixtureFacts(FactWork, SubjectWorkItem, 10), overflow: &ModelInputOverflow{Bytes: 200, MaxBytes: 0}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			bounded, reduced := boundSynthesisFacts(testCase.facts, nil, testCase.overflow)
			if reduced || len(bounded) != len(testCase.facts) {
				t.Fatalf("reduced = %v with %d of %d facts, want nothing removed", reduced, len(bounded), len(testCase.facts))
			}
		})
	}
}

// sizedModelRuntime refuses a synthesis input whose facts are larger than its
// bound, the way a model runtime measures its encoded input, and answers the
// draft for one that fits.
type sizedModelRuntime struct {
	bytesPerFact int
	maxBytes     int
	draft        SynthesisDraft
	given        *[]SynthesisInput
}

func (sizedModelRuntime) InterpretQuestion(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, ModelExecutionReceipt, error) {
	return InterpretedQuestion{}, ModelExecutionReceipt{}, errors.New("not used")
}

func (r sizedModelRuntime) SynthesizeAnswer(_ context.Context, _ storage.Principal, input SynthesisInput) (SynthesisDraft, ModelExecutionReceipt, error) {
	*r.given = append(*r.given, input)
	if size := len(input.Facts.Facts) * r.bytesPerFact; size > r.maxBytes {
		return SynthesisDraft{}, ModelExecutionReceipt{}, &ModelInputOverflow{Bytes: size, MaxBytes: r.maxBytes}
	}
	return r.draft, validModelReceiptFixture(ModelOperationSynthesize), nil
}

func largeSynthesisInputFixture(workItems int) SynthesisInput {
	input := validSynthesisInputFixture()
	input.Facts.Facts = append(input.Facts.Facts, boundFixtureFacts(FactWork, SubjectWorkItem, workItems)...)
	return input
}

func TestSynthesizeBoundsTheFactsWhenTheModelInputDoesNotFit(t *testing.T) {
	input := largeSynthesisInputFixture(199)
	var given []SynthesisInput
	telemetry := &recordingTelemetry{}
	synthesizer := RuntimeAnswerSynthesizer{
		Runtime:   sizedModelRuntime{bytesPerFact: 10, maxBytes: 1000, draft: validSynthesisDraftFixture(input), given: &given},
		Telemetry: telemetry,
	}

	result, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)

	if err != nil {
		t.Fatalf("Synthesize() error = %v, want the answer served", err)
	}
	// 200 facts of 10 bytes against a bound of 1000: one pass to a share of
	// 45%, 89 work facts and the committed project's own fact.
	if len(given) != 2 || len(given[0].Facts.Facts) != 200 || len(given[1].Facts.Facts) != 90 {
		t.Fatalf("the runtime was called %d times, want twice: 200 facts, then 90", len(given))
	}
	if kept := subjectIDs(given[1].Facts.Facts, FactReadiness); len(kept) != 1 || kept[0] != "project_ask_dev" {
		t.Fatalf("readiness facts given = %v, want the committed project's fact", kept)
	}
	if len(input.Facts.Facts) != 200 {
		t.Fatalf("the caller's input has %d facts, want it unchanged at 200", len(input.Facts.Facts))
	}
	if !result.Coverage.Partial {
		t.Fatal("coverage.partial = false, want true")
	}
	if result.Status != InvestigationPartial {
		t.Fatalf("status = %q, want %q: the draft said complete", result.Status, InvestigationPartial)
	}
	if len(result.Limitations) != 1 || result.Limitations[0] != contractsv1.ContextFabricSynthesisInputBoundedLimitation {
		t.Fatalf("limitations = %q, want the bounded-input disclosure", result.Limitations)
	}
	want := SynthesisInputBoundEvent{
		Outcome: SynthesisInputBoundFitted, Passes: 1, InputBytes: 2000, MaxInputBytes: 1000,
		FactsRead: 200, FactsGiven: 90, KindsRead: 2, KindsGiven: 2, KindsBounded: 1,
	}
	if len(telemetry.synthesisInputBounds) != 1 || telemetry.synthesisInputBounds[0] != want {
		t.Fatalf("bound events = %+v, want exactly %+v", telemetry.synthesisInputBounds, want)
	}
}

// An answer written from part of the facts is not complete. A complete status
// is lowered to partial; a status that already says more is kept.
func TestSynthesizeLowersACompleteStatusWhenTheFactsWereBounded(t *testing.T) {
	cases := []struct {
		drafted InvestigationStatus
		want    InvestigationStatus
	}{
		{drafted: InvestigationComplete, want: InvestigationPartial},
		{drafted: InvestigationPartial, want: InvestigationPartial},
		{drafted: InvestigationDegraded, want: InvestigationDegraded},
	}
	for _, testCase := range cases {
		t.Run(string(testCase.drafted), func(t *testing.T) {
			input := largeSynthesisInputFixture(199)
			draft := validSynthesisDraftFixture(input)
			draft.Status = testCase.drafted
			var given []SynthesisInput
			synthesizer := RuntimeAnswerSynthesizer{Runtime: sizedModelRuntime{bytesPerFact: 10, maxBytes: 1000, draft: draft, given: &given}}

			result, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)

			if err != nil {
				t.Fatalf("Synthesize() error = %v", err)
			}
			if len(given) != 2 {
				t.Fatalf("the runtime was called %d times, want 2: the facts were bounded", len(given))
			}
			if result.Status != testCase.want {
				t.Fatalf("status = %q, want %q", result.Status, testCase.want)
			}
		})
	}
}

// The bound event counts the kinds the model was given apart from the kinds
// read, so a kind that went missing from the model input would show.
func TestTheBoundEventCountsTheKindsGivenApartFromTheKindsRead(t *testing.T) {
	read := SynthesisInput{Facts: CanonicalFactBundle{Facts: append(boundFixtureFacts(FactWork, SubjectWorkItem, 4), boundFixtureFacts(FactPullRequests, SubjectPullRequest, 4)...)}}
	given := SynthesisInput{Facts: CanonicalFactBundle{Facts: boundFixtureFacts(FactWork, SubjectWorkItem, 4)}}
	telemetry := &recordingTelemetry{}

	RuntimeAnswerSynthesizer{Telemetry: telemetry}.recordSynthesisInputBound(context.Background(), storage.Principal{OrgID: "org_1"}, SynthesisInputBoundEvent{Outcome: SynthesisInputBoundFitted, Passes: 1}, read, given)

	want := SynthesisInputBoundEvent{Outcome: SynthesisInputBoundFitted, Passes: 1, FactsRead: 8, FactsGiven: 4, KindsRead: 2, KindsGiven: 1, KindsBounded: 1}
	if len(telemetry.synthesisInputBounds) != 1 || telemetry.synthesisInputBounds[0] != want {
		t.Fatalf("bound events = %+v, want exactly %+v", telemetry.synthesisInputBounds, want)
	}
}

// A claim is checked against the facts the model was given. A draft that
// claims a fact outside the bounded set is refused; the same claim about a
// fact inside it is served.
func TestSynthesizeChecksClaimsAgainstTheFactsTheModelWasGiven(t *testing.T) {
	cases := []struct {
		name     string
		workItem string
		wantErr  error
	}{
		{name: "a fact the model was given", workItem: "work_010"},
		{name: "a fact removed by the bound", workItem: "work_150", wantErr: ErrSynthesisRejected},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			input := largeSynthesisInputFixture(199)
			draft := validSynthesisDraftFixture(input)
			title := testCase.workItem
			workItem := SubjectRef{Kind: SubjectWorkItem, CanonicalID: testCase.workItem, Label: testCase.workItem}
			draft.Drivers[0].AffectedSubjects = append(draft.Drivers[0].AffectedSubjects, workItem)
			draft.Drivers[0].ClaimedFactIDs = []string{"claim_work_1"}
			draft.ClaimedFacts = []ClaimedFact{{
				ClaimID: "claim_work_1", Kind: FactWork, Field: "title", Value: ScalarValue{String: &title}, Subject: workItem,
			}}
			if err := draft.ValidateAgainst(input); err != nil {
				t.Fatalf("the draft is not valid against the unbounded input: %v", err)
			}
			var given []SynthesisInput
			synthesizer := RuntimeAnswerSynthesizer{Runtime: sizedModelRuntime{bytesPerFact: 10, maxBytes: 1000, draft: draft, given: &given}}

			result, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)

			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("Synthesize() error = %v, want %v", err, testCase.wantErr)
			}
			if testCase.wantErr == nil && len(result.ClaimedFacts) != 1 {
				t.Fatalf("claimed facts = %d, want the claim served", len(result.ClaimedFacts))
			}
		})
	}
}

// A model caveat the disclosure displaces from a full limitation list is
// counted on the result.
func TestSynthesizeCountsTheCaveatTheBoundedInputDisclosureDisplaces(t *testing.T) {
	input := largeSynthesisInputFixture(199)
	draft := validSynthesisDraftFixture(input)
	for index := 0; index < contractsv1.ContextFabricLimitationsMaxCount; index++ {
		draft.Limitations = append(draft.Limitations, fmt.Sprintf("Model caveat %d.", index))
	}
	var given []SynthesisInput
	synthesizer := RuntimeAnswerSynthesizer{Runtime: sizedModelRuntime{bytesPerFact: 10, maxBytes: 1000, draft: draft, given: &given}}

	result, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)

	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	last := result.Limitations[len(result.Limitations)-1]
	if len(result.Limitations) != contractsv1.ContextFabricLimitationsMaxCount || last != contractsv1.ContextFabricSynthesisInputBoundedLimitation || result.LimitationsDisplaced != 1 {
		t.Fatalf("limitations = %d ending %q, displaced = %d; want a full list ending with the disclosure and one caveat counted as displaced", len(result.Limitations), last, result.LimitationsDisplaced)
	}
}

func TestSynthesizeLeavesAnInputThatFitsAlone(t *testing.T) {
	input := largeSynthesisInputFixture(9)
	var given []SynthesisInput
	telemetry := &recordingTelemetry{}
	synthesizer := RuntimeAnswerSynthesizer{
		Runtime:   sizedModelRuntime{bytesPerFact: 10, maxBytes: 100, draft: validSynthesisDraftFixture(input), given: &given},
		Telemetry: telemetry,
	}

	result, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)

	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if len(given) != 1 || len(given[0].Facts.Facts) != 10 {
		t.Fatalf("the runtime was called %d times, want once with all 10 facts", len(given))
	}
	if result.Coverage.Partial || len(result.Limitations) != 0 || result.Status != InvestigationComplete {
		t.Fatalf("status = %q coverage.partial = %v limitations = %q, want the complete answer with no bound stated", result.Status, result.Coverage.Partial, result.Limitations)
	}
	if len(telemetry.synthesisInputBounds) != 0 {
		t.Fatalf("bound events = %+v, want none", telemetry.synthesisInputBounds)
	}
}

func TestSynthesizeEndsWithTheOverflowWhenBoundingIsExhausted(t *testing.T) {
	t.Run("nothing left to remove", func(t *testing.T) {
		input := largeSynthesisInputFixture(199)
		var given []SynthesisInput
		telemetry := &recordingTelemetry{}
		synthesizer := RuntimeAnswerSynthesizer{
			Runtime:   sizedModelRuntime{bytesPerFact: 1000, maxBytes: 1000, draft: validSynthesisDraftFixture(input), given: &given},
			Telemetry: telemetry,
		}

		_, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)

		var overflow *ModelInputOverflow
		if !errors.As(err, &overflow) || !errors.Is(err, ErrModelInputTooLarge) {
			t.Fatalf("Synthesize() error = %v, want the model input overflow", err)
		}
		// One pass leaves one work fact and the committed project's fact; two
		// facts of 1000 bytes still exceed the bound and neither can go.
		if overflow.Bytes != 2000 || overflow.MaxBytes != 1000 || len(given) != 2 {
			t.Fatalf("overflow = %+v after %d calls, want 2000 of 1000 bytes after 2 calls", overflow, len(given))
		}
		want := SynthesisInputBoundEvent{
			Outcome: SynthesisInputBoundExhausted, Passes: 1, InputBytes: 200_000, MaxInputBytes: 1000,
			FactsRead: 200, FactsGiven: 2, KindsRead: 2, KindsGiven: 2, KindsBounded: 1,
		}
		if len(telemetry.synthesisInputBounds) != 1 || telemetry.synthesisInputBounds[0] != want {
			t.Fatalf("bound events = %+v, want exactly %+v", telemetry.synthesisInputBounds, want)
		}
	})
	t.Run("the pass limit", func(t *testing.T) {
		input := largeSynthesisInputFixture(100_000)
		calls := 0
		telemetry := &recordingTelemetry{}
		synthesizer := RuntimeAnswerSynthesizer{
			Runtime:   overflowingModelRuntime{calls: &calls, overflow: &ModelInputOverflow{Bytes: 101, MaxBytes: 100}},
			Telemetry: telemetry,
		}

		_, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)

		if !errors.Is(err, ErrModelInputTooLarge) {
			t.Fatalf("Synthesize() error = %v, want the model input overflow", err)
		}
		if calls != maxSynthesisInputBoundPasses+1 {
			t.Fatalf("the runtime was called %d times, want %d: one per pass and the first", calls, maxSynthesisInputBoundPasses+1)
		}
		if len(telemetry.synthesisInputBounds) != 1 || telemetry.synthesisInputBounds[0].Outcome != SynthesisInputBoundExhausted || telemetry.synthesisInputBounds[0].Passes != maxSynthesisInputBoundPasses {
			t.Fatalf("bound events = %+v, want one exhausted event after %d passes", telemetry.synthesisInputBounds, maxSynthesisInputBoundPasses)
		}
	})
}

// overflowingModelRuntime refuses every synthesis input for its size.
type overflowingModelRuntime struct {
	calls    *int
	overflow *ModelInputOverflow
}

func (overflowingModelRuntime) InterpretQuestion(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, ModelExecutionReceipt, error) {
	return InterpretedQuestion{}, ModelExecutionReceipt{}, errors.New("not used")
}

func (r overflowingModelRuntime) SynthesizeAnswer(context.Context, storage.Principal, SynthesisInput) (SynthesisDraft, ModelExecutionReceipt, error) {
	*r.calls++
	return SynthesisDraft{}, ModelExecutionReceipt{}, r.overflow
}

func TestAnUnrecordedModelReceiptIsNamedAndKeepsItsCause(t *testing.T) {
	sinkFailure := errors.New("insert model receipt: connection reset")
	t.Run("the sink refuses the receipt", func(t *testing.T) {
		err := recordModelReceipt(context.Background(), storage.Principal{OrgID: "org_1"}, &fakeReceiptSink{err: sinkFailure}, validModelReceiptFixture(ModelOperationSynthesize))
		if !errors.Is(err, ErrModelReceiptUnrecorded) || !errors.Is(err, sinkFailure) {
			t.Fatalf("recordModelReceipt() error = %v, want the unrecorded-receipt sentinel over the sink's error", err)
		}
	})
	t.Run("the receipt is not valid", func(t *testing.T) {
		receipt := validModelReceiptFixture(ModelOperationSynthesize)
		receipt.Provider = ""
		sink := &fakeReceiptSink{}
		err := recordModelReceipt(context.Background(), storage.Principal{OrgID: "org_1"}, sink, receipt)
		if !errors.Is(err, ErrModelReceiptUnrecorded) || len(sink.recorded) != 0 {
			t.Fatalf("recordModelReceipt() error = %v with %d receipts recorded, want the unrecorded-receipt sentinel and nothing recorded", err, len(sink.recorded))
		}
	})
	t.Run("a recorded receipt", func(t *testing.T) {
		sink := &fakeReceiptSink{}
		if err := recordModelReceipt(context.Background(), storage.Principal{OrgID: "org_1"}, sink, validModelReceiptFixture(ModelOperationSynthesize)); err != nil || len(sink.recorded) != 1 {
			t.Fatalf("recordModelReceipt() error = %v with %d receipts recorded, want nil and one", err, len(sink.recorded))
		}
	})
}

func TestTheBoundedInputDisclosureIsServiceAuthored(t *testing.T) {
	if !contractsv1.IsContextFabricServiceAuthoredLimitation(contractsv1.ContextFabricSynthesisInputBoundedLimitation) {
		t.Fatal("the bounded-input disclosure is not in the service-authored list: a later disclosure could displace it")
	}
}
