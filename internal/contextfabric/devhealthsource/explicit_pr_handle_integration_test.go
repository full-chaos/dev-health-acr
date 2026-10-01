package devhealthsource_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestExplicitPullRequestHandleOnRealProducers resolves a pull_request_number
// subject handle through the real resolver: seeded ClickHouse, the real
// producers into a real FalkorDB, the real census. Pull request 747 exists in
// two repositories, so only the repository the question names can anchor it.
func TestExplicitPullRequestHandleOnRealProducers(t *testing.T) {
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range productionSchemaDDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("apply rendered schema statement: %v\n%s", err, statement)
		}
	}
	createProjectMembershipPresenceView(t, ctx, direct)
	adapter := chaos7074FalkorAdapterWith(t, ctx, func(c *falkorgraph.Config) {
		c.CensusFunc = devhealthsource.NewCensusFunc(query)
		c.HandleGrammarChecker = func(kind contractsv1.ContextFabricSubjectKind, patternID, value string) (string, bool) {
			return graphrank.HandleSourceColumn(kind, patternID)
		}
	})
	now := time.Now().UTC()
	orgID := "o3000000-0000-4000-8000-000000000358"
	o3Seed(t, ctx, direct, orgID, now)
	for _, pr := range []struct {
		repo   int
		number uint32
	}{{1, 747}, {2, 747}} {
		if err := direct.Exec(ctx, `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced) VALUES (?, ?, ?, ?, ?, ?)`,
			o3UUID(orgID+"R"+fmt.Sprint(pr.repo)), orgID, pr.number, fmt.Sprintf("PR %d in r%d", pr.number, pr.repo), "open", now); err != nil {
			t.Fatalf("seed PR: %v", err)
		}
	}
	main, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatal(err)
	}
	drainSource(t, ctx, main, adapter, orgID, devhealthsource.SourceName)

	principal := storage.Principal{OrgID: orgID, Subject: "u", CredentialID: "c"}
	resolve := func(t *testing.T, id, question string, terms ...string) contextfabric.SubjectResolution {
		t.Helper()
		rctx := chaos7126Ctx(id)
		binding, err := adapter.ResolveInvestigationBinding(rctx, principal)
		if err != nil {
			t.Fatal(err)
		}
		request := contextfabric.InvestigationRequest{
			SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_" + id, Question: question,
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
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
			Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", SubjectTerms: terms,
			TimeContext:      contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
		}
		resolution, _, _, _, err := adapter.ResolveSubjects(rctx, principal, request, interpreted, binding, nil, nil, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		return resolution
	}
	prID := "pull_request:" + o3UUID(orgID+"R1") + ":747"

	t.Run("repository named in the question anchors the handle", func(t *testing.T) {
		resolution := resolve(t, "named", "Is pull request 747 in acme/r1 ready to merge?", "acme/r1")
		var kinds []string
		got := ""
		for _, s := range resolution.Committed {
			kinds = append(kinds, string(s.Kind))
			if s.Kind == contextfabric.SubjectPullRequest {
				got = s.CanonicalID
			}
		}
		if got != prID {
			t.Fatalf("Committed kinds = %v, pull request = %q, want %q", kinds, got, prID)
		}
	})
	t.Run("no repository named leaves the handle for clarification", func(t *testing.T) {
		resolution := resolve(t, "unnamed", "Is pull request 747 ready to merge?", "pull request 747")
		for _, s := range resolution.Committed {
			if s.Kind == contextfabric.SubjectPullRequest {
				t.Fatalf("Committed = %#v, want no pull request: 747 exists in two repositories", resolution.Committed)
			}
		}
	})
}
