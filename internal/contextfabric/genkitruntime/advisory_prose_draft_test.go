package genkitruntime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// advisoryProseDrawText is a grounded draft whose advisory prose fields are
// rewritten by mutate. The server recomposes those fields and never serves the
// model's text for them.
func advisoryProseDrawText(t *testing.T, mutate func(map[string]any)) string {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal([]byte(validSynthesisDrawText(t)), &fields); err != nil {
		t.Fatal(err)
	}
	mutate(fields)
	encoded, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestEmptyAdvisoryProseDoesNotRejectAGroundedDraft(t *testing.T) {
	modelProse := validSynthesisOutput()
	cases := map[string]func(map[string]any){
		"empty deterministic answer":      func(f map[string]any) { f["deterministic_answer"] = "" },
		"blank deterministic answer":      func(f map[string]any) { f["deterministic_answer"] = "  \n" },
		"empty direct judgment":           func(f map[string]any) { f["direct_judgment"] = "" },
		"empty direct judgment (partial)": func(f map[string]any) { f["direct_judgment"] = " "; f["status"] = "partial" },
		"all advisory prose empty": func(f map[string]any) {
			f["deterministic_answer"], f["direct_judgment"], f["current_state"] = "", "", ""
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			rt, _, calls := scriptedSynthesisGenkitRuntime(t, []string{advisoryProseDrawText(t, mutate)})
			result, err := contextfabric.RuntimeAnswerSynthesizer{Runtime: rt}.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
			if err != nil {
				t.Fatalf("Synthesize() error = %v (reason %q), want the grounded draft served", err, contextfabric.SynthesisRejectionReasonOf(err))
			}
			if calls.Load() != 1 {
				t.Fatalf("calls = %d, want 1: the first draw is valid", calls.Load())
			}
			if contextfabric.IsSynthesisModelFailureAnswer(result) {
				t.Fatalf("result is the degraded model-failure answer, want the synthesized one")
			}
			if strings.TrimSpace(result.DeterministicAnswer) == "" || strings.TrimSpace(result.DirectJudgment) == "" {
				t.Fatalf("served deterministic answer %q / direct judgment %q, want the server-composed text", result.DeterministicAnswer, result.DirectJudgment)
			}
			for _, served := range []string{result.DeterministicAnswer, result.DirectJudgment, result.CurrentState} {
				for _, prose := range []string{modelProse.DeterministicAnswer, modelProse.DirectJudgment, modelProse.CurrentState} {
					if strings.Contains(served, prose) {
						t.Fatalf("served text %q carries model prose %q", served, prose)
					}
				}
			}
			if len(result.ClaimedFacts) != 1 || result.ClaimedFacts[0].ClaimID != groundedReadinessClaim(validSynthesisInput()).ClaimID {
				t.Fatalf("claimed facts = %+v, want the one grounded claim", result.ClaimedFacts)
			}
		})
	}
}

func TestEmptyAdvisoryProseDoesNotHideAnUngroundedClaim(t *testing.T) {
	rt, _, calls := scriptedSynthesisGenkitRuntime(t, []string{advisoryProseDrawText(t, func(f map[string]any) {
		f["deterministic_answer"], f["direct_judgment"] = "", ""
		f["drivers"] = []any{}
		f["claimed_facts"].([]any)[0].(map[string]any)["field"] = "field_no_fact_carries"
	})})
	_, _, err := rt.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
	if got := contextfabric.SynthesisRejectionReasonOf(err); got != contextfabric.RejectionReasonClaimFieldUnobserved {
		t.Fatalf("reason = %q (err %v), want the claim rule to reject the draft", got, err)
	}
	if calls.Load() != invalidSynthesisDrawCeiling {
		t.Fatalf("calls = %d, want %d", calls.Load(), invalidSynthesisDrawCeiling)
	}
}

func TestAbsentAdvisoryProseKeyIsStillASchemaRejection(t *testing.T) {
	for _, key := range []string{"deterministic_answer", "direct_judgment"} {
		t.Run(key, func(t *testing.T) {
			rt, _, calls := scriptedSynthesisGenkitRuntime(t, []string{advisoryProseDrawText(t, func(f map[string]any) { delete(f, key) })})
			_, _, err := rt.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
			if got := contextfabric.SynthesisRejectionReasonOf(err); got != contextfabric.RejectionReasonOutputSchemaMismatch {
				t.Fatalf("reason = %q (err %v), want the output schema to refuse a draft without the required key", got, err)
			}
			if calls.Load() != invalidSynthesisDrawCeiling {
				t.Fatalf("calls = %d, want %d", calls.Load(), invalidSynthesisDrawCeiling)
			}
		})
	}
}

func TestSuppliedSynthesizerAcceptsEmptyAdvisoryProse(t *testing.T) {
	t.Parallel()
	synthesizer := newTestSuppliedSynthesizer(t, SuppliedSynthesizerConfig{})
	document := suppliedSynthesisFixture(t)
	document["deterministic_answer"], document["direct_judgment"] = "   ", ""
	draft, err := synthesizer.Parse(marshalFixture(t, document))
	if err != nil {
		t.Fatalf("Parse() error = %v (reason %q), want the draft decoded", err, contextfabric.SynthesisRejectionReasonOf(err))
	}
	if draft.DeterministicAnswer != "" || draft.DirectJudgment != "" {
		t.Fatalf("draft prose = %q / %q, want both empty after trimming", draft.DeterministicAnswer, draft.DirectJudgment)
	}
}
