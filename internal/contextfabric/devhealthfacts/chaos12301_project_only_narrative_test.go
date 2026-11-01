package devhealthfacts_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12301, lead ruling (b): a project-only answer keeps its narrative. Every
// fact kind is withheld from this caller, so a floor that withheld the narrative
// too would leave it almost no written answer, even a correct graph-only one.

// withheldFactSecret is a value only a provider holds. If it reaches the answer,
// a withheld fact reached the answer.
const withheldFactSecret = "SECRET-RISK-SCORE-4711"

// secretFactProvider wraps a production provider's capability and serves the
// secret from every read, counting them. The registry's refusal is the only
// thing between it and the model.
type secretFactProvider struct {
	contextfabric.FactProvider
	reads *atomic.Int64
}

func (p secretFactProvider) ReadFacts(_ context.Context, _ storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	p.reads.Add(1)
	facts := make([]contextfabric.CanonicalFact, 0, len(query.Subjects))
	for _, subject := range query.Subjects {
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: query.Kind, Subject: subject, Source: "secret-spy", SourceVersion: "spy", SourceState: contextfabric.SourceAvailable,
			Fields:         map[string]contextfabric.FactValue{"severity": contextfabric.StringFactValue(withheldFactSecret)},
			EvidenceRefIDs: []string{"ev-secret"},
		})
	}
	return contextfabric.FactProviderResult{Facts: facts, State: contextfabric.SourceAvailable, Version: "spy"}, nil
}

func secretProviders(reads *atomic.Int64) func([]contextfabric.FactProvider) []contextfabric.FactProvider {
	return func(real []contextfabric.FactProvider) []contextfabric.FactProvider {
		wrapped := make([]contextfabric.FactProvider, 0, len(real))
		for _, provider := range real {
			wrapped = append(wrapped, secretFactProvider{FactProvider: provider, reads: reads})
		}
		return wrapped
	}
}

const modelNarrative = "The graph shows the project moving."

// echoingDraft writes everything synthesis was given into the prose the model
// authors, so what the model could know is what the answer shows. The server
// composes the lead fields and never reads the draft's, so the model's words
// reach the answer through strongest_pressures.
func echoingDraft(input contextfabric.SynthesisInput, draft contextfabric.SynthesisDraft) contextfabric.SynthesisDraft {
	given, err := json.Marshal(input.Facts.Facts)
	if err != nil {
		panic(err)
	}
	draft.StrongestPressures = []string{modelNarrative, string(given)}
	return draft
}

func projectOnlyNarrativeRun(t *testing.T, principal storage.Principal) (projectOnlyEngineRun, int64) {
	t.Helper()
	var reads atomic.Int64
	project := chaos12330ProjectCase()
	model := &projectOnlyEngineModel{
		kinds: []contextfabric.FactKind{contextfabric.FactHealth}, providers: secretProviders(&reads), draft: echoingDraft,
	}
	run := runProjectOnlyEngineWith(t, principal, project.subject, project.attrs, model, contextfabric.RequestedScope{})
	if run.err != nil {
		t.Fatalf("Investigate() error = %v", run.err)
	}
	return run, reads.Load()
}

// The state the ruling exists to reach: the model's narrative is served, the
// status is partial with the project-scope sentence, and no withheld value is in
// it. The provider that holds the value is never called, and the echo shows the
// model was not given one.
func TestAProjectOnlyAnswerKeepsItsNarrativeAndNoWithheldValue(t *testing.T) {
	t.Parallel()
	run, reads := projectOnlyNarrativeRun(t, chaos12330ProjectOnly())
	result := run.result
	if reads != 0 {
		t.Fatalf("the provider holding the secret was read %d times", reads)
	}
	if run.model.synthesized == 0 || !slices.Contains(result.StrongestPressures, modelNarrative) {
		t.Fatalf("the model's narrative was not served: synthesized %d, pressures %q", run.model.synthesized, result.StrongestPressures)
	}
	if slices.Contains(result.Limitations, contractsv1.ContextFabricSynthesisNarrativeWithheldLimitation) {
		t.Fatalf("limitations = %q, want no narrative-withheld sentence", result.Limitations)
	}
	if result.Status != contextfabric.InvestigationPartial || !result.Coverage.Partial {
		t.Fatalf("status %q, coverage partial %v, want partial", result.Status, result.Coverage.Partial)
	}
	if result.Completeness.State == contractsv1.ContextFabricAnswerCompletenessComplete {
		t.Fatal("completeness reads complete")
	}
	// The head is the server's and states the served status.
	if want := "Some canonical or graph coverage was unavailable."; !strings.Contains(result.DirectJudgment, want) || !strings.Contains(result.DeterministicAnswer, want) {
		t.Fatalf("head = %q, %q, want the partial sentence", result.DirectJudgment, result.DeterministicAnswer)
	}
	if !slices.Contains(result.Limitations, contractsv1.ContextFabricProjectScopeWithheldLimitation) {
		t.Fatalf("limitations = %q, want the project-scope sentence", result.Limitations)
	}
	body, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), withheldFactSecret) {
		t.Fatalf("a withheld value reached the answer: %s", body)
	}
}

// The control: the same spy and the same echo, for a caller allowed the fact.
// The provider is read and the value reaches the model, so the assertion above
// could fail.
func TestTheSecretReachesAnAllowedCallerSoTheProjectOnlyAssertionCanFail(t *testing.T) {
	t.Parallel()
	run, reads := projectOnlyNarrativeRun(t, storage.Principal{OrgID: projectOnlyEngineOrg})
	if reads == 0 {
		t.Fatal("this control lost its provider read")
	}
	given, err := json.Marshal(run.model.facts.Facts)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(given), withheldFactSecret) {
		t.Fatalf("this control lost its fact: synthesis was given %s", given)
	}
	body, err := json.Marshal(run.result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), withheldFactSecret) {
		t.Fatalf("this control lost its echo: the answer does not carry the value: %s", body)
	}
}

// Ruling condition 4: a value the model states with no fact behind it meets the
// same check as any other answer. No claim survives on a project-only answer,
// because no fact is read for this caller.
func TestAProjectOnlyClaimWithNoFactBehindItIsNotServed(t *testing.T) {
	t.Parallel()
	var reads atomic.Int64
	project := chaos12330ProjectCase()
	claimed := func(_ contextfabric.SynthesisInput, draft contextfabric.SynthesisDraft) contextfabric.SynthesisDraft {
		draft.ClaimedFacts = []contextfabric.ClaimedFact{{
			ClaimID: "claim-1", Kind: contextfabric.FactHealth, Subject: project.subject, Field: "severity",
			Value: contractsv1.ContextFabricScalarValue{String: stringPointer(withheldFactSecret)},
		}}
		return draft
	}
	model := &projectOnlyEngineModel{kinds: []contextfabric.FactKind{contextfabric.FactHealth}, providers: secretProviders(&reads), draft: claimed}
	run := runProjectOnlyEngineWith(t, chaos12330ProjectOnly(), project.subject, project.attrs, model, contextfabric.RequestedScope{})
	if reads.Load() != 0 {
		t.Fatalf("the provider was read %d times", reads.Load())
	}
	body, err := json.Marshal(run.result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), withheldFactSecret) || len(run.result.ClaimedFacts) != 0 {
		t.Fatalf("an unsupported claim was served: err %v, claims %+v", run.err, run.result.ClaimedFacts)
	}
}

func stringPointer(value string) *string { return &value }
