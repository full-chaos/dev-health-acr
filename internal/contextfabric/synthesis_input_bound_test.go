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
	bounded, reduced, _ := boundSynthesisFacts(facts, factRanking{}, &ModelInputOverflow{Bytes: 200, MaxBytes: 100})

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
	bounded, reduced, _ := boundSynthesisFacts(facts, factRanking{committed: committed}, &ModelInputOverflow{Bytes: 200, MaxBytes: 100})

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
			bounded, reduced, _ := boundSynthesisFacts(testCase.facts, factRanking{}, testCase.overflow)
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
		FactsRead: 200, FactsGiven: 90, KindsRead: 2, KindsGiven: 2, KindsBounded: 1, Selection: SynthesisInputSelectionPosition,
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
			FactsRead: 200, FactsGiven: 2, KindsRead: 2, KindsGiven: 2, KindsBounded: 1, Selection: SynthesisInputSelectionPosition,
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

// A call that failed after the facts were bounded still says the facts were
// bounded: the degraded answer carries the bounded-input disclosure.
func TestAModelFailureAfterBoundingStillDisclosesTheBound(t *testing.T) {
	input := largeSynthesisInputFixture(199)
	draft := validSynthesisDraftFixture(input)
	title := "work_150"
	workItem := SubjectRef{Kind: SubjectWorkItem, CanonicalID: "work_150", Label: "work_150"}
	draft.Drivers[0].AffectedSubjects = append(draft.Drivers[0].AffectedSubjects, workItem)
	draft.Drivers[0].ClaimedFactIDs = []string{"claim_work_1"}
	draft.ClaimedFacts = []ClaimedFact{{ClaimID: "claim_work_1", Kind: FactWork, Field: "title", Value: ScalarValue{String: &title}, Subject: workItem}}
	var given []SynthesisInput
	synthesizer := RuntimeAnswerSynthesizer{Runtime: sizedModelRuntime{bytesPerFact: 10, maxBytes: 1000, draft: draft, given: &given}}
	principal := storage.Principal{OrgID: "org_1"}

	_, err := synthesizer.Synthesize(context.Background(), principal, input)

	var failure *SynthesisFailure
	if !errors.As(err, &failure) || failure.Bound == nil {
		t.Fatalf("Synthesize() error = %v, want a SynthesisFailure from a bounded input", err)
	}
	result, err := synthesizer.ComposeDegraded(context.Background(), principal, input, failure)
	if err != nil {
		t.Fatalf("ComposeDegraded() error = %v", err)
	}
	found := false
	for _, limitation := range result.Limitations {
		found = found || limitation == contractsv1.ContextFabricSynthesisInputBoundedLimitation
	}
	if !found {
		t.Fatalf("limitations = %q, want the bounded-input disclosure", result.Limitations)
	}
}

func boundDetailsOf(coverage Coverage) []CoverageDetail {
	var details []CoverageDetail
	for _, detail := range coverage.Details {
		if detail.Code == contractsv1.ContextFabricCoverageDetailSynthesisInputBounded {
			details = append(details, detail)
		}
	}
	return details
}

// wantBoundDetail reads one detail's fact kind and its two counts.
func wantBoundDetail(t *testing.T, detail CoverageDetail, kind FactKind, declared, served int) {
	t.Helper()
	if detail.FactKind != kind || detail.Declared == nil || *detail.Declared != declared || detail.Served == nil || *detail.Served != served {
		t.Fatalf("detail = %+v, want %s: %d read, %d given", detail, kind, declared, served)
	}
	if detail.Degrading {
		t.Fatalf("detail %s degrades: the bounded input is disclosed, not counted as a second degradation", kind)
	}
	if detail.DetailID == "" {
		detail.DetailID = "cov-01"
	}
	if err := detail.Validate(); err != nil {
		t.Fatalf("detail %s does not validate: %v", kind, err)
	}
}

// 199 work facts and the committed project's readiness fact were read; the
// runtime's bound kept 89 work facts and the readiness fact. The disclosure
// says so per kind, as numbers.
func TestABoundedInputIsDisclosedPerKindAsGivenOfRead(t *testing.T) {
	input := largeSynthesisInputFixture(199)
	var given []SynthesisInput
	synthesizer := RuntimeAnswerSynthesizer{Runtime: sizedModelRuntime{bytesPerFact: 10, maxBytes: 1000, draft: validSynthesisDraftFixture(input), given: &given}}

	result, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)

	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	details := boundDetailsOf(result.Coverage)
	if len(details) != 2 {
		t.Fatalf("bounded-input details = %+v, want one per fact kind read", details)
	}
	wantBoundDetail(t, details[0], FactReadiness, 1, 1)
	wantBoundDetail(t, details[1], FactWork, 199, 89)
	if got := contractsv1.ComposeCoverageDetailLabel(details[1]); details[1].Label != got || got != "89 of 199 work facts read were given to answer synthesis" {
		t.Fatalf("label = %q, want the composed %q", details[1].Label, got)
	}
}

// An input that fits is not disclosed as bounded.
func TestAnInputThatFitsHasNoBoundedInputDetail(t *testing.T) {
	input := largeSynthesisInputFixture(3)
	var given []SynthesisInput
	synthesizer := RuntimeAnswerSynthesizer{Runtime: sizedModelRuntime{bytesPerFact: 10, maxBytes: 1000, draft: validSynthesisDraftFixture(input), given: &given}}

	result, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)

	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if details := boundDetailsOf(result.Coverage); len(details) != 0 {
		t.Fatalf("bounded-input details = %+v, want none for an input that fit", details)
	}
}

// The degraded answer served after a bounded call carries the same numbers,
// and so does the degraded answer composed from a stored result's own details.
func TestAModelFailureAfterBoundingKeepsThePerKindDisclosure(t *testing.T) {
	input := largeSynthesisInputFixture(199)
	draft := validSynthesisDraftFixture(input)
	title := "work_150"
	workItem := SubjectRef{Kind: SubjectWorkItem, CanonicalID: "work_150", Label: "work_150"}
	draft.Drivers[0].AffectedSubjects = append(draft.Drivers[0].AffectedSubjects, workItem)
	draft.Drivers[0].ClaimedFactIDs = []string{"claim_work_1"}
	draft.ClaimedFacts = []ClaimedFact{{ClaimID: "claim_work_1", Kind: FactWork, Field: "title", Value: ScalarValue{String: &title}, Subject: workItem}}
	var given []SynthesisInput
	synthesizer := RuntimeAnswerSynthesizer{Runtime: sizedModelRuntime{bytesPerFact: 10, maxBytes: 1000, draft: draft, given: &given}}
	principal := storage.Principal{OrgID: "org_1"}

	_, err := synthesizer.Synthesize(context.Background(), principal, input)

	var failure *SynthesisFailure
	if !errors.As(err, &failure) {
		t.Fatalf("Synthesize() error = %v, want a SynthesisFailure", err)
	}
	result, err := synthesizer.ComposeDegraded(context.Background(), principal, input, failure)
	if err != nil {
		t.Fatalf("ComposeDegraded() error = %v", err)
	}
	details := boundDetailsOf(result.Coverage)
	if len(details) != 2 {
		t.Fatalf("bounded-input details = %+v, want one per fact kind read", details)
	}
	wantBoundDetail(t, details[0], FactReadiness, 1, 1)
	wantBoundDetail(t, details[1], FactWork, 199, 89)

	recovered := synthesisInputBoundOf(result)
	if recovered == nil || len(recovered.Details) != 2 {
		t.Fatalf("synthesisInputBoundOf = %+v, want both details recovered from the served answer", recovered)
	}
	result.Limitations = nil
	if synthesisInputBoundOf(result) != nil {
		t.Fatal("an answer without the bounded-input limitation was read as bounded")
	}
}

func TestTheBoundedInputDetailRequiresTheCountsAndTheFactKind(t *testing.T) {
	two, one := 2, 1
	valid := CoverageDetail{
		DetailID: "cov-01", Source: synthesisInputBoundSource, Code: contractsv1.ContextFabricCoverageDetailSynthesisInputBounded,
		FactKind: FactWork, Declared: &two, Served: &one, Label: "1 of 2 work facts read were given to answer synthesis",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid detail: %v", err)
	}
	cases := map[string]func(*CoverageDetail){
		"no fact kind":     func(d *CoverageDetail) { d.FactKind = "" },
		"no declared":      func(d *CoverageDetail) { d.Declared = nil },
		"no served":        func(d *CoverageDetail) { d.Served = nil },
		"served over read": func(d *CoverageDetail) { d.Served = &two; d.Declared = &one },
		"a subject kind":   func(d *CoverageDetail) { d.Kind = SubjectProject },
		"degrading":        func(d *CoverageDetail) { d.Degrading = true },
		"a count":          func(d *CoverageDetail) { d.Count = &one },
		"a source state":   func(d *CoverageDetail) { d.SourceState = SourceAvailable },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			detail := valid
			mutate(&detail)
			if err := detail.Validate(); err == nil {
				t.Fatalf("detail %+v validated, want it refused", detail)
			}
		})
	}
}

// A kind read whose facts were all cut from the input is still disclosed, at
// zero given: the kinds come from what was read, not from what was kept.
func TestTheBoundedInputDisclosesAKindThatNoFactSurvived(t *testing.T) {
	read := append(boundFixtureFacts(FactWork, SubjectWorkItem, 4), boundFixtureFacts(FactPullRequests, SubjectPullRequest, 3)...)
	given := boundFixtureFacts(FactWork, SubjectWorkItem, 2)

	bound := newSynthesisInputBound(read, given)

	if len(bound.Details) != 2 {
		t.Fatalf("details = %+v, want one per kind read", bound.Details)
	}
	wantBoundDetail(t, bound.Details[0], FactPullRequests, 3, 0)
	wantBoundDetail(t, bound.Details[1], FactWork, 4, 2)
}

func lateSourceDetailFixture() CoverageDetail {
	detail := CoverageDetail{
		Source: "zz:late-source", Code: contractsv1.ContextFabricCoverageDetailGraphExactNameCandidatesTruncated,
		Raw: "late disclosure",
	}
	detail.Label = contractsv1.ComposeCoverageDetailLabel(detail)
	return detail
}

func synthesizeBoundedWithLateDetail(t *testing.T, disclosures []CoverageDisclosure) InvestigationResult {
	t.Helper()
	input := largeSynthesisInputFixture(199)
	input.Facts.Coverage.Details = []CoverageDetail{lateSourceDetailFixture()}
	draft := validSynthesisDraftFixture(input)
	draft.CoverageDisclosures = disclosures
	var given []SynthesisInput
	synthesizer := RuntimeAnswerSynthesizer{Runtime: sizedModelRuntime{bytesPerFact: 10, maxBytes: 1000, draft: draft, given: &given}}
	result, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	return result
}

// The ids the model was shown stay the ids of the same details: the bounded
// input rows are appended after them, never merged in ahead of them, so a
// phrasing the model wrote for cov-01 still lands on the detail it saw as cov-01.
func TestTheBoundedInputDetailsDoNotMoveTheIdsTheModelWasShown(t *testing.T) {
	result := synthesizeBoundedWithLateDetail(t, []CoverageDisclosure{{DetailID: "cov-01", Text: "The late source was cut."}})

	if len(result.Coverage.Details) != 3 {
		t.Fatalf("details = %+v, want the late detail and two bounded-input details", result.Coverage.Details)
	}
	first := result.Coverage.Details[0]
	if first.DetailID != "cov-01" || first.Source != "zz:late-source" || first.Phrasing != "The late source was cut." {
		t.Fatalf("detail 0 = %+v, want cov-01 still the late source, carrying the model's phrasing", first)
	}
	for index, detail := range result.Coverage.Details[1:] {
		if want := fmt.Sprintf("cov-%02d", index+2); detail.DetailID != want || detail.Code != contractsv1.ContextFabricCoverageDetailSynthesisInputBounded || detail.Phrasing != "" {
			t.Fatalf("detail %d = %+v, want id %s, the bounded-input code and no phrasing", index+1, detail, want)
		}
	}
}

// A draft that names the id of a bounded-input detail names one the model was
// never shown: the whole disclosure set is discarded.
func TestADraftCannotPhraseABoundedInputDetailItWasNeverShown(t *testing.T) {
	result := synthesizeBoundedWithLateDetail(t, []CoverageDisclosure{{DetailID: "cov-02", Text: "Guessed an id."}})

	for _, detail := range result.Coverage.Details {
		if detail.Phrasing != "" {
			t.Fatalf("detail %+v carries phrasing, want the guessed-id disclosure set discarded", detail)
		}
	}
}

func TestTheCoverageCapKeepsTheBoundedInputDetails(t *testing.T) {
	var details []CoverageDetail
	for i := 0; i < 120; i++ {
		details = append(details, syntheticNonDegradingDetail(i))
	}
	bound := newSynthesisInputBound(boundFixtureFacts(FactWork, SubjectWorkItem, 4), boundFixtureFacts(FactWork, SubjectWorkItem, 2))
	details = append(details, bound.Details...)
	result := &InvestigationResult{Coverage: Coverage{Details: details}}

	omitted := capCoverageEntriesToWriteBound(result)

	if len(result.Coverage.Details) != contractsv1.ContextFabricCoverageEntriesMaxCount || omitted != 121-contractsv1.ContextFabricCoverageEntriesMaxCount {
		t.Fatalf("details = %d omitted = %d, want the cap at %d", len(result.Coverage.Details), omitted, contractsv1.ContextFabricCoverageEntriesMaxCount)
	}
	if kept := boundDetailsOf(result.Coverage); len(kept) != 1 {
		t.Fatalf("bounded-input details after the cap = %+v, want the one the answer owes", kept)
	}
}

// The validation-stage degrade keeps the coverage the answer already carries,
// bounded-input details included.
func TestTheValidationStageDegradeKeepsTheBoundedInputDetails(t *testing.T) {
	engine, _, result, basis := validationStageFixture(t)
	result.Limitations = []string{contractsv1.ContextFabricSynthesisInputBoundedLimitation}
	bound := newSynthesisInputBound(boundFixtureFacts(FactWork, SubjectWorkItem, 4), boundFixtureFacts(FactWork, SubjectWorkItem, 2))
	appendSynthesisInputBoundDetails(&result, bound, "org-1")
	if len(boundDetailsOf(result.Coverage)) != 1 {
		t.Fatalf("fixture coverage = %+v, want the one bounded-input detail", result.Coverage.Details)
	}

	out, ok := engine.degradeWorkItemModelBreach(context.Background(), storage.Principal{OrgID: "org-1"}, result, basis)

	if !ok {
		t.Fatal("not degraded")
	}
	details := boundDetailsOf(out.Coverage)
	if len(details) != 1 {
		t.Fatalf("bounded-input details after the degrade = %+v, want the one the answer carried", details)
	}
	wantBoundDetail(t, details[0], FactWork, 4, 2)
}

// Details that do not pair with degraded_reasons would make the write refuse
// the answer, so no bounded-input detail is filed on top of them.
func TestTheBoundedInputDetailsAreNotFiledOverUnpairedCoverage(t *testing.T) {
	result := InvestigationResult{Coverage: Coverage{DegradedReasons: []string{"a read failed"}}}
	bound := newSynthesisInputBound(boundFixtureFacts(FactWork, SubjectWorkItem, 4), boundFixtureFacts(FactWork, SubjectWorkItem, 2))

	appendSynthesisInputBoundDetails(&result, bound, "org_1")

	if len(result.Coverage.Details) != 0 {
		t.Fatalf("details = %+v, want none filed over unpaired coverage", result.Coverage.Details)
	}
}
