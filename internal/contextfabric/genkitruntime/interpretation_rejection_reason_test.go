package genkitruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// This file is the interpret-side counterpart of
// chaos4522_rejection_reason_test.go. Read that file first: it establishes
// the shape of this proof for the synthesis side, and every rule it states
// -- assert the field where an operator actually reads it (the emitted
// log), and attach the reason AT the rejecting statement rather than
// re-deriving it from the shape of the result -- applies identically here.
//
// RED ON origin/main BY CONSTRUCTION: on the parent commit neither
// ModelExecutionReceipt.InterpretationRejectionReason nor the decision
// line's rejection_reason field exists, so this file does not compile
// there. See the lane's red-on-parent evidence file for the executed run.

// interpretDecisionLine runs InterpretQuestion against an output that will
// be rejected and returns BOTH the emitted decision-event line and the
// receipt, so the two can be asserted to agree. A telemetry field and a
// durable receipt field that disagree about why the same call was rejected
// would be worse than either alone.
func interpretDecisionLine(t *testing.T, output interpretationOutput) (map[string]any, contextfabric.ModelExecutionReceipt) {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	runtime := mustRuntime(t, &generatorStub{interpretation: output}, Config{Logger: logger})
	_, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
	if err == nil {
		t.Fatal("InterpretQuestion() = nil error, want a rejection")
	}
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		fields := map[string]any{}
		if json.Unmarshal(line, &fields) != nil {
			continue
		}
		if fields["operation"] == string(contextfabric.ModelOperationInterpret) {
			return fields, receipt
		}
	}
	t.Fatalf("no interpret decision line was emitted; log = %s", buf.String())
	return nil, contextfabric.ModelExecutionReceipt{}
}

// TestInterpretDecisionLineNamesTheRuleThatRejectedTheInterpretation is the
// primary guard. Each case is a DIFFERENT clause of
// ContextFabricInterpretedQuestion.validate(), and each must be named as
// itself -- not collapsed into one another and not left unnamed.
func TestInterpretDecisionLineNamesTheRuleThatRejectedTheInterpretation(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name   string
		mutate func(*interpretationOutput)
		want   contractsv1.ContextFabricInterpretationRejectionReason
		clause string
	}{
		{
			name:   "an out-of-vocabulary shape",
			mutate: func(o *interpretationOutput) { o.Shape = "not_a_real_shape" },
			want:   contractsv1.ContextFabricInterpretationRejectionShapeInvalid,
			clause: "statement 1, first clause",
		},
		{
			name:   "an empty requested judgment",
			mutate: func(o *interpretationOutput) { o.RequestedJudgment = "" },
			want:   contractsv1.ContextFabricInterpretationRejectionRequestedJudgmentInvalid,
			clause: "statement 1, second clause",
		},
		{
			name:   "an out-of-vocabulary fact requirement kind",
			mutate: func(o *interpretationOutput) { o.FactRequirements = []factRequirementOutput{{Kind: "not_a_fact_kind"}} },
			want:   contractsv1.ContextFabricInterpretationRejectionFactRequirementKindInvalid,
			clause: "statement 3, the fact_requirements loop",
		},
		{
			// The case that justifies this whole vocabulary. This is a
			// BUSINESS RULE, and DiagnoseContextFabricInterpretedQuestionBound
			// names nothing for it by deliberate CHAOS-3784 round-4 design
			// (no registered model-facing bound is involved). Before this
			// ticket it was therefore recorded as outcome=invalid_output
			// and nothing else -- indistinguishable from a model that
			// returned unparseable garbage.
			name: "clarification requested with no reason given",
			mutate: func(o *interpretationOutput) {
				o.ClarificationNeeded = true
				o.ClarificationReason = ""
			},
			want:   contractsv1.ContextFabricInterpretationRejectionClarificationReasonMissing,
			clause: "statement 4, a business rule no bound can name",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			output := validInterpretationOutput()
			testCase.mutate(&output)

			fields, receipt := interpretDecisionLine(t, output)

			if got := fields["rejection_reason"]; got != string(testCase.want) {
				t.Fatalf("rejection_reason = %v, want %q -- an interpretation rejected by %s must name that rule", got, testCase.want, testCase.clause)
			}
			if receipt.InterpretationRejectionReason != testCase.want {
				t.Fatalf("receipt.InterpretationRejectionReason = %q, want %q -- the durable receipt must carry the same reason the log line reports", receipt.InterpretationRejectionReason, testCase.want)
			}
			if got := fields["outcome"]; got != "invalid_output" {
				t.Fatalf("outcome = %v, want \"invalid_output\" -- naming the rule must not change the outcome classification any existing consumer reads", got)
			}
		})
	}
}

// TestInterpretDecisionLineOmitsRejectionReasonOnSuccess pins the
// append-only-when-non-empty behaviour. An unconditional field would put
// rejection_reason="" on every successful interpretation, which reads as
// "rejected for no reason" in aggregation and makes "count rejections by
// rule" impossible to write without also filtering the empty case. The
// synthesis side made the same choice; this keeps the two lines
// symmetrical.
func TestInterpretDecisionLineOmitsRejectionReasonOnSuccess(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	runtime := mustRuntime(t, &generatorStub{interpretation: validInterpretationOutput()}, Config{Logger: logger})
	_, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
	if err != nil {
		t.Fatalf("InterpretQuestion() error = %v", err)
	}
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		fields := map[string]any{}
		if json.Unmarshal(line, &fields) != nil {
			continue
		}
		if fields["operation"] != string(contextfabric.ModelOperationInterpret) {
			continue
		}
		if _, present := fields["rejection_reason"]; present {
			t.Fatalf("rejection_reason is present on a SUCCESSFUL interpretation (= %v); it must be appended only on a rejection", fields["rejection_reason"])
		}
	}
	if receipt.InterpretationRejectionReason != "" {
		t.Fatalf("receipt.InterpretationRejectionReason = %q on a successful interpretation, want empty -- omitempty must keep it absent, not present-and-empty", receipt.InterpretationRejectionReason)
	}
}

// TestInterpretRejectionReasonIsNeverModelAuthoredText is the corpus-safety
// assertion for the new field specifically. TestDecisionEventNeverCarriesCorpusText
// covers the line as a whole for a SUCCESSFUL call; this covers the
// rejection path, where the model's own (invalid) output is in scope at the
// moment the field is chosen and is therefore exactly where a leak would
// happen.
func TestInterpretRejectionReasonIsNeverModelAuthoredText(t *testing.T) {
	t.Parallel()
	const marker = "MARKER_SHAPE_4c81de"
	output := validInterpretationOutput()
	output.Shape = marker
	output.RequestedJudgment = marker
	output.SubjectTerms = []string{marker}

	fields, receipt := interpretDecisionLine(t, output)

	reason, _ := fields["rejection_reason"].(string)
	if !contractsv1.ValidContextFabricInterpretationRejectionReason(contractsv1.ContextFabricInterpretationRejectionReason(reason)) {
		t.Fatalf("rejection_reason = %q, which is not a member of the closed vocabulary -- the logged value must be a table constant, never a value derived from model output", reason)
	}
	for field, value := range fields {
		text, isText := value.(string)
		if isText && bytes.Contains([]byte(text), []byte(marker)) {
			t.Fatalf("decision field %q leaked model-authored text %q", field, text)
		}
	}
	if bytes.Contains([]byte(receipt.InterpretationRejectionReason), []byte(marker)) {
		t.Fatalf("receipt.InterpretationRejectionReason leaked model-authored text: %q", receipt.InterpretationRejectionReason)
	}
}

// TestFallbackSemanticRejectionCarriesItsReasonToTheOuterArtifacts closes a
// gap an adversarial review round found: when the primary's interpretation
// is rejected AND a configured fallback also fails semantically, the
// FALLBACK's error is what the caller receives — so the fallback's reason is
// the one the receipt and the decision line must report.
//
// Before this, the returned error was still correctly classifiable but both
// outer artifacts were silent. That is precisely the artifact/error
// disagreement this whole ticket exists to remove: an operator reading the
// telemetry would see a rejection with no rule while the error object in
// flight knew exactly which rule it was.
func TestFallbackSemanticRejectionCarriesItsReasonToTheOuterArtifacts(t *testing.T) {
	t.Parallel()
	// The fallback rejects for a rule of its own, distinct from anything
	// the primary's output would produce, so the assertion cannot pass by
	// accidentally reading the primary's reason.
	fallbackRejected := contextfabric.NewInterpretationRejection(
		contractsv1.ContextFabricInterpretationRejectionShapeInvalid,
		fmt.Errorf("%w: %w: shape is invalid", contextfabric.ErrInterpretationRejected, contextfabric.ErrModelOutput),
	)

	primary := validInterpretationOutput()
	primary.Shape = "not_a_real_shape"

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	runtime := mustRuntime(t, &generatorStub{interpretation: primary}, Config{
		Logger:   logger,
		Fallback: erroringFallbackRuntime{err: fallbackRejected},
	})

	_, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
	if err == nil {
		t.Fatal("InterpretQuestion() = nil error, want the fallback's rejection")
	}
	want := contractsv1.ContextFabricInterpretationRejectionShapeInvalid
	if got := contextfabric.InterpretationRejectionReasonOf(err); got != want {
		t.Fatalf("the RETURNED error's reason = %q, want %q -- fixture is wrong", got, want)
	}
	if receipt.InterpretationRejectionReason != want {
		t.Fatalf("receipt.InterpretationRejectionReason = %q, want %q -- the receipt must not stay silent while the error it accompanies names the rule", receipt.InterpretationRejectionReason, want)
	}
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		fields := map[string]any{}
		if json.Unmarshal(line, &fields) != nil {
			continue
		}
		if fields["operation"] != string(contextfabric.ModelOperationInterpret) {
			continue
		}
		if got := fields["rejection_reason"]; got != string(want) {
			t.Fatalf("decision line rejection_reason = %v, want %q", got, want)
		}
		return
	}
	t.Fatal("no interpret decision line was emitted")
}
