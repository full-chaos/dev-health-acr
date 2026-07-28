package contextpacket_test

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

type capturingRows struct {
	plan contextpacket.ReadPlan
	err  error
}

type capturingExecutor struct {
	queries  []contextpacket.SourceQuery
	bindings [][]contextpacket.ClickHouseBinding
}

func (e *capturingExecutor) QueryEvidence(_ context.Context, query contextpacket.SourceQuery, bindings []contextpacket.ClickHouseBinding) ([]contractsv1.EvidenceRef, error) {
	e.queries = append(e.queries, query)
	e.bindings = append(e.bindings, bindings)
	return []contractsv1.EvidenceRef{}, nil
}

func (r *capturingRows) ResolveEvidenceScope(_ context.Context, plan contextpacket.ReadPlan) (contractsv1.ResolvedScope, error) {
	return contractsv1.ResolvedScope{RepoID: "repo-server-derived", RepoSlug: plan.RepoSlug, Branch: plan.Branch, CommitSHA: plan.CommitSHA, Resolution: contractsv1.ScopeExactCommit, FallbackReasons: []string{}}, nil
}

func (r *capturingRows) EvidenceRows(_ context.Context, plan contextpacket.ReadPlan) ([]contractsv1.EvidenceRef, []contractsv1.SourceWatermark, []contractsv1.UnavailableSource, error) {
	r.plan = plan
	return nil, nil, nil, r.err
}

func TestClickHouseAdapter_scopes_query_to_authenticated_repository(t *testing.T) {
	rows := &capturingRows{}
	request := fixtureRequest("req-clickhouse", "main", "abc123")
	request.Scope.TaskRef, request.Scope.Files, request.Scope.TimeWindowDays = "TASK-9", []string{"internal/x.go"}, 7
	store, err := contextpacket.NewClickHouseEvidenceStoreWithOptions(rows, contextpacket.EvidenceStoreOptions{Codec: fixtureEvidenceCodec(t)})
	if err != nil {
		t.Fatalf("create evidence store: %v", err)
	}
	_, err = store.ContextForTask(context.Background(), fixturePrincipal(), request)
	if err != nil {
		t.Fatalf("read scoped evidence: %v", err)
	}
	if rows.plan.OrgID != "org-fixture" || rows.plan.RepoSlug != request.Repository.Slug || rows.plan.CommitSHA != "abc123" || rows.plan.TaskRef != "TASK-9" || len(rows.plan.Files) != 1 {
		t.Fatalf("unexpected plan: %#v", rows.plan)
	}
	bindings := rows.plan.Bindings()
	if len(bindings) != 10 || bindings[0].Name != "org_id" || bindings[0].Value != "org-fixture" || bindings[1].Name != "repo_id" || bindings[1].Value != "repo-server-derived" || bindings[4].Name != "branch_hash" || bindings[4].Value != "" || bindings[5].Value != "abc123" {
		t.Fatalf("unexpected named bindings: %#v", bindings)
	}
}

func TestAssembler_propagates_cancelled_context(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assembler := fixtureAssembler(t)
	_, err := assembler.Assemble(ctx, fixturePrincipal(), fixtureRequest("req-cancelled", "main", ""))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
}

func TestExecuteCatalog_runs_repo_scope_sources_when_branch_is_requested(t *testing.T) {
	plan, err := contextpacket.BuildReadPlanV1(fixturePrincipal(), fixtureRequest("req-query", "main", "abc123"))
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	plan.RepoID = "repo-server-derived"
	executor := &capturingExecutor{}
	result, err := contextpacket.ExecuteCatalog(context.Background(), executor, plan)
	if err != nil {
		t.Fatalf("execute catalog: %v", err)
	}
	repoQueries := 0
	for index, bindings := range executor.bindings {
		if executor.queries[index].Scope == contextpacket.EvidenceScopeRepo {
			repoQueries++
		}
		if bindings[0].Name != "org_id" || bindings[0].Value != "org-fixture" || bindings[1].Name != "repo_id" || bindings[1].Value != plan.RepoID {
			t.Fatalf("query %s was not independently org and repo scoped: %#v", executor.queries[index].ID, bindings)
		}
	}
	if repoQueries == 0 {
		t.Fatal("branch scope did not execute any repository-wide source")
	}
	for _, query := range contextpacket.SourceQueryCatalogV1 {
		if query.Scope == contextpacket.EvidenceScopeRepo {
			if containsUnavailable(result.Unavailable, query.ID, "repo_fallback_branch_not_supported") {
				t.Fatalf("branch scope excluded reachable source %s: %#v", query.ID, result.Unavailable)
			}
			if watermarkStatus(result.Watermarks, query.ID) != "missing" {
				t.Fatalf("watermark source = %q, want pure source ID %q with missing status", query.ID, query.ID)
			}
		}
	}
}
