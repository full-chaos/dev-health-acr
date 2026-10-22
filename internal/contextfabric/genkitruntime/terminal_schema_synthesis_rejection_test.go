package genkitruntime

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// When every draw is refused by the output-schema check alone, the call ends
// in a typed synthesis rejection whose reason names that check, on the error
// and on the decision line, the same word the last draw carries in
// draw_rejected_reasons. A draft that parses (an unknown property) and a draft
// that never decodes (malformed JSON) are both the schema check's.
func TestTerminalSchemaRefusedSynthesisRejectionNamesTheSchemaCheck(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind invalidSynthesisDrawKind
	}{
		{"unknown_property_on_a_parsed_draft", drawSynthesisUnknownProperty},
		{"malformed_json", drawSynthesisMalformedJSON},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, handler, calls := scriptedSynthesisGenkitRuntime(t, []string{invalidSynthesisDrawText(t, tc.kind)})
			_, receipt, err := rt.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
			if !errors.Is(err, contextfabric.ErrSynthesisRejected) || !errors.Is(err, contextfabric.ErrModelOutput) {
				t.Fatalf("err = %v, want the typed synthesis rejection", err)
			}
			if calls.Load() != invalidSynthesisDrawCeiling || receipt.Outcome != "invalid_output" {
				t.Fatalf("calls = %d outcome = %q, want %d draws and invalid_output", calls.Load(), receipt.Outcome, invalidSynthesisDrawCeiling)
			}
			if got := contextfabric.SynthesisRejectionReasonOf(err); got != contextfabric.RejectionReasonOutputSchemaMismatch {
				t.Fatalf("terminal reason = %q, want %q", got, contextfabric.RejectionReasonOutputSchemaMismatch)
			}
			events := handler.decisionEvents()
			if len(events) != 1 {
				t.Fatalf("decision events = %d, want 1", len(events))
			}
			if got := attrString(t, events[0].Attrs, "rejection_reason"); got != string(contextfabric.RejectionReasonOutputSchemaMismatch) {
				t.Fatalf("decision line rejection_reason = %q, want %q", got, contextfabric.RejectionReasonOutputSchemaMismatch)
			}
			want := "1:output_schema_mismatch,2:output_schema_mismatch,3:output_schema_mismatch"
			if got := attrString(t, events[0].Attrs, "draw_rejected_reasons"); got != want {
				t.Fatalf("draw_rejected_reasons = %q, want %q", got, want)
			}
		})
	}
}

// A terminal draw this package's own rule also refuses keeps that rule, even
// after schema-only draws: the schema word never replaces a more specific one.
func TestTerminalSynthesisRejectionKeepsAValidatorRuleOverTheSchemaWord(t *testing.T) {
	rt, handler, _ := scriptedSynthesisGenkitRuntime(t, []string{
		invalidSynthesisDrawText(t, drawSynthesisUnknownProperty),
		invalidSynthesisDrawText(t, drawSynthesisUnknownProperty),
		invalidSynthesisDrawText(t, drawSynthesisOutOfEnumStatus),
	})
	_, _, err := rt.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
	if got := contextfabric.SynthesisRejectionReasonOf(err); got != contextfabric.RejectionReasonStatusInvalid {
		t.Fatalf("terminal reason = %q (err %v), want %q", got, err, contextfabric.RejectionReasonStatusInvalid)
	}
	events := handler.decisionEvents()
	if len(events) != 1 || attrString(t, events[0].Attrs, "rejection_reason") != string(contextfabric.RejectionReasonStatusInvalid) {
		t.Fatalf("decision events = %v, want one line naming %q", events, contextfabric.RejectionReasonStatusInvalid)
	}
}
