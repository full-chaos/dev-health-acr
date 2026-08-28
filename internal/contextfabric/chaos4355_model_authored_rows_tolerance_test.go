package contextfabric

import (
	"context"
	"errors"
	"testing"

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

// TestStripModelAuthoredClaimedFactRows is the strip helper's own direct
// unit test: it must clear Rows, report the exact count, and leave every
// claim that never carried Rows byte-identical.
func TestStripModelAuthoredClaimedFactRows(t *testing.T) {
	t.Parallel()
	value := "fabricated"
	claims := []ClaimedFact{
		{ClaimID: "claim_clean", Kind: FactReadiness, Field: "release_ready", Value: boolScalar(true)},
		{ClaimID: "claim_dirty", Kind: FactReadiness, Field: "release_ready", Value: boolScalar(false),
			Rows: []ClaimedFactRow{{Fields: map[string]ScalarValue{"anything": {String: &value}}}}},
	}
	cleaned, stripped := StripModelAuthoredClaimedFactRows(claims)
	if stripped != 1 {
		t.Fatalf("stripped = %d, want 1", stripped)
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
