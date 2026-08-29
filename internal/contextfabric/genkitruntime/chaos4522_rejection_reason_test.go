package genkitruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// decisionLine extracts the synthesize decision-event line from a captured
// JSON log.
func decisionLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	fields := map[string]any{}
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		candidate := map[string]any{}
		if json.Unmarshal(line, &candidate) == nil && candidate["operation"] == string(contextfabric.ModelOperationSynthesize) {
			fields = candidate
		}
	}
	if len(fields) == 0 {
		t.Fatalf("no synthesize decision line was emitted; log = %s", buf.String())
	}
	return fields
}

// captureDecisionLine runs SynthesizeAnswer with output and returns the
// decision-event line's fields, so the two new CHAOS-4522 fields are
// asserted where an operator actually reads them -- the emitted log -- not
// merely on an internal value.
func captureDecisionLine(t *testing.T, output synthesisOutput, input contextfabric.SynthesisInput) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	runtime := mustRuntime(t, &generatorStub{synthesis: output}, Config{Logger: logger})
	_, _, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, input)
	if err == nil {
		t.Fatal("SynthesizeAnswer() = nil error, want a rejection")
	}
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		fields := map[string]any{}
		if json.Unmarshal(line, &fields) != nil {
			continue
		}
		if fields["operation"] == string(contextfabric.ModelOperationSynthesize) {
			return fields
		}
	}
	t.Fatalf("no synthesize decision line was emitted; log = %s", buf.String())
	return nil
}

// TestDecisionLineNamesTheRuleThatRejectedADecodedDraft is the guard for
// the CHAOS-4522 finding team-lead raised in review: a rejection reason
// must be attached AT the statement that rejected, never re-derived
// afterwards from the shape of the resulting draft. A draft's shape is a
// CONSEQUENCE of the rejecting branch, not an observation of it, and the
// two are not in bijection -- AGENTS.md verification rule 1, "assert the
// mechanism, not the outcome".
//
// A draft with an empty Status and no claims decodes perfectly well and is
// rejected by ValidateAgainst's FIRST statement, the status check. It must
// name that rule.
func TestDecisionLineNamesTheRuleThatRejectedADecodedDraft(t *testing.T) {
	t.Parallel()
	output := validSynthesisOutput()
	output.Status = ""
	output.Drivers = nil
	output.ClaimedFacts = nil

	fields := captureDecisionLine(t, output, validSynthesisInput())
	if got := fields["rejection_reason"]; got != string(contextfabric.RejectionReasonStatusInvalid) {
		t.Fatalf("rejection_reason = %v, want %q -- a draft that decoded and failed the status rule must name that rule", got, contextfabric.RejectionReasonStatusInvalid)
	}
	if got := fields["outcome"]; got != "invalid_output" {
		t.Fatalf("outcome = %v, want invalid_output", got)
	}
}

// TestToDomainRejectionNamesItsOwnRuleNotADecodeFailure is the RED-FIRST
// half, and it is where investigating team-lead's finding actually led.
//
// synthesisOutput.toDomain runs BEFORE ValidateAgainst and enforces one
// rule of its own: deterministic_answer is required. Two things were wrong
// there. The original implementation guarded its `output_undecodable`
// override on errors.Is(err, ErrModelOutput), which toDomain's plain
// errors.New never satisfies -- so the override was DEAD and this
// rejection reported `unclassified`, the vocabulary's "no entry for this"
// value, for a rule that has an entry. And `output_undecodable` was the
// wrong name for it regardless: the model's JSON decoded into
// synthesisOutput perfectly well, it merely omitted a required field, and
// it is the very same rule ValidateAgainst enforces one statement later.
// Nothing in this path can actually fail to DECODE, so no undecodable
// reason exists in the vocabulary at all -- a reason no branch can emit is
// a claim the telemetry cannot back.
//
// RED at 64d68cce and at e85a4ac8: both report a reason other than
// deterministic_answer_missing for a rejection that IS exactly that rule.
func TestToDomainRejectionNamesItsOwnRuleNotADecodeFailure(t *testing.T) {
	t.Parallel()
	output := validSynthesisOutput()
	output.DeterministicAnswer = "   "

	fields := captureDecisionLine(t, output, validSynthesisInput())
	if got := fields["rejection_reason"]; got != string(contextfabric.RejectionReasonDeterministicAnswerMissing) {
		t.Fatalf("rejection_reason = %v, want %q -- toDomain's required-field rejection is a validation rule with its own vocabulary entry, not a decode failure and not unclassified", got, contextfabric.RejectionReasonDeterministicAnswerMissing)
	}
}

// TestDecisionLineCarriesTheRejectingClaimsFactGroupSize pins the second
// new field on the emitted line, in codex R1 finding 1's adversarial shape:
// the claim that REJECTS sits on a group of size 1 while a later,
// never-evaluated claim sits on a group of size 3. A maximum over the draft
// would log 3 and describe a claim ValidateAgainst never reached.
func TestDecisionLineCarriesTheRejectingClaimsFactGroupSize(t *testing.T) {
	t.Parallel()
	input := validSynthesisInput()
	subject := input.Graph.Resolution.Committed[0]
	input.Facts.Facts = append(input.Facts.Facts, contextfabric.CanonicalFact{
		Kind: contextfabric.FactFlow, Subject: subject,
		Fields:         map[string]contextfabric.FactValue{"items_completed": contextfabric.IntegerFactValue(3)},
		EvidenceRefIDs: []string{"evidence_release_1234"}, SourceState: contextfabric.SourceAvailable,
		Source: "ops", SourceVersion: "v1",
	})
	for i := 0; i < 2; i++ {
		extra := input.Facts.Facts[0]
		extra.Fields = map[string]contextfabric.FactValue{"backlog_size": contextfabric.IntegerFactValue(int64(i))}
		input.Facts.Facts = append(input.Facts.Facts, extra)
	}

	output := validSynthesisOutput()
	output.ClaimedFacts = []contextfabric.ClaimedFact{
		{ClaimID: "claim_flow_rejects_first", Kind: contextfabric.FactFlow, Subject: subject,
			Field: "field_no_flow_fact_carries", Value: contextfabric.ScalarValue{Boolean: new(bool)}},
		{ClaimID: "claim_readiness_never_reached", Kind: contextfabric.FactReadiness, Subject: subject,
			Field: "release_ready", Value: contextfabric.ScalarValue{Boolean: new(bool)}},
	}
	output.Drivers = nil

	fields := captureDecisionLine(t, output, input)
	if got := fields["rejection_reason"]; got != string(contextfabric.RejectionReasonClaimFieldUnobserved) {
		t.Fatalf("rejection_reason = %v, want %q", got, contextfabric.RejectionReasonClaimFieldUnobserved)
	}
	if got, ok := fields["fact_group_size"].(float64); !ok || int(got) != 1 {
		t.Fatalf("fact_group_size = %v, want 1 (the REJECTING claim's group) -- 3 is the later claim that was never evaluated", fields["fact_group_size"])
	}
}

// TestFallbackSuccessLineCarriesNoRejectionDiagnostics is codex R1 finding
// 2: the decision line's outcome and its rejection diagnostics must
// describe the SAME leg. When the primary rejects and a configured fallback
// then SUCCEEDS, the call did not end in a rejection -- the line reports
// outcome=fallback, and carrying the primary's reason beside it would pair
// a success with a rejection that is not this call's result. The primary's
// failure is still reported, by primary_failure_classification, which
// exists for exactly that.
func TestFallbackSuccessLineCarriesNoRejectionDiagnostics(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	rejecting := validSynthesisOutput()
	rejecting.Status = ""
	runtime := mustRuntime(t, &generatorStub{synthesis: rejecting}, Config{
		Logger:   logger,
		Fallback: fallbackRuntime{draft: validDraft()},
	})
	if _, _, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput()); err != nil {
		t.Fatalf("SynthesizeAnswer() error = %v, want the fallback to succeed", err)
	}
	fields := map[string]any{}
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		candidate := map[string]any{}
		if json.Unmarshal(line, &candidate) == nil && candidate["operation"] == string(contextfabric.ModelOperationSynthesize) {
			fields = candidate
		}
	}
	if got := fields["outcome"]; got != "fallback" {
		t.Fatalf("outcome = %v, want fallback", got)
	}
	if _, present := fields["rejection_reason"]; present {
		t.Fatalf("a fallback-SUCCESS line carries rejection_reason = %v, want the field absent -- outcome and diagnostics must describe the same leg", fields["rejection_reason"])
	}
	if got := fields["primary_failure_classification"]; got != "invalid_output" {
		t.Fatalf("primary_failure_classification = %v, want invalid_output (the primary's failure is still reported, just not as this call's rejection)", got)
	}
}

// TestSuccessfulDecisionLineRecordsGroundingAmbiguityWithoutAReason is
// codex R2 finding 4. Grounding a claim against a LATER fact of its
// (Kind, Subject) group is an outcome-changing branch -- it turns what used
// to be a rejection into an answer -- so a SUCCESSFUL run must record that
// it fired. Before this, the closure left a trace only when it FAILED to
// save the answer, which is backwards.
//
// A success carries fact_group_size and NO rejection_reason: nothing
// rejected it, and a reason field on a success would assert otherwise.
func TestSuccessfulDecisionLineRecordsGroundingAmbiguityWithoutAReason(t *testing.T) {
	t.Parallel()
	input := validSynthesisInput()
	// Two facts under one (kind, subject): the FIRST lacks the claimed
	// field, so the claim can only be admitted by the widened closure.
	sparse := input.Facts.Facts[0]
	sparse.Fields = map[string]contextfabric.FactValue{"backlog_size": contextfabric.IntegerFactValue(24)}
	input.Facts.Facts = append([]contextfabric.CanonicalFact{sparse}, input.Facts.Facts...)

	output := validSynthesisOutput()
	output.ClaimedFacts = []contextfabric.ClaimedFact{groundedReadinessClaim(input)}

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	runtime := mustRuntime(t, &generatorStub{synthesis: output}, Config{Logger: logger})
	if _, _, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, input); err != nil {
		t.Fatalf("SynthesizeAnswer() error = %v, want success via the widened grounding closure", err)
	}
	fields := decisionLine(t, &buf)
	if _, present := fields["rejection_reason"]; present {
		t.Fatalf("a successful line carries rejection_reason = %v, want the field absent", fields["rejection_reason"])
	}
	if got, ok := fields["fact_group_size"].(float64); !ok || int(got) != 2 {
		t.Fatalf("fact_group_size = %v, want 2 -- a successful run must record that multi-fact grounding fired", fields["fact_group_size"])
	}
}

// TestSingleFactSuccessCarriesNoAmbiguitySignal: when every claim addressed
// an unambiguous fact the closure changed nothing, and the line must not
// imply it did.
func TestSingleFactSuccessCarriesNoAmbiguitySignal(t *testing.T) {
	t.Parallel()
	input := validSynthesisInput()
	output := validSynthesisOutput()
	output.ClaimedFacts = []contextfabric.ClaimedFact{groundedReadinessClaim(input)}

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	runtime := mustRuntime(t, &generatorStub{synthesis: output}, Config{Logger: logger})
	if _, _, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, input); err != nil {
		t.Fatalf("SynthesizeAnswer() error = %v, want success", err)
	}
	fields := decisionLine(t, &buf)
	if got, ok := fields["fact_group_size"].(float64); !ok || int(got) != 1 {
		t.Fatalf("fact_group_size = %v, want 1 (every claim addressed an unambiguous fact)", fields["fact_group_size"])
	}
	if _, present := fields["rejection_reason"]; present {
		t.Fatalf("a successful line carries rejection_reason = %v, want it absent", fields["rejection_reason"])
	}
}

// TestFallbackTransportFailureCarriesNoRejectionReason is codex R2 finding
// 1: when the primary is synthesis-rejected but the FALLBACK fails in
// transport, the call's final failure is not a rejection at all. Labelling
// it `rejection_reason=unclassified` would assert that a rule rejected a
// draft when no draft was ever judged on that leg; the field must be
// absent.
func TestFallbackTransportFailureCarriesNoRejectionReason(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	primary := validSynthesisOutput()
	primary.Status = ""
	fallbackReceipt := validReceipt(contextfabric.ModelOperationSynthesize)
	fallbackReceipt.Outcome = "unavailable"
	runtime := mustRuntime(t, &generatorStub{synthesis: primary}, Config{
		Logger:   logger,
		Fallback: erroringFallbackRuntime{err: errors.New("upstream transport failure"), receipt: fallbackReceipt},
	})
	if _, _, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput()); err == nil {
		t.Fatal("SynthesizeAnswer() = nil error, want both legs to fail")
	}
	fields := decisionLine(t, &buf)
	if got, present := fields["rejection_reason"]; present {
		t.Fatalf("rejection_reason = %v on a transport failure, want the field absent -- no draft was judged on the final leg", got)
	}
}

// TestBothLegsFailedLineDescribesTheFallbackLeg is codex R1 finding 2's
// other half: when the primary rejects AND the configured fallback also
// fails, the receipt reports the FALLBACK's outcome and the caller receives
// the FALLBACK's error -- so the reason on the same line must describe that
// leg too, never the primary's stale one.
func TestBothLegsFailedLineDescribesTheFallbackLeg(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Primary rejects on the STATUS rule; the fallback fails carrying a
	// DIFFERENT, deterministic-answer rejection. The two are distinguishable
	// on the wire, which is the whole point.
	primary := validSynthesisOutput()
	primary.Status = ""
	fallbackReceipt := validReceipt(contextfabric.ModelOperationSynthesize)
	fallbackReceipt.Outcome = "invalid_output"
	fallbackErr := contextfabric.NewSynthesisRejection(
		contextfabric.RejectionReasonDeterministicAnswerMissing,
		errors.New("deterministic answer is required"),
	)
	runtime := mustRuntime(t, &generatorStub{synthesis: primary}, Config{
		Logger:   logger,
		Fallback: erroringFallbackRuntime{err: fallbackErr, receipt: fallbackReceipt},
	})
	if _, _, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput()); err == nil {
		t.Fatal("SynthesizeAnswer() = nil error, want both legs to fail")
	}
	fields := map[string]any{}
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		candidate := map[string]any{}
		if json.Unmarshal(line, &candidate) == nil && candidate["operation"] == string(contextfabric.ModelOperationSynthesize) {
			fields = candidate
		}
	}
	if got := fields["rejection_reason"]; got != string(contextfabric.RejectionReasonDeterministicAnswerMissing) {
		t.Fatalf("rejection_reason = %v, want the FALLBACK leg's %q -- the receipt outcome and the caller's error both come from that leg", got, contextfabric.RejectionReasonDeterministicAnswerMissing)
	}
}

// groundedReadinessClaim is a claim that validSynthesisInput's canonical
// readiness fact actually grounds -- validSynthesisOutput carries no claims
// of its own, so the group-size tests must supply one or there is no
// grounding decision to report.
func groundedReadinessClaim(input contextfabric.SynthesisInput) contextfabric.ClaimedFact {
	releaseReady := false
	return contextfabric.ClaimedFact{
		ClaimID: "claim_readiness_grounded",
		Kind:    contextfabric.FactReadiness,
		Subject: input.Graph.Resolution.Committed[0],
		Field:   "release_ready",
		Value:   contextfabric.ScalarValue{Boolean: &releaseReady},
	}
}
