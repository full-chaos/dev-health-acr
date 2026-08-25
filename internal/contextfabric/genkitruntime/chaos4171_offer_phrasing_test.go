package genkitruntime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func validPhrasingInput() contextfabric.StructureOfferPhrasingInput {
	return contextfabric.StructureOfferPhrasingInput{
		Options: []contextfabric.StructureOfferPhrasingOption{
			{OptionID: "opt_pr", Member: "expected_kind", Kind: "pull_request", Label: "a pull request"},
		},
	}
}

func TestRuntimePhraseStructureOffersRejectsMissingOrgID(t *testing.T) {
	t.Parallel()
	runtime := mustRuntime(t, &generatorStub{}, Config{})
	_, _, err := runtime.PhraseStructureOffers(context.Background(), storage.Principal{}, validPhrasingInput())
	if err == nil {
		t.Fatal("PhraseStructureOffers() error = nil, want an error for a missing org id")
	}
}

func TestRuntimePhraseStructureOffersRejectsEmptyOptions(t *testing.T) {
	t.Parallel()
	runtime := mustRuntime(t, &generatorStub{}, Config{})
	_, _, err := runtime.PhraseStructureOffers(context.Background(), storage.Principal{OrgID: "org_1"}, contextfabric.StructureOfferPhrasingInput{})
	if err == nil {
		t.Fatal("PhraseStructureOffers() error = nil, want an error for an empty offer set")
	}
}

// TestRuntimePhraseStructureOffersForwardsTheOfferSetVerbatim proves the
// generator receives exactly the options it was given -- no question, no
// evidence, nothing beyond option_id/member/kind/label -- matching the
// ratified design's "input is the offer set only".
func TestRuntimePhraseStructureOffersForwardsTheOfferSetVerbatim(t *testing.T) {
	t.Parallel()
	gen := &generatorStub{phrasing: phrasingOutput{Phrasings: []phrasingEntryOutput{{OptionID: "opt_pr", Phrasing: "an open pull request"}}}}
	runtime := mustRuntime(t, gen, Config{})
	draft, receipt, err := runtime.PhraseStructureOffers(context.Background(), storage.Principal{OrgID: "org_1"}, validPhrasingInput())
	if err != nil {
		t.Fatalf("PhraseStructureOffers() error = %v", err)
	}
	if receipt.Operation != contextfabric.ModelOperationPhraseOffers || receipt.Outcome != "success" {
		t.Fatalf("receipt = %#v", receipt)
	}
	if len(gen.requests) != 1 {
		t.Fatalf("generator called %d times, want 1", len(gen.requests))
	}
	if !containsAll(gen.requests[0].Prompt, "opt_pr", "expected_kind", "pull_request", "a pull request") {
		t.Fatalf("prompt did not carry the offer set verbatim: %q", gen.requests[0].Prompt)
	}
	if len(draft.Phrasings) != 1 || draft.Phrasings[0].OptionID != "opt_pr" || draft.Phrasings[0].Phrasing != "an open pull request" {
		t.Fatalf("draft = %#v", draft)
	}
}

func TestRuntimePhraseStructureOffersDefaultsToThePrimaryModel(t *testing.T) {
	t.Parallel()
	gen := &generatorStub{}
	runtime := mustRuntime(t, gen, Config{})
	_, receipt, err := runtime.PhraseStructureOffers(context.Background(), storage.Principal{OrgID: "org_1"}, validPhrasingInput())
	if err != nil {
		t.Fatalf("PhraseStructureOffers() error = %v", err)
	}
	if receipt.Model != runtime.config.Model {
		t.Fatalf("receipt.Model = %q, want the primary model %q (unset ACR_CONTEXT_FABRIC_MODEL_PHRASING defaults to it)", receipt.Model, runtime.config.Model)
	}
	if gen.requests[0].Model != runtime.config.ModelRef {
		t.Fatalf("generator request Model = %q, want %q", gen.requests[0].Model, runtime.config.ModelRef)
	}
}

func TestRuntimePhraseStructureOffersHonorsADistinctPhrasingModel(t *testing.T) {
	t.Parallel()
	gen := &generatorStub{}
	runtime := mustRuntime(t, gen, Config{PhrasingModel: "test/phrasing-model"})
	_, receipt, err := runtime.PhraseStructureOffers(context.Background(), storage.Principal{OrgID: "org_1"}, validPhrasingInput())
	if err != nil {
		t.Fatalf("PhraseStructureOffers() error = %v", err)
	}
	if receipt.Model != "test/phrasing-model" {
		t.Fatalf("receipt.Model = %q, want the configured phrasing model", receipt.Model)
	}
	if gen.requests[0].Model != "test/phrasing-model" {
		t.Fatalf("generator request Model = %q, want the configured phrasing model", gen.requests[0].Model)
	}
}

// TestRuntimePhraseStructureOffersRecordsFailureOnGenerationError is
// RED-FIRST evidence that a transport/provider failure still returns a
// receipt (never a bare error with no audit trail), classified as a
// failure outcome, with no draft.
func TestRuntimePhraseStructureOffersRecordsFailureOnGenerationError(t *testing.T) {
	t.Parallel()
	gen := &generatorStub{phrasingErr: errors.New("provider unavailable")}
	runtime := mustRuntime(t, gen, Config{})
	draft, receipt, err := runtime.PhraseStructureOffers(context.Background(), storage.Principal{OrgID: "org_1"}, validPhrasingInput())
	if err == nil {
		t.Fatal("PhraseStructureOffers() error = nil, want the classified generation error")
	}
	if receipt.Operation != contextfabric.ModelOperationPhraseOffers {
		t.Fatalf("receipt.Operation = %q, want %q", receipt.Operation, contextfabric.ModelOperationPhraseOffers)
	}
	if receipt.Outcome == "success" {
		t.Fatalf("receipt.Outcome = %q, want a failure classification", receipt.Outcome)
	}
	if len(draft.Phrasings) != 0 {
		t.Fatalf("draft = %#v, want empty on failure", draft)
	}
}

func containsAll(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(haystack, needle) {
			return false
		}
	}
	return true
}
