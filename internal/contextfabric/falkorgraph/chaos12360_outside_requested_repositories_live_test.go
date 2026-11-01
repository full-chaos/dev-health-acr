package falkorgraph

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func outsideMatchRequest(question string) contextfabric.InvestigationRequest {
	return contextfabric.InvestigationRequest{SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_live12360",
		Question: question, TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Options:  contextfabric.InvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50, MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true},
		Consumer: contextfabric.ConsumerInfo{Name: "chaos-12360", Version: "0.1.0", Surface: "test"}}
}

// TestLiveANoMatchStatesTheMatchOutsideTheRequestedRepositories (CHAOS-12360,
// lead ruling): a term whose only match lies outside a requested repository
// narrowing still resolves to nothing, on the real engine over the real graph.
// An unrestricted principal is told a count on both terminals, and nobody is
// told a name. For a restricted or project-only caller the count would be an
// existence oracle, so the answer stays as it was. The engine's ask for an
// inferred window runs before any search, so it has nothing to state: the turn
// with a stated window does.
func TestLiveANoMatchStatesTheMatchOutsideTheRequestedRepositories(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	orgID := "live-12360-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	observed := time.Now().UTC()
	created := observed.Add(-48 * time.Hour)
	batch := contextfabric.ProjectionBatch{SchemaVersion: contextfabric.ProjectionBatchSchemaV1, OrgID: orgID, BatchID: "batch_12360_0001", Source: "live-test", SourceVersion: "v1", GeneratedAt: observed, NextCursor: "cursor-1",
		Entities: []contextfabric.EntityProjection{}, Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
		Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{}}
	for _, slug := range []string{"acme/payments-api", "acme/payments-worker"} {
		batch.Entities = append(batch.Entities, contextfabric.EntityProjection{
			Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:github:" + slug, Label: slug},
			Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{}, Authorization: contextfabric.AuthorizationScope{RepositorySlugs: []string{slug}},
			EvidenceRefIDs: []string{"evidence_" + strings.ReplaceAll(slug, "/", "_")}, ObservedAt: observed, ValidFrom: &created, SourceVersion: "v1"})
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("ApplyProjectionBatch() error = %v", err)
	}
	const term = "payments-worker"
	interpreted := contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status", SubjectTerms: []string{term},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}}}
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: fixedInterpreter{interpretation: interpreted},
		Graph:       adapter,
		Facts: factsFunc(func(context.Context, storage.Principal, contextfabric.CanonicalFactRequest) (contextfabric.CanonicalFactBundle, error) {
			t.Fatal("the fact read ran: a no_match reads no facts")
			return contextfabric.CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizeFunc(func(context.Context, storage.Principal, contextfabric.SynthesisInput) (contextfabric.InvestigationResult, error) {
			t.Fatal("synthesis ran: a no_match synthesizes nothing")
			return contextfabric.InvestigationResult{}, nil
		}),
	}, contextfabric.EngineOptions{ServiceVersion: "chaos-12360", Now: func() time.Time { return time.Now().UTC() }, NewResultID: func() string { return "result_live12360001" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	sentence := contractsv1.ContextFabricOutsideRequestedRepositoriesLimitation(1)
	narrowed := []string{"acme/payments-api"}
	for _, tc := range []struct {
		name      string
		principal storage.Principal
		repos     []string
		stated    bool
	}{
		{"an unrestricted caller is told the count", storage.Principal{OrgID: orgID}, narrowed, true},
		{"a universal caller is told the count", storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}}, narrowed, true},
		{"a restricted caller is told nothing of a match outside its grant", storage.Principal{OrgID: orgID, RepositoryScopes: narrowed}, narrowed, false},
		// CHAOS-12446: the shape a web user has at runtime: a browser credential
		// issued "*", which the access read replaced with the live inventory.
		{"a web user is told the count of a match its grant covers", storage.Principal{OrgID: orgID, RepositoryScopes: []string{"acme/payments-api", "acme/payments-worker"}, ClientClass: storage.CredentialClientClassBrowser}, narrowed, true},
		{"a project-only caller is told nothing", storage.Principal{OrgID: orgID, ProjectScopes: []string{"project.v2:github:P-1"}}, narrowed, false},
	} {
		for _, terminal := range []struct {
			name               string
			allowClarification bool
			window             *contextfabric.RequestedEvidenceWindow
		}{
			{"clarification declined", false, nil},
			{"a stated window", true, &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}},
		} {
			t.Run(tc.name+", "+terminal.name, func(t *testing.T) {
				request := outsideMatchRequest("how is " + term + " doing?")
				request.Options.AllowClarification = terminal.allowClarification
				request.TimeContext.EvidenceWindow = terminal.window
				request.RequestedScope = contextfabric.RequestedScope{RepositorySlugs: tc.repos}
				result, err := engine.Investigate(ctx, tc.principal, request)
				if err != nil {
					t.Fatalf("Investigate() error = %v", err)
				}
				if result.Status != contextfabric.InvestigationNoMatch {
					t.Fatalf("status %q, want no_match: the narrowing stands", result.Status)
				}
				if got := slices.Contains(result.Limitations, sentence); got != tc.stated {
					t.Fatalf("limitations %q: stated %v, want %v", result.Limitations, got, tc.stated)
				}
				for _, limitation := range result.Limitations {
					if strings.Contains(limitation, "acme/payments-worker") {
						t.Fatalf("limitation %q names the subject: a count, never a name", limitation)
					}
				}
			})
		}
	}
	t.Run("no narrowing refuses nothing", func(t *testing.T) {
		request := outsideMatchRequest("how is " + term + " doing?")
		resolution, _, _, _, err := adapter.ResolveSubjects(ctx, storage.Principal{OrgID: orgID}, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil, nil, "")
		if err != nil {
			t.Fatalf("ResolveSubjects() error = %v", err)
		}
		if resolution.OutsideRequestedRepositories != 0 || len(resolution.Candidates) == 0 {
			t.Fatalf("outside %d, candidates %d; want 0 and the match itself", resolution.OutsideRequestedRepositories, len(resolution.Candidates))
		}
	})
}
