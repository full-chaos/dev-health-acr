package contextfabric_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	"github.com/full-chaos/dev-health-go/llm"
)

type wrapperScriptLLM struct {
	model string
	reply string
	calls atomic.Int32
}

func (s *wrapperScriptLLM) Complete(context.Context, llm.Request) (llm.Result, error) {
	s.calls.Add(1)
	in, out := 10, 4
	return llm.Result{Text: s.reply, Model: s.model, Usage: llm.Usage{InputTokens: &in, OutputTokens: &out}}, nil
}
func (s *wrapperScriptLLM) Model() string  { return s.model }
func (s *wrapperScriptLLM) Kind() llm.Kind { return llm.KindLocal }
func (s *wrapperScriptLLM) Close() error   { return nil }

func bootstrapDraftFields(t *testing.T) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(contextfabric.BootstrapDraftForTest())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	return fields
}

func investigateWithModelReply(t *testing.T, reply map[string]any) (contextfabric.InvestigationResult, int32) {
	t.Helper()
	encoded, err := json.Marshal(reply)
	if err != nil {
		t.Fatal(err)
	}
	name := "wrapper-" + strings.ReplaceAll(t.Name(), "/", "-")
	client := &wrapperScriptLLM{model: name, reply: string(encoded)}
	runtime, err := genkitruntime.NewWithLLM(genkitruntime.Config{
		Provider: "test", Model: name, ModelVersion: "test-v1",
		Timeout: 1e9, MaxAttempts: 1, MaxInputBytes: 128 << 10, MaxSynthesisResynthesisAttempts: 3,
	}, genkitruntime.LLMClients{name: client})
	if err != nil {
		t.Fatalf("NewWithLLM() error = %v", err)
	}
	result, err := contextfabric.InvestigateAcceptanceBootstrapForTest(t, runtime)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	return result, client.calls.Load()
}

func TestInvestigateServesADraftWrappedInAToolInputObject(t *testing.T) {
	t.Parallel()
	control, _ := investigateWithModelReply(t, bootstrapDraftFields(t))
	if control.Status != contextfabric.InvestigationComplete || contextfabric.IsSynthesisModelFailureAnswer(control) {
		t.Fatalf("control status = %q warnings = %v, want the synthesized complete answer", control.Status, control.Warnings)
	}
	partial := bootstrapDraftFields(t)
	inner := map[string]any{}
	for _, key := range []string{"direct_judgment", "current_state", "claimed_facts", "deterministic_answer", "warnings"} {
		inner[key] = partial[key]
		delete(partial, key)
	}
	partial["input"] = inner
	for name, reply := range map[string]map[string]any{
		"whole draft under input":       {"input": bootstrapDraftFields(t)},
		"part of the draft under input": partial,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result, calls := investigateWithModelReply(t, reply)
			if calls != 1 {
				t.Fatalf("synthesis calls = %d, want 1", calls)
			}
			if result.Status != control.Status || contextfabric.IsSynthesisModelFailureAnswer(result) {
				t.Fatalf("status = %q warnings = %v, want the synthesized %q answer", result.Status, result.Warnings, control.Status)
			}
			if result.DeterministicAnswer != control.DeterministicAnswer || result.DirectJudgment != control.DirectJudgment || result.CurrentState != control.CurrentState {
				t.Fatalf("served prose differs from the unwrapped draft's answer")
			}
			got, _ := json.Marshal([]any{result.ClaimedFacts, result.Drivers, result.EvidenceRefIDs})
			want, _ := json.Marshal([]any{control.ClaimedFacts, control.Drivers, control.EvidenceRefIDs})
			if string(got) != string(want) {
				t.Fatalf("served claims/drivers/evidence = %s, want the unwrapped draft's %s", got, want)
			}
		})
	}
}

func TestInvestigateStillRefusesAContradictingClaimInsideAToolInputObject(t *testing.T) {
	t.Parallel()
	draft := bootstrapDraftFields(t)
	draft["claimed_facts"].([]any)[0].(map[string]any)["value"] = map[string]any{"boolean": true}
	result, calls := investigateWithModelReply(t, map[string]any{"input": draft})
	if calls != 3 {
		t.Fatalf("synthesis calls = %d, want 3 rejected draws", calls)
	}
	if !contextfabric.IsSynthesisModelFailureAnswer(result) || result.Status != contextfabric.InvestigationDegraded {
		t.Fatalf("status = %q warnings = %v, want the degraded synthesis-rejected answer", result.Status, result.Warnings)
	}
	for _, claim := range result.ClaimedFacts {
		if !strings.HasPrefix(claim.ClaimID, "server:fact:") {
			t.Fatalf("claim %q is not server-minted: the refused draft's claim was served", claim.ClaimID)
		}
	}
}
