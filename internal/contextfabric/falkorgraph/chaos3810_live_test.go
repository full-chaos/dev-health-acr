package falkorgraph

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestCHAOS3810LiveCorpusDoesNotFallThroughToAnUnclassifiedFailure is the
// live half of CHAOS-3810's evidence, run against the real organization graph
// the stack operator reproduced the blocker on (20k+ subjects, so every
// lexical search truncates).
//
// It composes a REAL Engine over the REAL adapter, with only the model legs
// stubbed: the interpreter returns a fixed InterpretedQuestion (no LLM
// needed to state what the question is about), and the synthesizer fails the
// test if it is called at all. The fact reader records whether it ran.
//
// The assertion is the ticket's own definition of done: the investigation
// returns a RESULT, never an error. If nothing committed, that result is
// clarification_required (with candidates and a prompt) or no_match -- the
// two outcomes the contract has always had -- and no fact read was attempted.
// If the exact-match fix committed a subject instead, the fact read runs and
// the terminal path is not taken. Both are passes; a 500-shaped error is not.
//
// Read-only: it opens its own client, runs queries, and never writes.
// Skipped unless the live inputs are set, so it is inert in CI.
func TestCHAOS3810LiveCorpusDoesNotFallThroughToAnUnclassifiedFailure(t *testing.T) {
	if os.Getenv("ACR_TEST_FALKOR_ADDR") == "" {
		t.Skip("ACR_TEST_FALKOR_ADDR is not set; this probe measures against the live dev graph")
	}
	orgID := os.Getenv("ACR_TEST_LIVE_ORG")
	if orgID == "" {
		t.Skip("ACR_TEST_LIVE_ORG is not set")
	}
	question := os.Getenv("ACR_TEST_LIVE_QUESTION")
	if question == "" {
		question = "Most of the work is closed, so why is Ask Dev still not ready to ship?"
	}
	term := os.Getenv("ACR_TEST_LIVE_TERM")
	if term == "" {
		term = "Ask Dev"
	}

	graphConfig, err := ConfigFromEnv(benchmarkLookup)
	if err != nil {
		t.Fatalf("graph configuration: %v", err)
	}
	adapter, err := New(graphConfig)
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	ctx := context.Background()
	principal := storage.Principal{OrgID: orgID}
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status_and_drivers",
		SubjectTerms: []string{term}, TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	request := contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_live3810",
		Question: question, TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
		},
		Consumer: contextfabric.ConsumerInfo{Name: "chaos-3810-probe", Version: "0.1.0", Surface: "test"},
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("probe request is invalid: %v", err)
	}

	// Diagnostics first: the resolution alone, so the log records what the
	// live corpus actually produced (this is the state the blocker's
	// diagnosis describes).
	resolution, _, _, _, err := adapter.ResolveSubjects(ctx, principal, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	t.Logf("live resolution for %q: committed=%d candidates=%d prompt=%q",
		term, len(resolution.Committed), len(resolution.Candidates), resolution.ClarificationPrompt)

	factsRead, synthesized := false, false
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: fixedInterpreter{interpretation: interpreted},
		Graph:       adapter,
		Facts: factsFunc(func(_ context.Context, _ storage.Principal, factRequest contextfabric.CanonicalFactRequest) (contextfabric.CanonicalFactBundle, error) {
			factsRead = true
			if len(factRequest.Subjects) == 0 && factRequest.Cohort == nil {
				t.Fatal("the fact read was reached with no subjects -- the CHAOS-3810 invariant is broken on live data")
			}
			return contextfabric.CanonicalFactBundle{
				Facts:    []contextfabric.CanonicalFact{},
				Coverage: contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}},
				Version:  "probe-v1",
			}, nil
		}),
		// The synthesizer stands in for the model leg. It must NOT run on
		// the terminal path (asserted below); on the committed path it
		// returns a minimal valid draft so the probe can prove the fact read
		// happened without needing a live LLM.
		Synthesizer: synthesizeFunc(func(context.Context, storage.Principal, contextfabric.SynthesisInput) (contextfabric.InvestigationResult, error) {
			synthesized = true
			return contextfabric.InvestigationResult{
				Status: contextfabric.InvestigationNoMatch, StrongestPressures: []string{},
				Drivers: []contextfabric.DriverJudgment{}, RemainingWork: []contextfabric.Finding{},
				ReadinessGaps: []contextfabric.Finding{}, Paths: []contextfabric.RelationshipPath{},
				Conflicts: []contextfabric.Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
				ClaimedFacts: []contextfabric.ClaimedFact{},
				Coverage:     contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}},
				Versions: contextfabric.VersionSet{
					ServiceVersion: "chaos-3810-probe", ContractVersion: contextfabric.InvestigationResultSchemaV1,
					Backend: "graph", ProjectionVersion: "probe", QueryVersion: "probe",
					InterpretationVersion: "probe", SynthesisVersion: "probe", CanonicalServiceVersion: "probe",
				},
				DeterministicAnswer: "probe", Warnings: []string{},
			}, nil
		}),
	}, contextfabric.EngineOptions{
		ServiceVersion: "chaos-3810-probe",
		Now:            func() time.Time { return time.Now().UTC() },
		NewResultID:    func() string { return "result_live38100001" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	result, err := engine.Investigate(ctx, principal, request)
	if len(resolution.Committed) > 0 {
		// The exact-match fix committed a subject on a truncated live
		// search: the investigation must now run the fact read and synthesis
		// like any answerable question, not divert to the terminal path.
		if err != nil {
			t.Fatalf("Investigate() error = %v for a committed live resolution", err)
		}
		if !factsRead {
			t.Fatal("a committed resolution did not reach the fact read")
		}
		t.Logf("live outcome: committed %d subject(s) on a truncated search; facts read, status=%q", len(resolution.Committed), result.Status)
		return
	}
	if synthesized {
		t.Fatal("the synthesizer ran for a resolution that committed nothing: the terminal path must need no model")
	}
	if err != nil {
		t.Fatalf("Investigate() error = %v -- an unresolved subject must produce a RESULT, not the unclassified failure that became a 500", err)
	}
	if factsRead {
		t.Fatal("facts were read for a resolution that committed nothing")
	}
	switch result.Status {
	case contextfabric.InvestigationClarificationRequired:
		if result.SubjectResolution.ClarificationPrompt == "" || len(result.SubjectResolution.Candidates) == 0 {
			t.Fatalf("clarification result carries no prompt/candidates: %#v", result.SubjectResolution)
		}
		t.Logf("live outcome: clarification_required with %d candidates, prompt=%q",
			len(result.SubjectResolution.Candidates), result.SubjectResolution.ClarificationPrompt)
	case contextfabric.InvestigationNoMatch:
		t.Logf("live outcome: no_match (nothing in this organization's graph matched %q)", term)
	default:
		t.Fatalf("Status = %q, want clarification_required or no_match for a resolution that committed nothing", result.Status)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("live terminal result failed contract validation: %v", err)
	}
}

type factsFunc func(context.Context, storage.Principal, contextfabric.CanonicalFactRequest) (contextfabric.CanonicalFactBundle, error)

func (f factsFunc) ReadFacts(ctx context.Context, principal storage.Principal, request contextfabric.CanonicalFactRequest) (contextfabric.CanonicalFactBundle, error) {
	return f(ctx, principal, request)
}

type synthesizeFunc func(context.Context, storage.Principal, contextfabric.SynthesisInput) (contextfabric.InvestigationResult, error)

func (f synthesizeFunc) Synthesize(ctx context.Context, principal storage.Principal, input contextfabric.SynthesisInput) (contextfabric.InvestigationResult, error) {
	return f(ctx, principal, input)
}
