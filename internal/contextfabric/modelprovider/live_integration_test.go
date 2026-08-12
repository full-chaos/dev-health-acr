package modelprovider

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Live acceptance for CHAOS-3770. Unlike the recorded-transport tests in
// provider_test.go, this file talks to a real provider and therefore costs
// money and needs network, so it is skipped unless a credential is supplied
// explicitly:
//
//	ACR_TEST_MODEL_API_KEY=... go test ./internal/contextfabric/modelprovider -run Live -v
//
// ACR_TEST_MODEL, ACR_TEST_MODEL_PROVIDER and ACR_TEST_MODEL_BASE_URL
// override the model, provider name and endpoint, so the same test doubles
// as the acceptance probe for a BYO endpoint (point it at a local
// OpenAI-compatible server and supply any placeholder credential).
//
// The gating variables are ACR_TEST_* rather than the production
// ACR_CONTEXT_FABRIC_MODEL_* names on purpose: a developer shell that
// happens to carry the production configuration must not silently start
// billing a provider during `make verify`.
const (
	envLiveAPIKey   = "ACR_TEST_MODEL_API_KEY"
	envLiveModel    = "ACR_TEST_MODEL"
	envLiveProvider = "ACR_TEST_MODEL_PROVIDER"
	envLiveBaseURL  = "ACR_TEST_MODEL_BASE_URL"
)

func liveConfig(t *testing.T) Config {
	t.Helper()
	apiKey := os.Getenv(envLiveAPIKey)
	if apiKey == "" {
		t.Skipf("%s is not set; skipping live provider acceptance", envLiveAPIKey)
	}
	cfg := Config{
		Provider: DefaultProvider, Model: DefaultModel, APIKey: apiKey,
		Timeout: 90 * time.Second, MaxAttempts: 2, MaxTransportRetries: 2,
	}
	if value := os.Getenv(envLiveProvider); value != "" {
		cfg.Provider = value
	}
	if value := os.Getenv(envLiveModel); value != "" {
		cfg.Model = value
	}
	if value := os.Getenv(envLiveBaseURL); value != "" {
		cfg.BaseURL = value
	}
	return cfg
}

func TestLiveProviderInterpretsARealQuestion(t *testing.T) {
	// Given a real provider and model.
	cfg := liveConfig(t)
	runtime, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}

	// When
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	interpreted, receipt, err := runtime.InterpretQuestion(ctx, storage.Principal{OrgID: "org_live_acceptance"}, testRequest())

	// Then the model must produce an interpretation that passes ACR's own
	// semantic validation, not merely a syntactically valid response.
	if err != nil {
		t.Fatalf("InterpretQuestion() = %v, want a valid interpretation from %s/%s", err, cfg.Provider, cfg.Model)
	}
	if err := interpreted.Validate(); err != nil {
		t.Fatalf("interpretation failed ACR validation: %v", err)
	}
	if receipt.Outcome != "success" || receipt.Usage.TotalTokens == 0 {
		t.Fatalf("receipt = %#v, want a successful receipt with real token usage", receipt)
	}
	t.Logf("live interpretation: model=%s shape=%s judgment=%q requirements=%d attempts=%d tokens=%d",
		receipt.Model, interpreted.Shape, interpreted.RequestedJudgment,
		len(interpreted.FactRequirements), receipt.Attempts, receipt.Usage.TotalTokens)
}

func TestLiveProviderSynthesizesARealAnswer(t *testing.T) {
	// Given a real provider and a grounded synthesis input.
	cfg := liveConfig(t)
	runtime, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}

	// When
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	draft, receipt, err := runtime.SynthesizeAnswer(ctx, storage.Principal{OrgID: "org_live_acceptance"}, liveSynthesisInput())

	// Then. Synthesis is the strictest validator in the pipeline (claimed
	// facts must match the canonical bundle by exact value, every cited
	// evidence ref must exist), so a small model can legitimately fail it.
	// What this acceptance asserts is the WIRING contract: the call reaches
	// the provider, and any failure arrives as one of the ACR sentinels
	// rather than as an unclassified error. Whether a given model is good
	// enough at the task is the CHAOS-3756 evaluator's question, not this
	// test's.
	if err != nil {
		if !errors.Is(err, contextfabric.ErrModelOutput) &&
			!errors.Is(err, contextfabric.ErrModelRateLimited) &&
			!errors.Is(err, contextfabric.ErrModelUnavailable) {
			t.Fatalf("SynthesizeAnswer() = %v, want a classified model error", err)
		}
		t.Logf("live synthesis rejected by ACR validation (classified %v); receipt outcome=%s tokens=%d",
			err, receipt.Outcome, receipt.Usage.TotalTokens)
		return
	}
	if err := draft.ValidateAgainst(liveSynthesisInput()); err != nil {
		t.Fatalf("returned draft does not validate against its own input: %v", err)
	}
	t.Logf("live synthesis: model=%s status=%s drivers=%d answer=%q tokens=%d",
		receipt.Model, draft.Status, len(draft.Drivers), draft.DeterministicAnswer, receipt.Usage.TotalTokens)
}

func liveSynthesisInput() contextfabric.SynthesisInput {
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	workItem := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_1", Label: "Release acceptance"}
	path := contextfabric.RelationshipPath{
		PathID: "path_12345678", Nodes: []contextfabric.SubjectRef{project, workItem},
		Edges: []contextfabric.RelationshipEdge{{
			Type: "REQUIRES", From: project, To: workItem,
			Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
			EvidenceRefIDs: []string{"evidence_release_1234"},
		}},
		WhyRelevant: "The open work blocks release.", EvidenceRefIDs: []string{"evidence_release_1234"},
	}
	return contextfabric.SynthesisInput{
		Request: testRequest(),
		Interpretation: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeOpen, RequestedJudgment: "actual_status_and_current_drivers",
			TimeContext:      contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
		},
		Graph: contextfabric.GraphContext{
			Resolution: contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{project}},
			Paths:      []contextfabric.RelationshipPath{path}, EvidenceRefIDs: []string{"evidence_release_1234"},
			Coverage: contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}},
		},
		Facts: contextfabric.CanonicalFactBundle{
			Facts: []contextfabric.CanonicalFact{{
				Kind: contextfabric.FactReadiness, Subject: project,
				Fields:         map[string]contextfabric.FactValue{"release_ready": contextfabric.BooleanFactValue(false)},
				EvidenceRefIDs: []string{"evidence_release_1234"}, SourceState: contextfabric.SourceAvailable,
			}},
			Coverage: contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-v1",
		},
	}
}
