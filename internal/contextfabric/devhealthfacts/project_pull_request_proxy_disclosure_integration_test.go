package devhealthfacts_test

import (
	"context"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A project's pull requests are reached by the activity proxy: the pull
// requests of the repositories its issues' linked pull requests are in. The
// answer says so. Through the real Engine.Investigate over a real ClickHouse,
// with the production scope expander, providers and registry, a project
// pull request question serves the activity-proxy limitation and a partial
// coverage. Only the graph and the model are doubles.
func TestProjectPullRequestAnswerDisclosesTheActivityProxyThroughTheEngine(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	s := newProjectRollupSeed(t, ctx, direct)
	s.workItem("i-p1", "repo-a", "issue", "open", "proj-p")
	s.pullRequest("repo-a", 1, s.at, nil, nil)
	s.pullRequest("repo-a", 2, s.at, nil, nil)
	s.link("repo-a", "i-p1", 1, "native")

	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{
		ScopeExpander: devhealthfacts.NewScopeExpander(query),
		Logger:        slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	project := projectRollupRef(t, "proj-p")
	attrs := map[string]interface{}{"subject_kind": "project", "canonical_id": project.CanonicalID, "label": "Payments", "authorization_repositories": []string{"acme/repo-a"}, "authorization_projects": "*", "authorization_teams": "*"}
	graph := &projectOnlyEngineGraph{t: t, subject: project, subjectAttrs: attrs}
	model := &projectOnlyEngineModel{kinds: []contextfabric.FactKind{contextfabric.FactPullRequests}, draft: claimProjectOpenPullRequests}
	at := time.Now().UTC()
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter:  contextfabric.RuntimeQuestionInterpreter{Runtime: model, Requirements: registry},
		Graph:        graph,
		Facts:        registry,
		Requirements: registry,
		Telemetry:    contextfabric.NewSlogEngineTelemetry(slog.New(slog.DiscardHandler)),
		Synthesizer:  contextfabric.RuntimeAnswerSynthesizer{Runtime: model, Options: contextfabric.RuntimeAnswerSynthesizerOptions{ServiceVersion: "proxy-disclosure", Backend: "graph"}},
		Results:      memoryinvestigation.NewStore(),
		CandidateVerifier: func(context.Context, storage.Principal, contextfabric.RequestedScope, contextfabric.ResolvedGraphBinding, contextfabric.SubjectKind, string) (bool, contextfabric.CandidateVerificationReason) {
			return true, contextfabric.CandidateVerificationValid
		},
	}, contextfabric.EngineOptions{ServiceVersion: "proxy-disclosure", Now: func() time.Time { return at }, NewResultID: func() string { return "result_proxy_disclosure" }})
	if err != nil {
		t.Fatal(err)
	}
	request := contextfabric.InvestigationRequest{SchemaVersion: contractsv1.ContextFabricInvestigationRequestSchema, RequestID: "request_proxy_disclosure", Question: "How are the pull requests of Payments doing?", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, EvidenceWindow: &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}}, RequestedScope: contextfabric.RequestedScope{RepositorySlugs: []string{"acme/repo-a"}}, Options: contractsv1.ContextFabricInvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50, MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 1048576, AllowClarification: true}, Consumer: contractsv1.ContextFabricConsumerInfo{Name: "proxy-disclosure", Version: "1.0.0", Surface: "workbench"}}

	result, err := engine.Investigate(ctx, storage.Principal{OrgID: projectRollupOrg, RepositoryScopes: []string{"acme/repo-a"}}, request)
	if err != nil {
		t.Fatalf("Engine.Investigate: %v", err)
	}
	if !model.input {
		t.Fatal("the answer was never synthesized, so the disclosure was not exercised")
	}
	proxied := 0
	for _, derivation := range model.facts.Scope.Derivations {
		if derivation.Root.CanonicalID != project.CanonicalID || derivation.Target.Kind != contextfabric.SubjectPullRequest {
			continue
		}
		if derivation.Policy != contextfabric.FactScopePolicyProjectWorkItemPullRequest || derivation.Basis != contextfabric.FactScopeBasisActivityProxy {
			t.Fatalf("derivation %+v, want policy %s on basis %s", derivation, contextfabric.FactScopePolicyProjectWorkItemPullRequest, contextfabric.FactScopeBasisActivityProxy)
		}
		proxied++
	}
	if proxied == 0 {
		t.Fatalf("no project -> pull request derivation reached the synthesis input: %+v", model.facts.Scope.Derivations)
	}
	if !slices.Contains(result.Limitations, contractsv1.ContextFabricFactScopeActivityProxyLimitation) {
		t.Fatalf("limitations = %q, want the activity-proxy disclosure on the answer", result.Limitations)
	}
	if !result.Coverage.Partial {
		t.Fatal("Coverage.Partial = false on an answer whose pull requests were reached by the activity proxy")
	}
	if len(result.SubjectResolution.Committed) != 1 || result.SubjectResolution.Committed[0].CanonicalID != project.CanonicalID {
		t.Fatalf("committed = %+v, want the project as the answer's subject (status %s, limitations %q)", result.SubjectResolution.Committed, result.Status, result.Limitations)
	}
}

// claimProjectOpenPullRequests drafts one claim on the project's own pull
// request rollup, so the answer commits the project.
func claimProjectOpenPullRequests(input contextfabric.SynthesisInput, draft contextfabric.SynthesisDraft) contextfabric.SynthesisDraft {
	for _, fact := range input.Facts.Facts {
		value := fact.Fields["pull_requests_open_current"].Integer
		if fact.Kind != contextfabric.FactPullRequests || fact.Subject.Kind != contextfabric.SubjectProject || value == nil {
			continue
		}
		draft.DirectJudgment, draft.CurrentState, draft.DeterministicAnswer = "Open pull requests of the project.", "Open pull requests of the project.", "Open pull requests of the project."
		draft.ClaimedFacts = []contextfabric.ClaimedFact{{ClaimID: "claim_proxy_disclosure_0001", Kind: fact.Kind, Subject: fact.Subject, Field: "pull_requests_open_current", Value: contextfabric.ScalarValue{Integer: value}}}
		draft.EvidenceRefIDs = append([]string(nil), fact.EvidenceRefIDs...)
		draft.Drivers = []contextfabric.DriverJudgment{{
			DriverID: "driver_proxy_disclosure_01", Standing: contextfabric.DriverPrincipal, Category: "narrative",
			Title: "Pull requests", Summary: "Open pull requests of the project.",
			AffectedSubjects: []contextfabric.SubjectRef{fact.Subject}, EvidenceRefIDs: append([]string(nil), fact.EvidenceRefIDs...),
			ClaimedFactIDs: []string{"claim_proxy_disclosure_0001"},
			Derivation:     contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved, Confidence: 0.9, Current: true,
		}}
		return draft
	}
	return draft
}
