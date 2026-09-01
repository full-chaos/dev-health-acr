package contextfabric

import (
	"context"
	"errors"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// --- CHAOS-4355 follow-up: a model that still authors ClaimedFact.Rows
// (despite the prompt no longer showing it any Rows-shaped field --
// genkitruntime.modelFacingFacts) must be TOLERATED, not have its whole
// otherwise-valid answer rejected. This file proves the tolerance itself
// (strip + telemetry), the diagnosis mirror naming the bound when the strip
// is bypassed, and the claim-index attribution the server-side log line
// depends on. ---

// rowsAuthoredDraft is closureFixture's valid draft, with the SAME
// model-authored-Rows shape TestSynthesisDraftValidateAgainstRejectsClaimedFactSettingRows
// pins as a hard rejection at the ValidateAgainst layer -- this file proves
// what happens one layer up, at RuntimeAnswerSynthesizer.Synthesize, where
// the tolerance is supposed to intervene BEFORE ValidateAgainst ever sees
// it.
func rowsAuthoredDraft() (SynthesisInput, SynthesisDraft) {
	input, draft := closureFixture()
	value := "fabricated"
	draft.ClaimedFacts[0].Rows = []ClaimedFactRow{{Fields: map[string]ScalarValue{"anything": {String: &value}}}}
	return input, draft
}

// TestRuntimeAnswerSynthesizerStripsModelAuthoredRowsInsteadOfRejecting is
// the RED-FIRST proof for the tolerance build: before it existed, a model
// that authored Rows on an otherwise closure-passing claim (identical
// scenario to TestSynthesisDraftValidateAgainstRejectsClaimedFactSettingRows)
// made RuntimeAnswerSynthesizer.Synthesize return ErrSynthesisRejected for
// the WHOLE answer -- a live 3/3 422 on kiac pilot rev 19, CHAOS-4355 diag
// 19:10 08-27, even though the claim's scalar value-level closure (Value
// against the canonical fact) was perfectly valid. After the fix, the same
// draft must succeed, with the fabricated Rows discarded rather than
// reaching the projected answer (this fixture's canonical fact carries no
// Rows-shaped field of its own, so the result's Rows must come back nil,
// never the model's fabricated content).
func TestRuntimeAnswerSynthesizerStripsModelAuthoredRowsInsteadOfRejecting(t *testing.T) {
	t.Parallel()
	input, draft := rowsAuthoredDraft()
	synthesizer := RuntimeAnswerSynthesizer{
		Runtime: fakeModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationSynthesize)},
		Sink:    &fakeReceiptSink{},
	}
	result, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)
	if err != nil {
		t.Fatalf("Synthesize() error = %v, want the model-authored Rows tolerated (stripped), not the whole answer rejected", err)
	}
	if len(result.ClaimedFacts) != 1 {
		t.Fatalf("ClaimedFacts = %v, want exactly 1", result.ClaimedFacts)
	}
	if result.ClaimedFacts[0].Rows != nil {
		t.Fatalf("ClaimedFacts[0].Rows = %+v, want nil -- the canonical fact this claim cites carries no Rows-shaped field, so the model's fabricated Rows must never reach the projected answer", result.ClaimedFacts[0].Rows)
	}
}

// TestRuntimeAnswerSynthesizerRecordsModelRowsStrippedTelemetry proves the
// cf_model_rows_stripped dimension fires with the exact claim count when a
// strip actually happens.
func TestRuntimeAnswerSynthesizerRecordsModelRowsStrippedTelemetry(t *testing.T) {
	t.Parallel()
	input, draft := rowsAuthoredDraft()
	telemetry := &recordingTelemetry{}
	synthesizer := RuntimeAnswerSynthesizer{
		Runtime:   fakeModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationSynthesize)},
		Sink:      &fakeReceiptSink{},
		Telemetry: telemetry,
	}
	if _, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input); err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if len(telemetry.modelRowsStripped) != 1 || telemetry.modelRowsStripped[0] != 1 {
		t.Fatalf("modelRowsStripped = %v, want exactly one record of 1 (one claim stripped)", telemetry.modelRowsStripped)
	}
}

// TestRuntimeAnswerSynthesizerDoesNotRecordModelRowsStrippedWhenNoneStripped
// is the negative half: a call where no claim ever carried Rows must not
// report anything here -- "nothing to do is not an outcome", the same
// convention every other gated EngineTelemetry method in this package
// follows (unlike RecordProjectedRowsCount, which is unconditional by its
// own, different, contract).
func TestRuntimeAnswerSynthesizerDoesNotRecordModelRowsStrippedWhenNoneStripped(t *testing.T) {
	t.Parallel()
	input, draft := closureFixture()
	telemetry := &recordingTelemetry{}
	synthesizer := RuntimeAnswerSynthesizer{
		Runtime:   fakeModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationSynthesize)},
		Sink:      &fakeReceiptSink{},
		Telemetry: telemetry,
	}
	if _, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input); err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if len(telemetry.modelRowsStripped) != 0 {
		t.Fatalf("modelRowsStripped = %v, want none recorded when nothing was stripped", telemetry.modelRowsStripped)
	}
}

// TestStripModelAuthoredClaimedFactTableContent is the strip helper's own
// direct unit test: it must clear Rows AND the CHAOS-4637 Table
// declaration, report the exact count, and leave every claim that carried
// neither byte-identical.
func TestStripModelAuthoredClaimedFactTableContent(t *testing.T) {
	t.Parallel()
	value := "fabricated"
	claims := []ClaimedFact{
		{ClaimID: "claim_clean", Kind: FactReadiness, Field: "release_ready", Value: boolScalar(true)},
		{ClaimID: "claim_dirty", Kind: FactReadiness, Field: "release_ready", Value: boolScalar(false),
			Rows: []ClaimedFactRow{{Fields: map[string]ScalarValue{"anything": {String: &value}}}}},
		// CHAOS-4682 (§5.1 P2): a third claim carrying ONLY the additive
		// TimeSeriesRows pair -- proving the strip clears it independently
		// of Rows, not merely alongside it.
		{ClaimID: "claim_dirty_time_series", Kind: FactReadiness, Field: "release_ready", Value: boolScalar(false),
			TimeSeriesRows: []ClaimedFactRow{{Fields: map[string]ScalarValue{"anything": {String: &value}}}}},
	}
	cleaned, stripped := StripModelAuthoredClaimedFactTableContent(claims)
	if stripped != 2 {
		t.Fatalf("stripped = %d, want 2", stripped)
	}
	if cleaned[0].Rows != nil {
		t.Fatalf("cleaned[0].Rows = %+v, want nil (unchanged -- never carried Rows)", cleaned[0].Rows)
	}
	if cleaned[1].Rows != nil {
		t.Fatalf("cleaned[1].Rows = %+v, want nil (cleared)", cleaned[1].Rows)
	}
	if claims[1].Rows == nil {
		t.Fatalf("claims[1].Rows was mutated in place, want the input slice's claim left untouched")
	}
	if cleaned[2].TimeSeriesRows != nil {
		t.Fatalf("cleaned[2].TimeSeriesRows = %+v, want nil (cleared)", cleaned[2].TimeSeriesRows)
	}
	if claims[2].TimeSeriesRows == nil {
		t.Fatalf("claims[2].TimeSeriesRows was mutated in place, want the input slice's claim left untouched")
	}
}

// TestAModelAuthoredTableIsStrippedNotRejected is codex round 2 finding 1
// (P2, EXECUTED by the reviewer and re-run here before being ledgered).
//
// CHAOS-4637 added ClaimedFact.Table. The model-output DTO is DERIVED FROM
// THAT STRUCT by schema inference, so the field silently became something a
// model may return — and unlike Rows it was not stripped. A model could
// author a syntactically valid declaration beside a correct scalar claim
// and NO rows, and the wire validator would then refuse the whole document.
// Observed on the tip before the fix:
//
//	stripped=0 table_nil=false
//	validate=table: declared table describes rows the fact does not carry
//
// A benign hallucination turning a good answer into a failed one is exactly
// what CHAOS-4355's strip-and-tolerate discipline exists to prevent.
func TestAModelAuthoredTableIsStrippedNotRejected(t *testing.T) {
	t.Parallel()
	claim := ClaimedFact{
		ClaimID: "claim_model_authored_table", Kind: FactReadiness,
		Field: "release_ready", Value: boolScalar(true),
		// NO Rows: the pre-4637 strip has nothing to do here, which is
		// precisely why the gap existed.
		Table: &contractsv1.ContextFabricClaimedFactTable{
			Field: "daily_readiness", Shape: contractsv1.ContextFabricFactTableShapeTimeSeries,
			Key: []string{"day"}, Measures: []string{"coverage_ratio"},
		},
	}
	// Non-vacuity: the hallucinated declaration is SYNTACTICALLY VALID on
	// its own, so what follows is about the strip and not about a
	// malformed fixture being rejected for some other reason.
	if err := claim.Table.Validate(); err != nil {
		t.Fatalf("fixture declaration is malformed (%v); a rejection would not be attributable to the strip", err)
	}

	cleaned, stripped := StripModelAuthoredClaimedFactTableContent([]ClaimedFact{claim})
	if stripped != 1 {
		t.Fatalf("stripped = %d, want 1 -- a model-authored declaration must be COUNTED, not silently kept", stripped)
	}
	if cleaned[0].Table != nil {
		t.Fatalf("cleaned[0].Table = %+v, want nil -- a declaration is attached server-side from the cited canonical fact, never authored", cleaned[0].Table)
	}
	if claim.Table == nil {
		t.Fatal("the input claim was mutated in place")
	}
	// And the stripped claim now validates on the wire, which is the whole
	// point: tolerated, not rejected.
	wire := contractsv1.ContextFabricClaimedFact{
		ClaimID: cleaned[0].ClaimID, Kind: cleaned[0].Kind,
		Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: "team:x", Label: "x"},
		Field:   cleaned[0].Field, Value: contractsv1.ContextFabricScalarValue{Boolean: cleaned[0].Value.Boolean},
		Rows: cleaned[0].Rows, Table: cleaned[0].Table,
	}
	if err := wire.Validate(); err != nil {
		t.Fatalf("the stripped claim still does not validate: %v", err)
	}
}

// TestAModelAuthoredTimeSeriesTableIsStrippedNotRejected mirrors
// TestAModelAuthoredTableIsStrippedNotRejected exactly, for
// TimeSeriesTable (CHAOS-4682, §5.1 P2): the same class of gap
// StripModelAuthoredClaimedFactTableContent's own doc comment names --
// "ANY field added to a model-output DTO becomes model-authorable, and
// must be either grounded or stripped" -- applied to the field this
// ticket added.
func TestAModelAuthoredTimeSeriesTableIsStrippedNotRejected(t *testing.T) {
	t.Parallel()
	claim := ClaimedFact{
		ClaimID: "claim_model_authored_time_series_table", Kind: FactReadiness,
		Field: "release_ready", Value: boolScalar(true),
		// NO TimeSeriesRows: a hallucinated declaration with nothing to
		// describe is exactly the shape that turned a good answer into a
		// rejected one for Table before this strip existed.
		TimeSeriesTable: &contractsv1.ContextFabricClaimedFactTable{
			Field: "daily_readiness", Shape: contractsv1.ContextFabricFactTableShapeTimeSeries,
			Key: []string{"day"}, Measures: []string{"coverage_ratio"},
		},
	}
	if err := claim.TimeSeriesTable.Validate(); err != nil {
		t.Fatalf("fixture declaration is malformed (%v); a rejection would not be attributable to the strip", err)
	}

	cleaned, stripped := StripModelAuthoredClaimedFactTableContent([]ClaimedFact{claim})
	if stripped != 1 {
		t.Fatalf("stripped = %d, want 1 -- a model-authored declaration must be COUNTED, not silently kept", stripped)
	}
	if cleaned[0].TimeSeriesTable != nil {
		t.Fatalf("cleaned[0].TimeSeriesTable = %+v, want nil -- a declaration is attached server-side from the cited canonical fact, never authored", cleaned[0].TimeSeriesTable)
	}
	if claim.TimeSeriesTable == nil {
		t.Fatal("the input claim was mutated in place")
	}
	wire := contractsv1.ContextFabricClaimedFact{
		ClaimID: cleaned[0].ClaimID, Kind: cleaned[0].Kind,
		Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: "team:x", Label: "x"},
		Field:   cleaned[0].Field, Value: contractsv1.ContextFabricScalarValue{Boolean: cleaned[0].Value.Boolean},
		TimeSeriesRows: cleaned[0].TimeSeriesRows, TimeSeriesTable: cleaned[0].TimeSeriesTable,
	}
	if err := wire.Validate(); err != nil {
		t.Fatalf("the stripped claim still does not validate: %v", err)
	}
}

// TestDiagnoseSynthesisDraftBoundReportsRowsAuthorshipBound is the (c)
// drift-regression proof: before this statement existed in
// diagnoseSynthesisDraftBound, a Rows-authorship rejection fell through
// every remaining check with NO bound name (CHAOS-4355 diagnosis, 19:10
// 08-27) -- the live 422 body carried no violated_bound at all. This test
// calls ValidateAgainst directly (never the tolerance path) to prove the
// mirror still names the SAME rejection ValidateAgainst itself produces,
// with the correct claim index.
func TestDiagnoseSynthesisDraftBoundReportsRowsAuthorshipBound(t *testing.T) {
	t.Parallel()
	input, draft := rowsAuthoredDraft()
	validateErr := draft.ValidateAgainst(input)
	if validateErr == nil {
		t.Fatalf("ValidateAgainst() error = nil, want a sets-rows rejection (test precondition)")
	}
	err := ClassifySynthesisRejection(draft, input, validateErr)
	var violation *ModelBoundViolation
	if !errors.As(err, &violation) {
		t.Fatalf("ClassifySynthesisRejection() = %v, want a *ModelBoundViolation naming the Rows-authorship bound", err)
	}
	if violation.Bound != boundClaimedFactRowsModelAuthored {
		t.Fatalf("violation.Bound = %q, want %q", violation.Bound, boundClaimedFactRowsModelAuthored)
	}
	if violation.ClaimIndex != 0 {
		t.Fatalf("violation.ClaimIndex = %d, want 0 (the only claim in this fixture)", violation.ClaimIndex)
	}
}

// TestClaimIndexForBoundReturnsMinusOneForNonClaimScopedBounds proves
// claimIndexForBound's own negative case: a bound that isn't claim-scoped
// (or none at all) must never report a fabricated index.
func TestClaimIndexForBoundReturnsMinusOneForNonClaimScopedBounds(t *testing.T) {
	t.Parallel()
	_, draft := closureFixture()
	if got := claimIndexForBound(draft, ""); got != -1 {
		t.Fatalf("claimIndexForBound(_, \"\") = %d, want -1", got)
	}
	if got := claimIndexForBound(draft, "synthesis.driver.title.max_length"); got != -1 {
		t.Fatalf("claimIndexForBound(_, driver bound) = %d, want -1 (not claim-scoped)", got)
	}
}
