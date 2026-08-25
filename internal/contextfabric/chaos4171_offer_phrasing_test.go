package contextfabric

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// --- classifyOfferPhrasingDraft: the closed-vocabulary guard ---

func offerOptions() []StructureOfferPhrasingOption {
	return []StructureOfferPhrasingOption{
		{OptionID: "opt_pr", Member: contractsv1.ContextFabricStructureNeedExpectedKind, Kind: contractsv1.ContextFabricSubjectPullRequest, Label: "a pull request"},
		{OptionID: "opt_wi", Member: contractsv1.ContextFabricStructureNeedExpectedKind, Kind: contractsv1.ContextFabricSubjectWorkItem, Label: "a work item"},
	}
}

func TestClassifyOfferPhrasingDraft_GeneratedOnWellFormedResponse(t *testing.T) {
	t.Parallel()
	input := StructureOfferPhrasingInput{Options: offerOptions()}
	draft := StructureOfferPhrasingDraft{Phrasings: []StructureOfferPhrasingEntry{
		{OptionID: "opt_pr", Phrasing: "an open pull request"},
		{OptionID: "opt_wi", Phrasing: "a tracked work item"},
	}}
	outcome, phrasings := classifyOfferPhrasingDraft(input, draft, nil)
	if outcome != OfferPhrasingGenerated {
		t.Fatalf("outcome = %q, want %q", outcome, OfferPhrasingGenerated)
	}
	if phrasings["opt_pr"] != "an open pull request" || phrasings["opt_wi"] != "a tracked work item" {
		t.Fatalf("phrasings = %#v", phrasings)
	}
}

// TestClassifyOfferPhrasingDraft_CallFailed is RED-FIRST evidence for the
// "the call fails ... the structural labels ship unchanged" fallback
// contract: a transport/provider error classifies to OfferPhrasingCallFailed
// with NO phrasings, regardless of what draft (if any) accompanied it.
func TestClassifyOfferPhrasingDraft_CallFailed(t *testing.T) {
	t.Parallel()
	input := StructureOfferPhrasingInput{Options: offerOptions()}
	draft := StructureOfferPhrasingDraft{Phrasings: []StructureOfferPhrasingEntry{{OptionID: "opt_pr", Phrasing: "an open pull request"}}}
	outcome, phrasings := classifyOfferPhrasingDraft(input, draft, errors.New("transport timeout"))
	if outcome != OfferPhrasingCallFailed {
		t.Fatalf("outcome = %q, want %q", outcome, OfferPhrasingCallFailed)
	}
	if phrasings != nil {
		t.Fatalf("phrasings = %#v, want nil on call failure", phrasings)
	}
}

// TestClassifyOfferPhrasingDraft_EmptyDraftFallsBackStructural is RED-FIRST
// evidence for the "unusable but not guard-violating" fallback path.
func TestClassifyOfferPhrasingDraft_EmptyDraftFallsBackStructural(t *testing.T) {
	t.Parallel()
	input := StructureOfferPhrasingInput{Options: offerOptions()}
	outcome, phrasings := classifyOfferPhrasingDraft(input, StructureOfferPhrasingDraft{}, nil)
	if outcome != OfferPhrasingFellBackStructural {
		t.Fatalf("outcome = %q, want %q", outcome, OfferPhrasingFellBackStructural)
	}
	if phrasings != nil {
		t.Fatalf("phrasings = %#v, want nil", phrasings)
	}
}

func TestClassifyOfferPhrasingDraft_OversizedEntryFallsBackStructural(t *testing.T) {
	t.Parallel()
	input := StructureOfferPhrasingInput{Options: offerOptions()}
	draft := StructureOfferPhrasingDraft{Phrasings: []StructureOfferPhrasingEntry{
		{OptionID: "opt_pr", Phrasing: strings.Repeat("a", phrasingMaxLabelLength+1)},
	}}
	outcome, phrasings := classifyOfferPhrasingDraft(input, draft, nil)
	if outcome != OfferPhrasingFellBackStructural {
		t.Fatalf("outcome = %q, want %q", outcome, OfferPhrasingFellBackStructural)
	}
	if phrasings != nil {
		t.Fatalf("phrasings = %#v, want nil", phrasings)
	}
}

// TestClassifyOfferPhrasingDraft_BoundsByRuneNotByte is RED-FIRST evidence
// for a codex review finding (chaos4171pr2-codex-r2): the bound must count
// runes, matching the contract's own optionalStringBetween
// (utf8.RuneCountInString), not bytes -- a multi-byte-rune string whose
// BYTE length exceeds the bound but whose RUNE count does not must still
// be accepted.
func TestClassifyOfferPhrasingDraft_BoundsByRuneNotByte(t *testing.T) {
	t.Parallel()
	input := StructureOfferPhrasingInput{Options: offerOptions()}
	// Each "é" is 2 bytes, 1 rune: 150 runes = 300 bytes, over the byte
	// bound but under the rune bound.
	text := strings.Repeat("é", 150)
	draft := StructureOfferPhrasingDraft{Phrasings: []StructureOfferPhrasingEntry{{OptionID: "opt_pr", Phrasing: text}}}
	outcome, phrasings := classifyOfferPhrasingDraft(input, draft, nil)
	if outcome != OfferPhrasingGenerated {
		t.Fatalf("outcome = %q, want %q (150 runes is within the %d-rune bound despite exceeding it in bytes)", outcome, OfferPhrasingGenerated, phrasingMaxLabelLength)
	}
	if phrasings["opt_pr"] != text {
		t.Fatalf("phrasings = %#v", phrasings)
	}
}

func TestClassifyOfferPhrasingDraft_EmptyTextFallsBackStructural(t *testing.T) {
	t.Parallel()
	input := StructureOfferPhrasingInput{Options: offerOptions()}
	draft := StructureOfferPhrasingDraft{Phrasings: []StructureOfferPhrasingEntry{{OptionID: "opt_pr", Phrasing: "   "}}}
	outcome, phrasings := classifyOfferPhrasingDraft(input, draft, nil)
	if outcome != OfferPhrasingFellBackStructural {
		t.Fatalf("outcome = %q, want %q", outcome, OfferPhrasingFellBackStructural)
	}
	if phrasings != nil {
		t.Fatalf("phrasings = %#v, want nil", phrasings)
	}
}

// TestClassifyOfferPhrasingDraft_UnknownOptionIDRejectedByGuard is RED-FIRST
// evidence for the closed-vocabulary guard's own core property: the model
// "may rephrase, never add ... options or change option_id" (ratified
// design). An option_id outside the offered set rejects the WHOLE
// response -- the well-formed opt_wi entry alongside it is discarded too,
// never applied partially.
func TestClassifyOfferPhrasingDraft_UnknownOptionIDRejectedByGuard(t *testing.T) {
	t.Parallel()
	input := StructureOfferPhrasingInput{Options: offerOptions()}
	draft := StructureOfferPhrasingDraft{Phrasings: []StructureOfferPhrasingEntry{
		{OptionID: "opt_wi", Phrasing: "a tracked work item"},
		{OptionID: "opt_invented", Phrasing: "an option that was never offered"},
	}}
	outcome, phrasings := classifyOfferPhrasingDraft(input, draft, nil)
	if outcome != OfferPhrasingRejectedByGuard {
		t.Fatalf("outcome = %q, want %q", outcome, OfferPhrasingRejectedByGuard)
	}
	if phrasings != nil {
		t.Fatalf("phrasings = %#v, want nil (whole-response rejection, not partial application)", phrasings)
	}
}

// TestClassifyOfferPhrasingDraft_DuplicateOptionIDRejectedByGuard is
// RED-FIRST evidence that a repeated option_id -- the model saying two
// different things about the same offer -- is also a guard violation, not
// silently resolved by last-write-wins.
func TestClassifyOfferPhrasingDraft_DuplicateOptionIDRejectedByGuard(t *testing.T) {
	t.Parallel()
	input := StructureOfferPhrasingInput{Options: offerOptions()}
	draft := StructureOfferPhrasingDraft{Phrasings: []StructureOfferPhrasingEntry{
		{OptionID: "opt_pr", Phrasing: "an open pull request"},
		{OptionID: "opt_pr", Phrasing: "a different phrasing for the same option"},
	}}
	outcome, phrasings := classifyOfferPhrasingDraft(input, draft, nil)
	if outcome != OfferPhrasingRejectedByGuard {
		t.Fatalf("outcome = %q, want %q", outcome, OfferPhrasingRejectedByGuard)
	}
	if phrasings != nil {
		t.Fatalf("phrasings = %#v, want nil", phrasings)
	}
}

// TestClassifyOfferPhrasingDraft_MalformedTextOnUnknownOptionIsStillRejectedByGuard
// is RED-FIRST evidence for a codex review finding (chaos4171pr2-codex-r1):
// an entry that is BOTH malformed (empty/oversized text) AND names an
// unknown option_id must classify as OfferPhrasingRejectedByGuard, not
// OfferPhrasingFellBackStructural -- the guard violation is the more
// specific fact. Both outcomes already apply nothing, so this only pins
// which reason is reported, not the safety property.
func TestClassifyOfferPhrasingDraft_MalformedTextOnUnknownOptionIsStillRejectedByGuard(t *testing.T) {
	t.Parallel()
	input := StructureOfferPhrasingInput{Options: offerOptions()}
	draft := StructureOfferPhrasingDraft{Phrasings: []StructureOfferPhrasingEntry{
		{OptionID: "opt_invented", Phrasing: ""},
	}}
	outcome, phrasings := classifyOfferPhrasingDraft(input, draft, nil)
	if outcome != OfferPhrasingRejectedByGuard {
		t.Fatalf("outcome = %q, want %q (membership must be checked before the text-shape bound)", outcome, OfferPhrasingRejectedByGuard)
	}
	if phrasings != nil {
		t.Fatalf("phrasings = %#v, want nil", phrasings)
	}
}

// TestClassifyOfferPhrasingDraft_MoreEntriesThanOfferedOptionsRejectedByGuard
// is RED-FIRST evidence for a codex review finding: a response carrying
// more entries than there are offered options is, by pigeonhole, already
// proof of a duplicate or unknown option_id -- rejected BEFORE any map is
// sized to the raw (potentially adversarial) response length.
func TestClassifyOfferPhrasingDraft_MoreEntriesThanOfferedOptionsRejectedByGuard(t *testing.T) {
	t.Parallel()
	input := StructureOfferPhrasingInput{Options: offerOptions()} // 2 options
	draft := StructureOfferPhrasingDraft{Phrasings: []StructureOfferPhrasingEntry{
		{OptionID: "opt_pr", Phrasing: "an open pull request"},
		{OptionID: "opt_wi", Phrasing: "a tracked work item"},
		{OptionID: "opt_wi", Phrasing: "a third entry"},
	}}
	outcome, phrasings := classifyOfferPhrasingDraft(input, draft, nil)
	if outcome != OfferPhrasingRejectedByGuard {
		t.Fatalf("outcome = %q, want %q", outcome, OfferPhrasingRejectedByGuard)
	}
	if phrasings != nil {
		t.Fatalf("phrasings = %#v, want nil", phrasings)
	}
}

// --- RuntimeOfferPhraser wiring ---

type fakeOfferPhrasingModelRuntime struct {
	draft   StructureOfferPhrasingDraft
	receipt ModelExecutionReceipt
	err     error
}

func (f fakeOfferPhrasingModelRuntime) PhraseStructureOffers(context.Context, storage.Principal, StructureOfferPhrasingInput) (StructureOfferPhrasingDraft, ModelExecutionReceipt, error) {
	return f.draft, f.receipt, f.err
}

var _ OfferPhrasingModelRuntime = fakeOfferPhrasingModelRuntime{}
var _ OfferPhraser = RuntimeOfferPhraser{}

func TestRuntimeOfferPhraser_NilRuntimeFallsBackStructural(t *testing.T) {
	t.Parallel()
	phraser := RuntimeOfferPhraser{}
	result := phraser.Phrase(context.Background(), storage.Principal{OrgID: "org_1"}, StructureOfferPhrasingInput{Options: offerOptions()})
	if result.Outcome != OfferPhrasingFellBackStructural {
		t.Fatalf("Outcome = %q, want %q", result.Outcome, OfferPhrasingFellBackStructural)
	}
	if result.Phrasings != nil {
		t.Fatalf("Phrasings = %#v, want nil", result.Phrasings)
	}
}

func TestRuntimeOfferPhraser_RecordsReceiptAndAppliesGuardOnSuccess(t *testing.T) {
	t.Parallel()
	sink := &fakeReceiptSink{}
	draft := StructureOfferPhrasingDraft{Phrasings: []StructureOfferPhrasingEntry{{OptionID: "opt_pr", Phrasing: "an open pull request"}}}
	phraser := RuntimeOfferPhraser{
		Runtime: fakeOfferPhrasingModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationPhraseOffers)},
		Sink:    sink,
	}
	result := phraser.Phrase(context.Background(), storage.Principal{OrgID: "org_1"}, StructureOfferPhrasingInput{Options: offerOptions()})
	if result.Outcome != OfferPhrasingGenerated {
		t.Fatalf("Outcome = %q, want %q", result.Outcome, OfferPhrasingGenerated)
	}
	if result.Phrasings["opt_pr"] != "an open pull request" {
		t.Fatalf("Phrasings = %#v", result.Phrasings)
	}
	if len(sink.recorded) != 1 || sink.recorded[0].Operation != ModelOperationPhraseOffers {
		t.Fatalf("sink.recorded = %#v", sink.recorded)
	}
}

func TestRuntimeOfferPhraser_CallErrorRecordsReceiptAndFallsBack(t *testing.T) {
	t.Parallel()
	sink := &fakeReceiptSink{}
	receipt := validModelReceiptFixture(ModelOperationPhraseOffers)
	receipt.Outcome = "provider_error"
	phraser := RuntimeOfferPhraser{
		Runtime: fakeOfferPhrasingModelRuntime{receipt: receipt, err: errors.New("provider unavailable")},
		Sink:    sink,
	}
	result := phraser.Phrase(context.Background(), storage.Principal{OrgID: "org_1"}, StructureOfferPhrasingInput{Options: offerOptions()})
	if result.Outcome != OfferPhrasingCallFailed {
		t.Fatalf("Outcome = %q, want %q", result.Outcome, OfferPhrasingCallFailed)
	}
	if len(sink.recorded) != 1 {
		t.Fatalf("sink.recorded = %#v, want exactly one receipt even on failure", sink.recorded)
	}
}

// TestRuntimeOfferPhraser_SinkFailurePreservesAMoreSpecificOutcome is
// RED-FIRST evidence for a codex review finding (chaos4171pr2-codex-r3): a
// simultaneous model-call failure AND receipt-sink failure must still
// report OfferPhrasingCallFailed, not be silently relabelled as the
// generic OfferPhrasingFellBackStructural -- losing that distinction would
// hide a genuine provider outage from telemetry during exactly the outage
// an operator most needs to see it.
func TestRuntimeOfferPhraser_SinkFailurePreservesAMoreSpecificOutcome(t *testing.T) {
	t.Parallel()
	sink := &fakeReceiptSink{err: errors.New("store unavailable")}
	receipt := validModelReceiptFixture(ModelOperationPhraseOffers)
	receipt.Outcome = "provider_error"
	phraser := RuntimeOfferPhraser{
		Runtime: fakeOfferPhrasingModelRuntime{receipt: receipt, err: errors.New("provider unavailable")},
		Sink:    sink,
	}
	result := phraser.Phrase(context.Background(), storage.Principal{OrgID: "org_1"}, StructureOfferPhrasingInput{Options: offerOptions()})
	if result.Outcome != OfferPhrasingCallFailed {
		t.Fatalf("Outcome = %q, want %q (the call failure is the more specific, more important fact)", result.Outcome, OfferPhrasingCallFailed)
	}
}

func TestRuntimeOfferPhraser_SinkFailureFallsBackStructural(t *testing.T) {
	t.Parallel()
	sink := &fakeReceiptSink{err: errors.New("store unavailable")}
	draft := StructureOfferPhrasingDraft{Phrasings: []StructureOfferPhrasingEntry{{OptionID: "opt_pr", Phrasing: "an open pull request"}}}
	phraser := RuntimeOfferPhraser{
		Runtime: fakeOfferPhrasingModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationPhraseOffers)},
		Sink:    sink,
	}
	result := phraser.Phrase(context.Background(), storage.Principal{OrgID: "org_1"}, StructureOfferPhrasingInput{Options: offerOptions()})
	if result.Outcome != OfferPhrasingFellBackStructural {
		t.Fatalf("Outcome = %q, want %q", result.Outcome, OfferPhrasingFellBackStructural)
	}
	if result.Phrasings != nil {
		t.Fatalf("Phrasings = %#v, want nil", result.Phrasings)
	}
}

// TestRuntimeOfferPhraser_SinkFailureLogsDistinctlyFromAnOrdinaryFallback
// is RED-FIRST evidence for a codex review finding (chaos4171pr2-codex-r1):
// a sink failure and unusable model output both collapse into the SAME
// OfferPhrasingFellBackStructural outcome on the closed telemetry
// vocabulary, so the logger call is the ONLY place an operator can tell
// "the model call worked but we lost the receipt" apart from "the model's
// own output was unusable".
func TestRuntimeOfferPhraser_SinkFailureLogsDistinctlyFromAnOrdinaryFallback(t *testing.T) {
	t.Parallel()
	var records []slog.Record
	logger := slog.New(recordingHandler{records: &records})
	sink := &fakeReceiptSink{err: errors.New("store unavailable")}
	draft := StructureOfferPhrasingDraft{Phrasings: []StructureOfferPhrasingEntry{{OptionID: "opt_pr", Phrasing: "an open pull request"}}}
	phraser := RuntimeOfferPhraser{
		Runtime: fakeOfferPhrasingModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationPhraseOffers)},
		Sink:    sink,
		Logger:  logger,
	}
	phraser.Phrase(context.Background(), storage.Principal{OrgID: "org_1"}, StructureOfferPhrasingInput{Options: offerOptions(), RequestID: "request_00000001"})
	if len(records) != 1 || records[0].Level != slog.LevelWarn {
		t.Fatalf("records = %#v, want exactly one WARN record", records)
	}
	if records[0].Message != "context fabric offer phrasing receipt sink failed" {
		t.Fatalf("message = %q", records[0].Message)
	}
}

type recordingHandler struct {
	records *[]slog.Record
}

func (h recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h recordingHandler) Handle(_ context.Context, record slog.Record) error {
	*h.records = append(*h.records, record)
	return nil
}
func (h recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h recordingHandler) WithGroup(string) slog.Handler      { return h }

// --- applyOfferPhrasing: the Engine-level hook ---

type fakeOfferPhraser struct {
	result StructureOfferPhrasingResult
	calls  []StructureOfferPhrasingInput
}

func (f *fakeOfferPhraser) Phrase(_ context.Context, _ storage.Principal, input StructureOfferPhrasingInput) StructureOfferPhrasingResult {
	f.calls = append(f.calls, input)
	return f.result
}

func TestApplyOfferPhrasing_NilNeedsPassesThrough(t *testing.T) {
	t.Parallel()
	phraser := &fakeOfferPhraser{result: StructureOfferPhrasingResult{Outcome: OfferPhrasingGenerated}}
	e := &Engine{offerPhraser: phraser}
	if got := e.applyOfferPhrasing(context.Background(), storage.Principal{OrgID: "org_1"}, "request_00000001", nil); got != nil {
		t.Fatalf("applyOfferPhrasing(nil) = %+v, want nil", got)
	}
	if len(phraser.calls) != 0 {
		t.Fatalf("phraser called %d times on nil needs, want 0", len(phraser.calls))
	}
}

// TestApplyOfferPhrasing_NoPhraserConfiguredLeavesNeedsUnchanged is
// RED-FIRST evidence that a deployment with no phrasing model behaves
// byte-identically to before this ticket: no call attempted, Label stands
// alone, Phrasing stays empty.
func TestApplyOfferPhrasing_NoPhraserConfiguredLeavesNeedsUnchanged(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	needs := &contractsv1.ContextFabricStructureNeeds{
		Missing:     []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind},
		KindOptions: []contractsv1.ContextFabricKindOption{{OptionID: "opt_pr", Label: "a pull request", Kind: contractsv1.ContextFabricSubjectPullRequest, OfferSource: contractsv1.ContextFabricStructureOfferEngine, ReceiptID: "kindr_x"}},
	}
	got := e.applyOfferPhrasing(context.Background(), storage.Principal{OrgID: "org_1"}, "request_00000001", needs)
	if got != needs {
		t.Fatalf("applyOfferPhrasing() returned a different pointer with no phraser configured")
	}
	if got.KindOptions[0].Phrasing != "" {
		t.Fatalf("KindOptions[0].Phrasing = %q, want empty (no phraser configured)", got.KindOptions[0].Phrasing)
	}
}

func TestApplyOfferPhrasing_WindowOnlyNeedsSkipsThePhraserEntirely(t *testing.T) {
	t.Parallel()
	phraser := &fakeOfferPhraser{result: StructureOfferPhrasingResult{Outcome: OfferPhrasingGenerated}}
	telemetry := &recordingTelemetry{}
	e := &Engine{offerPhraser: phraser, telemetry: telemetry}
	needs := &contractsv1.ContextFabricStructureNeeds{
		Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedWindow},
		WindowOptions: []contractsv1.ContextFabricWindowOption{
			{Label: "last 7 days", RelativeID: "last_7_days", ReceiptID: "winr_x", OptionID: "opt_win"},
		},
	}
	e.applyOfferPhrasing(context.Background(), storage.Principal{OrgID: "org_1"}, "request_00000001", needs)
	if len(phraser.calls) != 0 {
		t.Fatalf("phraser called %d times for a window-only disclosure, want 0", len(phraser.calls))
	}
	if len(telemetry.offerPhrasingOutcomes) != 0 {
		t.Fatalf("telemetry.offerPhrasingOutcomes = %#v, want none recorded (nothing attempted)", telemetry.offerPhrasingOutcomes)
	}
}

// TestApplyOfferPhrasing_GeneratedOutcomeAppliesPhrasingAcrossOptionTypes
// proves the hook rewrites Phrasing on every phraseable option type by
// option_id, and never touches WindowOptions (no Phrasing field to write).
func TestApplyOfferPhrasing_GeneratedOutcomeAppliesPhrasingAcrossOptionTypes(t *testing.T) {
	t.Parallel()
	phrasings := map[string]string{
		"opt_kind": "which kind of work?",
		"opt_anch": "the ask-dev repo",
		"opt_hand": "PR number 42",
		"opt_cand": "did you mean WU-9?",
	}
	phraser := &fakeOfferPhraser{result: StructureOfferPhrasingResult{Outcome: OfferPhrasingGenerated, Phrasings: phrasings}}
	telemetry := &recordingTelemetry{}
	e := &Engine{offerPhraser: phraser, telemetry: telemetry}
	needs := &contractsv1.ContextFabricStructureNeeds{
		Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind},
		KindOptions: []contractsv1.ContextFabricKindOption{
			{OptionID: "opt_kind", Label: "a work item", Kind: contractsv1.ContextFabricSubjectWorkItem, OfferSource: contractsv1.ContextFabricStructureOfferEngine, ReceiptID: "kindr_confirm001"},
		},
		AnchorOptions: []contractsv1.ContextFabricAnchorOption{
			{OptionID: "opt_anch", Label: "ask-dev", Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repository_ask_dev", MatchedTermHash: "aa11bb22cc33dd44ee55ff66", OfferSource: contractsv1.ContextFabricStructureOfferEngine, ReceiptID: "ancr_confirm0001"},
		},
		HandleOptions: []contractsv1.ContextFabricHandleOption{
			{OptionID: "opt_hand", Label: "PR #42", Kind: contractsv1.ContextFabricSubjectPullRequest, PatternID: "pr_number", Value: "42", SourceColumn: "pull_requests.number", OfferSource: contractsv1.ContextFabricStructureOfferEngine, ReceiptID: "handr_confirm001"},
		},
		CandidateOptions: []contractsv1.ContextFabricCandidateOption{
			{OptionID: "opt_cand", Label: "WU-9", Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: "work_item_wu_9", OfferSource: contractsv1.ContextFabricStructureOfferEngine, ReceiptID: "candr_confirm001"},
		},
		WindowOptions: []contractsv1.ContextFabricWindowOption{
			{Label: "all time", RelativeID: contractsv1.ContextFabricRelativeWindowAllTime, ReceiptID: "winr_confirm0001", OptionID: "opt_win"},
		},
	}
	got := e.applyOfferPhrasing(context.Background(), storage.Principal{OrgID: "org_1"}, "request_00000001", needs)
	if got.KindOptions[0].Phrasing != "which kind of work?" {
		t.Errorf("KindOptions[0].Phrasing = %q", got.KindOptions[0].Phrasing)
	}
	if got.AnchorOptions[0].Phrasing != "the ask-dev repo" {
		t.Errorf("AnchorOptions[0].Phrasing = %q", got.AnchorOptions[0].Phrasing)
	}
	if got.HandleOptions[0].Phrasing != "PR number 42" {
		t.Errorf("HandleOptions[0].Phrasing = %q", got.HandleOptions[0].Phrasing)
	}
	if got.CandidateOptions[0].Phrasing != "did you mean WU-9?" {
		t.Errorf("CandidateOptions[0].Phrasing = %q", got.CandidateOptions[0].Phrasing)
	}
	// Every structural field survives untouched -- phrasing rewrites
	// Phrasing only, never Label/Kind/OptionID/ReceiptID.
	if got.KindOptions[0].Label != "a work item" || got.KindOptions[0].OptionID != "opt_kind" {
		t.Errorf("KindOptions[0] structural fields mutated: %+v", got.KindOptions[0])
	}
	if len(telemetry.offerPhrasingOutcomes) != 1 || telemetry.offerPhrasingOutcomes[0] != OfferPhrasingGenerated {
		t.Fatalf("telemetry.offerPhrasingOutcomes = %#v, want [%q]", telemetry.offerPhrasingOutcomes, OfferPhrasingGenerated)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("applyOfferPhrasing() result fails Validate(): %v", err)
	}
}

// TestApplyOfferPhrasing_RejectedByGuardLeavesStructuralLabelsUnchanged is
// RED-FIRST evidence for the fail-open contract at the Engine hook level:
// a guard-rejected outcome must never write into Phrasing, and the
// telemetry must still record the attempt.
func TestApplyOfferPhrasing_RejectedByGuardLeavesStructuralLabelsUnchanged(t *testing.T) {
	t.Parallel()
	phraser := &fakeOfferPhraser{result: StructureOfferPhrasingResult{Outcome: OfferPhrasingRejectedByGuard}}
	telemetry := &recordingTelemetry{}
	e := &Engine{offerPhraser: phraser, telemetry: telemetry}
	needs := &contractsv1.ContextFabricStructureNeeds{
		Missing:     []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind},
		KindOptions: []contractsv1.ContextFabricKindOption{{OptionID: "opt_kind", Label: "a work item", Kind: contractsv1.ContextFabricSubjectWorkItem, OfferSource: contractsv1.ContextFabricStructureOfferEngine, ReceiptID: "kindr_confirm001"}},
	}
	got := e.applyOfferPhrasing(context.Background(), storage.Principal{OrgID: "org_1"}, "request_00000001", needs)
	if got.KindOptions[0].Phrasing != "" {
		t.Fatalf("KindOptions[0].Phrasing = %q, want empty on guard rejection", got.KindOptions[0].Phrasing)
	}
	if got.KindOptions[0].Label != "a work item" {
		t.Fatalf("KindOptions[0].Label = %q, want unchanged structural label", got.KindOptions[0].Label)
	}
	if len(telemetry.offerPhrasingOutcomes) != 1 || telemetry.offerPhrasingOutcomes[0] != OfferPhrasingRejectedByGuard {
		t.Fatalf("telemetry.offerPhrasingOutcomes = %#v, want [%q]", telemetry.offerPhrasingOutcomes, OfferPhrasingRejectedByGuard)
	}
}

// --- phrasableOptions / applyPhrasings ---

func TestPhrasableOptions_ExcludesWindowAndAcceptedGrammars(t *testing.T) {
	t.Parallel()
	needs := &contractsv1.ContextFabricStructureNeeds{
		KindOptions:      []contractsv1.ContextFabricKindOption{{OptionID: "opt_kind", Label: "a work item"}},
		WindowOptions:    []contractsv1.ContextFabricWindowOption{{OptionID: "opt_win", Label: "last 7 days"}},
		AcceptedGrammars: []contractsv1.ContextFabricAcceptedGrammar{{Member: contractsv1.ContextFabricStructureNeedSubjectHandle, PatternID: "pr_number"}},
	}
	options := phrasableOptions(needs)
	if len(options) != 1 || options[0].OptionID != "opt_kind" {
		t.Fatalf("phrasableOptions() = %#v, want exactly the one KindOption", options)
	}
}
