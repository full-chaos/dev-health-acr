package devhealthsource_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// roundTracer keeps the evidence_round events of a resolution.
type roundTracer struct {
	mu     sync.Mutex
	rounds []graphrank.ResolutionTraceEvent
}

func (r *roundTracer) Trace(event graphrank.ResolutionTraceEvent) {
	if event.Stage != "evidence_round" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rounds = append(r.rounds, event)
}

func (r *roundTracer) narrowings() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.rounds))
	for _, e := range r.rounds {
		out = append(out, e.ShadowCallerNarrowing)
	}
	return out
}

// TestHandleCensusGraphFilterOnRealStores runs the whole path of a bare pull
// request number with a caller repository narrowing on a real ClickHouse (the
// census) and a real FalkorDB (the graph filter): the census lists the
// satisfiers inside the narrowing, the graph filter reads each satisfier's
// row. One satisfier left commits it. A satisfier with no graph row, and a
// graph read that fails, both keep the clarification (fail closed).
func TestHandleCensusGraphFilterOnRealStores(t *testing.T) {
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range productionSchemaDDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("apply rendered schema statement: %v\n%s", err, statement)
		}
	}
	createProjectMembershipPresenceView(t, ctx, direct)
	tracer := &roundTracer{}
	var afterCensus func()
	census := devhealthsource.NewCensusFunc(query)
	adapter := chaos7074FalkorAdapterWith(t, ctx, func(c *falkorgraph.Config) {
		c.ResolutionTracer = tracer
		// The hook lets one case cancel the request once the real census has
		// answered, so the graph filter's next read fails on the real store.
		c.CensusFunc = func(ctx context.Context, org string, kind graphrank.CensusKind, value string, handleBound bool, anchorKind contextfabric.SubjectKind, anchorID string, anchorBound bool) (graphrank.CensusOutcome, error) {
			outcome, err := census(ctx, org, kind, value, handleBound, anchorKind, anchorID, anchorBound)
			if afterCensus != nil {
				afterCensus()
			}
			return outcome, err
		}
		c.HandleGrammarChecker = func(kind contractsv1.ContextFabricSubjectKind, patternID, value string) (string, bool) {
			return graphrank.HandleSourceColumn(kind, patternID)
		}
	})
	now := time.Now().UTC()
	orgID := "o3000000-0000-4000-8000-000000008462"
	o3Seed(t, ctx, direct, orgID, now)
	seedPR := func(repo int) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced) VALUES (?, ?, ?, ?, ?, ?)`,
			o3UUID(orgID+"R"+fmt.Sprint(repo)), orgID, uint32(747), fmt.Sprintf("PR 747 in r%d", repo), "open", now); err != nil {
			t.Fatalf("seed PR: %v", err)
		}
	}
	seedPR(1)
	seedPR(2)
	main, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatal(err)
	}
	drainSource(t, ctx, main, adapter, orgID, devhealthsource.SourceName)
	// Pull request 747 of acme/r3 exists in the source after the drain: the
	// census lists it, the graph has no row for it.
	seedPR(3)

	principal := storage.Principal{OrgID: orgID, Subject: "u", CredentialID: "c"}
	prID := func(repo int) string { return "pull_request:" + o3UUID(orgID+"R"+fmt.Sprint(repo)) + ":747" }
	resolve := func(t *testing.T, id string, slugs []string, cancelAfterCensus bool) (committed []string, narrowings []string) {
		t.Helper()
		rctx, cancel := context.WithCancel(chaos7126Ctx(id))
		defer cancel()
		afterCensus = nil
		if cancelAfterCensus {
			afterCensus = cancel
		}
		defer func() { afterCensus = nil }()
		tracer.mu.Lock()
		tracer.rounds = nil
		tracer.mu.Unlock()
		binding, err := adapter.ResolveInvestigationBinding(rctx, principal)
		if err != nil {
			t.Fatal(err)
		}
		request := contextfabric.InvestigationRequest{
			SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_" + id, Question: "Is pull request 747 ready to merge?",
			TimeContext:    contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			RequestedScope: contextfabric.RequestedScope{RepositorySlugs: slugs},
			Options: contextfabric.InvestigationOptions{
				MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
				MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
			},
			Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
			SubjectHandles: []contractsv1.ContextFabricRequestedHandle{
				{Kind: contextfabric.SubjectPullRequest, PatternID: "pull_request_number", Value: "747"},
			},
		}
		interpreted := contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", SubjectTerms: []string{"pull request 747"},
			TimeContext:      contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
		}
		resolution, _, _, _, err := adapter.ResolveSubjects(rctx, principal, request, interpreted, binding, nil, nil, nil, "")
		if err != nil {
			t.Fatalf("ResolveSubjects(%s): %v", id, err)
		}
		for _, s := range resolution.Committed {
			if s.Kind == contextfabric.SubjectPullRequest {
				committed = append(committed, s.CanonicalID)
			}
		}
		return committed, tracer.narrowings()
	}

	t.Run("one satisfier inside the narrowing commits it", func(t *testing.T) {
		committed, narrowings := resolve(t, "inside", []string{"acme/r1"}, false)
		if len(committed) != 1 || committed[0] != prID(1) {
			t.Fatalf("committed = %v (narrowings %v), want only %s", committed, narrowings, prID(1))
		}
	})
	t.Run("two satisfiers inside the narrowing keep the clarification", func(t *testing.T) {
		// acme/r1 and acme/r2 are both inside the narrowing: two satisfiers.
		committed, narrowings := resolve(t, "many", []string{"acme/r1", "acme/r2"}, false)
		if len(committed) != 0 {
			t.Fatalf("committed = %v (narrowings %v), want none: two pull requests are inside the narrowing", committed, narrowings)
		}
	})
	t.Run("a satisfier with no graph row keeps the clarification", func(t *testing.T) {
		committed, narrowings := resolve(t, "missing-row", []string{"acme/r1", "acme/r3"}, false)
		if len(committed) != 0 {
			t.Fatalf("committed = %v (narrowings %v), want none: the satisfier of acme/r3 has no graph row", committed, narrowings)
		}
		if len(narrowings) != 1 || narrowings[0] != "satisfier_read_failed" {
			t.Fatalf("narrowings = %v, want one satisfier_read_failed", narrowings)
		}
	})
	t.Run("a failed graph read keeps the clarification", func(t *testing.T) {
		committed, narrowings := resolve(t, "read-failure", []string{"acme/r1", "acme/r2"}, true)
		if len(committed) != 0 {
			t.Fatalf("committed = %v (narrowings %v), want none: the graph read failed", committed, narrowings)
		}
		if len(narrowings) != 1 || narrowings[0] != "satisfier_read_failed" {
			t.Fatalf("narrowings = %v, want one satisfier_read_failed", narrowings)
		}
	})
}
