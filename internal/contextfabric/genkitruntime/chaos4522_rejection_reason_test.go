package genkitruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

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

// TestDecisionLineCarriesTheFactGroupAmbiguity pins the second new field on
// the same line. fact_group_max is what separates "claim_field_unobserved
// at 17" (a multi-fact grounding problem) from "at 1" (the model claiming a
// field that does not exist) -- indistinguishable before CHAOS-4522.
func TestDecisionLineCarriesTheFactGroupAmbiguity(t *testing.T) {
	t.Parallel()
	input := validSynthesisInput()
	subject := input.Graph.Resolution.Committed[0]
	// Three facts under ONE (kind, subject), none carrying the claimed
	// field -- so the rejection is genuine AND the group is 3 deep.
	for i := 0; i < 2; i++ {
		extra := input.Facts.Facts[0]
		extra.Fields = map[string]contextfabric.FactValue{"backlog_size": contextfabric.IntegerFactValue(int64(i))}
		input.Facts.Facts = append(input.Facts.Facts, extra)
	}
	output := validSynthesisOutput()
	output.ClaimedFacts = []contextfabric.ClaimedFact{{
		ClaimID: "claim_readiness_group_1", Kind: contextfabric.FactReadiness, Subject: subject,
		Field: "field_no_fact_in_the_group_carries", Value: contextfabric.ScalarValue{Boolean: new(bool)},
	}}
	output.Drivers = nil

	fields := captureDecisionLine(t, output, input)
	if got := fields["rejection_reason"]; got != string(contextfabric.RejectionReasonClaimFieldUnobserved) {
		t.Fatalf("rejection_reason = %v, want %q", got, contextfabric.RejectionReasonClaimFieldUnobserved)
	}
	if got, ok := fields["fact_group_max"].(float64); !ok || int(got) != 3 {
		t.Fatalf("fact_group_max = %v, want 3 (the claim's (kind, subject) group depth)", fields["fact_group_max"])
	}
}

// TestSuccessfulDecisionLineCarriesNeitherNewField: a success must stay
// byte-identical in shape to its pre-CHAOS-4522 line. A telemetry field
// that appears on every call, carrying "unclassified" when nothing was
// rejected, would drown the signal it exists to carry.
func TestSuccessfulDecisionLineCarriesNeitherNewField(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	runtime := mustRuntime(t, &generatorStub{synthesis: validSynthesisOutput()}, Config{Logger: logger})
	if _, _, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput()); err != nil {
		t.Fatalf("SynthesizeAnswer() error = %v, want success", err)
	}
	fields := map[string]any{}
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		candidate := map[string]any{}
		if json.Unmarshal(line, &candidate) == nil && candidate["operation"] == string(contextfabric.ModelOperationSynthesize) {
			fields = candidate
		}
	}
	if _, present := fields["rejection_reason"]; present {
		t.Fatalf("a successful synthesize line carries rejection_reason = %v, want the field absent", fields["rejection_reason"])
	}
	if _, present := fields["fact_group_max"]; present {
		t.Fatalf("a successful synthesize line carries fact_group_max = %v, want the field absent", fields["fact_group_max"])
	}
}
