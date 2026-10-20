package genkitruntime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/llm"
)

func refusalFallbackSynthesis() synthesisOutput {
	output := validSynthesisOutput()
	output.ClaimedFacts = []contextfabric.ClaimedFact{}
	return output
}

// A refused synthesis does not run the configured fallback runtime: the
// fallback client sees no call, and the primary is called once.
func TestRefusedSynthesisSkipsTheFallbackLeg(t *testing.T) {
	fallbackClient := &scriptedLLM{model: refusalModel, replies: []llmReply{{result: llm.Result{Text: jsonOf(t, refusalFallbackSynthesis()), StopReason: "stop"}}}}
	fallback := refusalRuntime(t, fallbackClient, nil)
	primary := &scriptedLLM{model: refusalModel, replies: []llmReply{refusalReply()}}
	runtime := refusalRuntime(t, primary, fallback)
	_, receipt, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
	if !errors.Is(err, contextfabric.ErrModelOutput) {
		t.Fatalf("err = %v, want ErrModelOutput", err)
	}
	if len(primary.requests) != 1 || len(fallbackClient.requests) != 0 {
		t.Fatalf("primary calls = %d fallback calls = %d, want 1 and 0", len(primary.requests), len(fallbackClient.requests))
	}
	if receipt.FallbackUsed || receipt.Usage.InputTokens != 900 {
		t.Fatalf("fallback used = %v input tokens = %d, want false and 900", receipt.FallbackUsed, receipt.Usage.InputTokens)
	}
}

// Control: a primary synthesis failure that is not a refusal still reaches
// the fallback, so the skip above is the refusal's alone.
func TestNonRefusalSynthesisFailureStillReachesTheFallbackLeg(t *testing.T) {
	fallbackClient := &scriptedLLM{model: refusalModel, replies: []llmReply{{result: llm.Result{Text: jsonOf(t, refusalFallbackSynthesis()), StopReason: "stop"}}}}
	fallback := refusalRuntime(t, fallbackClient, nil)
	primary := &scriptedLLM{model: refusalModel, replies: []llmReply{{err: &llm.Error{Class: llm.ClassAuth}}}}
	runtime := refusalRuntime(t, primary, fallback)
	if _, _, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput()); err != nil {
		t.Fatalf("err = %v, want the fallback's answer", err)
	}
	if len(fallbackClient.requests) == 0 {
		t.Fatal("fallback calls = 0, want at least 1")
	}
}

// A refused offer-phrasing call, through the runtime: one provider call, the
// refusal named in the error, the billed input tokens on the receipt, and the
// phraser serves the structural offers (no phrasings, call_failed).
func TestRefusedPhrasingIsOneCallWithBilledReceiptAndDegradedOffers(t *testing.T) {
	principal := storage.Principal{OrgID: "org_1"}
	client := &scriptedLLM{model: refusalModel, replies: []llmReply{refusalReply()}}
	runtime := refusalRuntime(t, client, nil)

	_, receipt, err := runtime.PhraseStructureOffers(context.Background(), principal, validPhrasingInput())
	if !errors.Is(err, contextfabric.ErrModelOutput) || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("err = %v, want ErrModelOutput naming the refusal", err)
	}
	if len(client.requests) != 1 || receipt.Attempts != 1 || receipt.Usage.InputTokens != 900 || receipt.Operation != contextfabric.ModelOperationPhraseOffers {
		t.Fatalf("provider calls = %d attempts = %d input tokens = %d operation = %q, want 1, 1, 900, %q",
			len(client.requests), receipt.Attempts, receipt.Usage.InputTokens, receipt.Operation, contextfabric.ModelOperationPhraseOffers)
	}

	served := &scriptedLLM{model: refusalModel, replies: []llmReply{refusalReply()}}
	result := contextfabric.RuntimeOfferPhraser{Runtime: refusalRuntime(t, served, nil)}.Phrase(context.Background(), principal, validPhrasingInput())
	if result.Outcome != contextfabric.OfferPhrasingCallFailed || len(result.Phrasings) != 0 {
		t.Fatalf("outcome = %q phrasings = %d, want call_failed and none", result.Outcome, len(result.Phrasings))
	}
	if len(served.requests) != 1 {
		t.Fatalf("provider calls through the phraser = %d, want 1", len(served.requests))
	}
}
