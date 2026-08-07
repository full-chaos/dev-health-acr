package contextpacket_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestClickHouseScopeResolver_returns_unresolved_when_authorized_repository_is_missing(t *testing.T) {
	// Given
	plan, err := contextpacket.BuildReadPlanV1(fixturePrincipal(), fixtureRequest("scope-missing", "main", "commit-1"))
	if err != nil {
		t.Fatalf("build read plan: %v", err)
	}
	resolver := contextpacket.NewClickHouseScopeResolver(&scopeClient{})

	// When
	scope, err := resolver.ResolveEvidenceScope(context.Background(), plan)

	// Then
	if err != nil {
		t.Fatalf("resolve scope: %v", err)
	}
	if scope.Resolution != contractsv1.ScopeUnresolved || scope.RepoID != "" || scope.RepoSlug != plan.RepoSlug {
		t.Fatalf("unexpected unresolved scope: %#v", scope)
	}
}

func TestCatalogClickHouseRows_resolves_exact_commit_and_maps_evidence(t *testing.T) {
	// Given
	plan, err := contextpacket.BuildReadPlanV1(fixturePrincipal(), fixtureRequest("scope-exact", "main", "commit-1"))
	if err != nil {
		t.Fatalf("build read plan: %v", err)
	}
	rows := contextpacket.NewCatalogClickHouseRows(&catalogClient{})

	// When
	scope, err := rows.ResolveEvidenceScope(context.Background(), plan)
	if err != nil {
		t.Fatalf("resolve scope: %v", err)
	}
	plan.RepoID = scope.RepoID
	evidence, watermarks, unavailable, err := rows.EvidenceRows(context.Background(), plan)

	// Then
	if err != nil {
		t.Fatalf("load catalog evidence: %v", err)
	}
	if scope.Resolution != contractsv1.ScopeExactCommit || scope.RepoID != "00000000-0000-0000-0000-000000000001" {
		t.Fatalf("unexpected exact scope: %#v", scope)
	}
	if len(evidence) != len(contextpacket.SourceQueryCatalogV1) || len(watermarks) != len(contextpacket.SourceQueryCatalogV1) || len(unavailable) != 0 {
		t.Fatalf("unexpected catalog result: evidence=%d watermarks=%d unavailable=%d", len(evidence), len(watermarks), len(unavailable))
	}
	for _, ref := range evidence {
		if ref.SourceVersion == "deployments.v1" {
			if ref.Source.DisplayLabel != "test (repository-wide)" || ref.Metadata["scope_breadth"] != "repository-wide" {
				t.Fatalf("deployment evidence does not disclose repository-wide breadth: %#v", ref)
			}
			return
		}
	}
	t.Fatal("catalog did not return deployments.v1 evidence")
}

type scopeClient struct{}

func (*scopeClient) Query(_ context.Context, _ string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	return &rowScanner{}, nil
}

type catalogClient struct{}

func (*catalogClient) Query(_ context.Context, statement string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if statement == contextpacket.RepositoryScopeQueryV1 {
		return &rowScanner{rows: [][]any{{"00000000-0000-0000-0000-000000000001", "example-org/widget-service", "main"}}}, nil
	}
	return &rowScanner{rows: [][]any{{"acr:v1:test:1", "dev_health", "test", "1", "test", "", "native", 0.9, "citation", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), (*time.Time)(nil)}}}, nil
}
