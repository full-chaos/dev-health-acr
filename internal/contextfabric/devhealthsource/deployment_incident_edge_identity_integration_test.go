package devhealthsource_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/evidenceref"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// applyFrom pages the real producer from cursor and applies every page to the
// real graph. It returns the cursor it reached and the first apply error.
func applyFrom(ctx context.Context, t *testing.T, source contextfabric.ProjectionSource, adapter *falkorgraph.Adapter, orgID, cursor string) (string, error) {
	t.Helper()
	for page := 0; page < 200; page++ {
		batch, available, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: orgID, Source: devhealthsource.SourceName, Cursor: cursor})
		if err != nil {
			t.Fatalf("read page %d: %v", page, err)
		}
		if !available {
			return cursor, nil
		}
		if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
			return cursor, fmt.Errorf("apply page %d: %w", page, err)
		}
		cursor = batch.NextCursor
	}
	t.Fatal("the source did not drain")
	return cursor, nil
}

type incidentEdgeFixture struct {
	orgID, repoA, repoB, incidentID string
}

// seedIncidentMappedToTwoRepos writes an organization with two repositories
// that both run deployment "deploy-shared-1", and one incident whose service
// maps to both.
func seedIncidentMappedToTwoRepos(ctx context.Context, t *testing.T, orgID string, direct interface {
	Exec(context.Context, string, ...any) error
}) incidentEdgeFixture {
	t.Helper()
	f := incidentEdgeFixture{orgID: orgID, repoA: o3UUID(orgID + "a"), repoB: o3UUID(orgID + "b"), incidentID: o3UUID(orgID + "incident")}
	const serviceID = "pagerduty:PX7Q2LM:service:PS4K9D9"
	now := time.Now().UTC().Truncate(time.Second)
	earlier := now.Add(-3 * time.Hour)
	exec := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	for _, repo := range [][2]string{{f.repoA, "acme/alpha"}, {f.repoB, "acme/beta"}} {
		exec("repo "+repo[1], `INSERT INTO repos (id, repo, ref, created_at, tags, last_synced, org_id, provider) VALUES (?,?,?,?,?,?,?,?)`, repo[0], repo[1], nil, now, nil, earlier, orgID, "github")
		exec("mapping "+repo[1], `INSERT INTO operational_service_repository_mappings (org_id, source_version_at, id, service_id, repo_id, is_active) VALUES (?, ?, ?, ?, ?, ?)`,
			orgID, earlier, "mapping-"+repo[1], serviceID, repo[0], uint8(1))
		exec("deployment "+repo[1], `INSERT INTO deployments (repo_id, org_id, deployment_id, status, environment, deployed_at, started_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			repo[0], orgID, "deploy-shared-1", "success", "prod", earlier, earlier, earlier)
	}
	exec("incident", `INSERT INTO operational_incidents (id, org_id, source_version_at, service_id, title, normalized_status, raw_status, normalized_severity, raw_severity, started_at, source_event_at, observed_at, is_deleted, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.incidentID, orgID, earlier, serviceID, "shared outage", "triggered", "triggered", "sev2", "sev2", earlier, earlier, earlier, uint8(0), earlier)
	return f
}

func insertDeploymentIncidentEdge(ctx context.Context, t *testing.T, direct interface {
	Exec(context.Context, string, ...any) error
}, f incidentEdgeFixture, repoID, source string, computedAt time.Time) {
	t.Helper()
	// edge_id is what ops derives from (org, deployment_id, incident_id): the
	// same value for both repositories, because it names neither repo nor source.
	if err := direct.Exec(ctx, `INSERT INTO work_graph_deployment_incident_edges (edge_id, deployment_id, incident_id, repo_id, org_id, confidence, source, evidence, observed_at, computed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"edge-shared-1", "deploy-shared-1", f.incidentID, repoID, f.orgID, float32(0.8), source, "", computedAt, computedAt); err != nil {
		t.Fatalf("seed edge: %v", err)
	}
}

// A deployment_id that two repositories share, and an incident mapped to both:
// ops writes one edge per repository with the SAME edge_id. The second row
// replaces the first in ClickHouse, so the relationship id the producer mints
// (relationship:deployment_incident:<edge_id>) now names an edge whose From
// endpoint is the other repository's deployment. The graph's unique
// relationship-id constraint must not reject the write and stop the
// organization's projection.
func TestSharedDeploymentIDAcrossReposDoesNotWedgeTheProjection(t *testing.T) {
	ctx := context.Background()
	query, direct := orgIsolationClickHouseFixture(t)
	var addr string
	adapter := chaos7074FalkorAdapterWith(t, ctx, func(config *falkorgraph.Config) {
		config.GraphPrefix = "acr-cf-edge-identity-a"
		addr = config.Addr
	})
	raw := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = raw.Close() })

	const orgID = "93000000-0000-4000-8000-000000000001"
	f := seedIncidentMappedToTwoRepos(ctx, t, orgID, direct)
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	source, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	insertDeploymentIncidentEdge(ctx, t, direct, f, f.repoA, "heuristic", now.Add(-2*time.Hour))
	cursor, err := applyFrom(ctx, t, source, adapter, orgID, "")
	if err != nil {
		t.Fatalf("first repository's edge: %v", err)
	}

	insertDeploymentIncidentEdge(ctx, t, direct, f, f.repoB, "heuristic", now.Add(-time.Hour))
	if _, err := applyFrom(ctx, t, source, adapter, orgID, cursor); err != nil {
		if errors.Is(err, falkorgraph.ErrConstraintViolation) {
			t.Fatalf("the projection is wedged by the second repository's edge: %v", err)
		}
		t.Fatalf("second repository's edge: %v", err)
	}

	keys := graphKeys(t, ctx, raw)
	if len(keys) != 1 {
		t.Fatalf("%d graphs, want 1", len(keys))
	}
	for key := range keys {
		reply, err := raw.Do(ctx, "GRAPH.RO_QUERY", key, "MATCH ()-[r:CORRELATED_WITH_INCIDENT]->() RETURN count(r)").Slice()
		if err != nil || len(reply) < 2 {
			t.Fatalf("count edges: reply of %d parts, err=%v", len(reply), err)
		}
		records, _ := reply[1].([]any)
		cells, _ := records[0].([]any)
		if edges, _ := cells[0].(int64); edges != 2 {
			t.Errorf("%v CORRELATED_WITH_INCIDENT edges, want 2 (one per repository's deployment)", cells[0])
		}
	}
}

// The native row (5110) and the heuristic row for one deployment and incident are one row in the source table
// (ops keys it by repository, deployment and incident, no source) and the higher tier wins whatever the time: the
// producer projects one relationship for the pair, from the native row.
func TestNativeAndHeuristicDeploymentIncidentRowsAreOneRelationship(t *testing.T) {
	ctx := context.Background()
	query, direct := orgIsolationClickHouseFixture(t)

	const orgID = "93000000-0000-4000-8000-000000000002"
	f := seedIncidentMappedToTwoRepos(ctx, t, orgID, direct)
	source, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	insertDeploymentIncidentEdge(ctx, t, direct, f, f.repoA, "native", now.Add(-2*time.Hour))
	insertDeploymentIncidentEdge(ctx, t, direct, f, f.repoA, "heuristic", now.Add(-time.Hour))

	h := &ingestHarness{t: t, ctx: ctx, direct: direct, src: source, source: devhealthsource.SourceName, orgID: orgID, repo: f.repoA}
	edges := h.drain("").relationships["CORRELATED_WITH_INCIDENT"]
	if len(edges) != 1 {
		t.Fatalf("%d CORRELATED_WITH_INCIDENT relationships, want 1 (one row for the pair)", len(edges))
	}
	wantRef, _ := evidenceref.Mint(contractsv1.ContextFabricEvidenceEntityDeploymentIncidentV2, f.repoA, "deploy-shared-1", f.incidentID, "native")
	if len(edges[0].EvidenceRefIDs) != 1 || edges[0].EvidenceRefIDs[0] != wantRef {
		t.Errorf("evidence refs %v, want the native row's %s (the higher tier wins although the heuristic row is newer)", edges[0].EvidenceRefIDs, wantRef)
	}
}
