package modelprovider

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const zeroClaimSynthesisJSON = `{
	"status": "complete",
	"direct_judgment": "Ask Dev is not release-ready.",
	"current_state": "Tracked completion and release readiness diverge.",
	"strongest_pressures": ["Release acceptance remains open."],
	"drivers": [{
		"driver_id": "driver_12345678", "standing": "principal", "category": "relationship",
		"title": "Release acceptance remains open", "summary": "Required acceptance has not completed.",
		"affected_subjects": [{"kind": "project", "canonical_id": "project_ask_dev", "label": "Ask Dev"}],
		"path_ids": ["path_12345678"], "evidence_ref_ids": ["evidence_release_1234"],
		"derivation": "rule_inferred", "epistemic_status": "inferred", "confidence": 0.9, "current": true
	}],
	"remaining_work": [], "readiness_gaps": [], "conflicts": [], "limitations": [],
	"evidence_ref_ids": ["evidence_release_1234"], "claimed_facts": [],
	"deterministic_answer": "Ask Dev is not release-ready because release acceptance remains open.",
	"warnings": []
}`

func zeroClaimSynthesisInput() contextfabric.SynthesisInput {
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	work := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_1", Label: "Release acceptance"}
	path := contextfabric.RelationshipPath{
		PathID: "path_12345678", Nodes: []contextfabric.SubjectRef{project, work},
		Edges: []contextfabric.RelationshipEdge{{
			Type: "BLOCKS", From: project, To: work,
			Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
			EvidenceRefIDs: []string{"evidence_release_1234"},
		}},
		WhyRelevant: "The open work blocks release.", EvidenceRefIDs: []string{"evidence_release_1234"},
	}
	return contextfabric.SynthesisInput{
		Request: testRequest(),
		Interpretation: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeOpen, RequestedJudgment: "fallback_open_investigation",
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

// TestNew_fallbackLegDrawsOnceEvenWhenItsAnswerClaimsNothing drives the real
// composed runtime against a recorded provider: the primary model is
// unavailable, the fallback model answers with a valid draft that claims no
// fact although the input's fact carries a reference. The fallback leg is a
// single draw by design, so the provider sees exactly one fallback call.
func TestNew_fallbackLegDrawsOnceEvenWhenItsAnswerClaimsNothing(t *testing.T) {
	const fallbackModel = "gpt-5.9-fallback-test"
	var mu sync.Mutex
	fallbackCalls := 0
	provider := recordingProvider(t, func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		if !strings.Contains(string(body), fallbackModel) {
			providerError(http.StatusServiceUnavailable, "engine_overloaded", "overloaded")(writer, request)
			return
		}
		mu.Lock()
		fallbackCalls++
		mu.Unlock()
		chatCompletion(t, zeroClaimSynthesisJSON)(writer, request)
	})
	cfg := testConfig(provider)
	cfg.FallbackModel = fallbackModel

	runtime, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	draft, receipt, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_test"}, zeroClaimSynthesisInput())
	if err != nil {
		t.Fatalf("SynthesizeAnswer() = %v, want the fallback's answer", err)
	}
	if !receipt.FallbackUsed || len(draft.ClaimedFacts) != 0 {
		t.Fatalf("receipt.FallbackUsed = %v, claims = %d, want the fallback's zero-claim answer served", receipt.FallbackUsed, len(draft.ClaimedFacts))
	}
	mu.Lock()
	defer mu.Unlock()
	if fallbackCalls != 1 {
		t.Fatalf("fallback model calls = %d, want exactly 1 -- the fallback leg is a single draw", fallbackCalls)
	}
}

// TestFallbackRuntimeConfigIsSingleDraw and its primary counterpart pin the
// builders the composition reads.
func TestFallbackRuntimeConfigIsSingleDraw(t *testing.T) {
	cfg := Config{Provider: "test-provider", Model: "primary-model", FallbackModel: "fallback-model", MaxSynthesisResynthesisAttempts: 3, PhrasingModel: "phrasing-model"}
	got := fallbackRuntimeConfig(nil, cfg)
	if !got.SingleDraw || got.Model != "fallback-model" || got.MaxSynthesisResynthesisAttempts != 0 || got.PhrasingModel != "" {
		t.Fatalf("fallbackRuntimeConfig() = single_draw %v model %q attempts %d phrasing %q, want a single-draw fallback-model config with no inherited bound or phrasing model", got.SingleDraw, got.Model, got.MaxSynthesisResynthesisAttempts, got.PhrasingModel)
	}
	if primary := runtimeConfigWithPhrasing(nil, cfg, cfg.Model, nil); primary.SingleDraw {
		t.Fatal("the primary runtime must not be single-draw")
	}
}
