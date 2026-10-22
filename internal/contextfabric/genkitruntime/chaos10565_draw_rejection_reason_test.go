package genkitruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A draw the output-schema check refuses before this package's own rules run
// (the recorded BYO haiku pass 1: a draft that parses, with claims, that the
// declared schema still refuses) used to be logged as
// draw_rejected_reasons=1:unclassified, naming no rule. The rule that fired is
// the output-schema check.
func TestSchemaRefusedSynthesisDrawLogsTheRuleThatRefusedIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind invalidSynthesisDrawKind
		want string
	}{
		{"unknown_property_on_a_parsed_draft", drawSynthesisUnknownProperty, "1:output_schema_mismatch,2:none"},
		{"malformed_json", drawSynthesisMalformedJSON, "1:output_schema_mismatch,2:none"},
		// A draft this package's own rules also refuse keeps the more specific rule.
		{"out_of_enum_status", drawSynthesisOutOfEnumStatus, "1:status_invalid,2:none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, handler, _ := scriptedSynthesisGenkitRuntime(t, []string{invalidSynthesisDrawText(t, tc.kind), validSynthesisDrawText(t)})
			if _, _, err := rt.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput()); err != nil {
				t.Fatalf("SynthesizeAnswer() error = %v, want the redraw to serve", err)
			}
			events := handler.decisionEvents()
			if len(events) != 1 {
				t.Fatalf("decision events = %d, want 1", len(events))
			}
			if got := attrString(t, events[0].Attrs, "draw_rejected_reasons"); got != tc.want {
				t.Fatalf("draw_rejected_reasons = %q, want %q", got, tc.want)
			}
		})
	}
}

// Pass 1 refused by the schema check, pass 2 by a validator rule, pass 3 by the
// out-of-enum status (a rule of this package too): each draw names its own rule.
func TestEachRejectedSynthesisDrawNamesItsOwnRule(t *testing.T) {
	ungrounded := validSynthesisOutput()
	claim := groundedReadinessClaim(validSynthesisInput())
	claim.Field = "field_no_fact_carries"
	ungrounded.ClaimedFacts = []contextfabric.ClaimedFact{claim}
	ungrounded.Drivers = nil
	encoded, err := json.Marshal(ungrounded)
	if err != nil {
		t.Fatal(err)
	}
	rt, handler, _ := scriptedSynthesisGenkitRuntime(t, []string{
		invalidSynthesisDrawText(t, drawSynthesisUnknownProperty),
		string(encoded),
		invalidSynthesisDrawText(t, drawSynthesisOutOfEnumStatus),
	})
	if _, _, err := rt.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput()); err == nil {
		t.Fatal("three rejected draws were served")
	}
	events := handler.decisionEvents()
	if len(events) != 1 {
		t.Fatalf("decision events = %d, want 1", len(events))
	}
	want := fmt.Sprintf("1:%s,2:%s,3:%s", contextfabric.RejectionReasonOutputSchemaMismatch, contextfabric.RejectionReasonClaimFieldUnobserved, contextfabric.RejectionReasonStatusInvalid)
	if got := attrString(t, events[0].Attrs, "draw_rejected_reasons"); got != want {
		t.Fatalf("draw_rejected_reasons = %q, want %q", got, want)
	}
}
