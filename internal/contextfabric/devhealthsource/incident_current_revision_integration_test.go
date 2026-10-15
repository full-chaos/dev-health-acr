package devhealthsource_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Under the ordering contract every source revision of an incident or a
// service mapping is its own row. The projection, its source counts and the
// deployment/incident edges serve the latest revision of each id only.
func TestIncidentProjectionServesTheLatestRevision(t *testing.T) {
	ctx := context.Background()
	query, direct := orgIsolationClickHouseFixture(t)
	const (
		orgID       = "5b7e2c40-91d3-4f6a-8e25-3c0d9a7f1b61"
		mappedRepo  = "5b7e2c40-91d3-4f6a-8e25-3c0d9a7f1b62"
		droppedRepo = "5b7e2c40-91d3-4f6a-8e25-3c0d9a7f1b63"
		incidentID  = "7d1c9e4a-2b6f-4a83-9e51-0c4f8a2d6b17"
		deletedID   = "7d1c9e4a-2b6f-4a83-9e51-0c4f8a2d6b18"
		serviceID   = "pagerduty:PX7Q2LM:service:PS4K9D2"
	)
	now := time.Now().UTC().Truncate(time.Second)
	earlier, later := now.Add(-2*time.Hour), now.Add(-time.Hour)
	started := now.Add(-3 * time.Hour)

	for _, repo := range [][2]string{{mappedRepo, "acme/checkout"}, {droppedRepo, "acme/ledger"}} {
		mustExec(t, ctx, direct, `INSERT INTO repos (id, repo, ref, created_at, tags, last_synced, org_id, provider) VALUES (?,?,?,?,?,?,?,?)`, repo[0], repo[1], nil, now, nil, earlier, orgID, "github")
	}
	mapping := func(id, repoID string, version time.Time, active uint8) {
		mustExec(t, ctx, direct, `INSERT INTO operational_service_repository_mappings (org_id, source_version_at, id, service_id, repo_id, is_active) VALUES (?, ?, ?, ?, ?, ?)`,
			orgID, version, id, serviceID, repoID, active)
	}
	mapping("mapping-checkout", mappedRepo, earlier, 1)
	mapping("mapping-ledger", droppedRepo, earlier, 1)
	mapping("mapping-ledger", droppedRepo, later, 0)

	incident := func(id string, version time.Time, title, status, description string, resolved any, deleted uint8) {
		mustExec(t, ctx, direct, `INSERT INTO operational_incidents (id, org_id, source_version_at, service_id, title, description, normalized_status, raw_status, normalized_severity, raw_severity, started_at, resolved_at, source_event_at, observed_at, is_deleted, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, orgID, version, serviceID, title, description, status, status, "sev2", "sev2", started, resolved, started, started, deleted, version)
	}
	incident(incidentID, earlier, "checkout errors", "triggered", "first report", nil, 0)
	incident(incidentID, later, "checkout errors resolved", "resolved", "root cause found", later, 0)
	incident(deletedID, earlier, "duplicate page", "triggered", "", nil, 0)
	incident(deletedID, later, "duplicate page", "triggered", "", nil, 1)

	mustExec(t, ctx, direct, `INSERT INTO deployments (repo_id, org_id, deployment_id, status, environment, deployed_at, started_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		mappedRepo, orgID, "deploy-checkout-7f3a", "success", "prod", started, started, earlier)
	mustExec(t, ctx, direct, `INSERT INTO work_graph_deployment_incident_edges (edge_id, deployment_id, incident_id, repo_id, org_id, observed_at, computed_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"edge-checkout-7f3a", "deploy-checkout-7f3a", incidentID, mappedRepo, orgID, started, earlier)

	src, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatal(err)
	}
	h := &ingestHarness{t: t, ctx: ctx, direct: direct, src: src, source: devhealthsource.SourceName, orgID: orgID, repo: mappedRepo}
	drain := h.drain("")

	got, ok := drain.entityOfKind(t, contractsv1.ContextFabricSubjectIncident)
	if !ok {
		t.Fatal("no incident entity was served")
	}
	if got.Subject.CanonicalID != "incident:"+incidentID {
		t.Fatalf("served incident %q, want incident:%s (the deleted one has no current row)", got.Subject.CanonicalID, incidentID)
	}
	if got.Subject.Label != "checkout errors resolved" {
		t.Errorf("label = %q, want the latest revision's title", got.Subject.Label)
	}
	for property, want := range map[string]string{"status": "resolved", "description": "root cause found"} {
		value := got.Properties[property]
		if value.String == nil || *value.String != want {
			t.Errorf("%s = %#v, want %q (the latest revision)", property, value, want)
		}
	}
	if got.ValidTo == nil || !got.ValidTo.Equal(later) {
		t.Errorf("valid_to = %v, want the latest revision's resolved_at %v", got.ValidTo, later)
	}
	var repositories []string
	for _, relationship := range drain.relationships["BELONGS_TO_REPOSITORY"] {
		if relationship.From.Kind == contractsv1.ContextFabricSubjectIncident {
			repositories = append(repositories, relationship.To.CanonicalID)
		}
	}
	if len(repositories) != 1 || repositories[0] != "repository:"+mappedRepo {
		t.Errorf("incident repositories = %v, want only repository:%s (the other mapping is inactive in its latest revision)", repositories, mappedRepo)
	}
	if edges := drain.relationships["CORRELATED_WITH_INCIDENT"]; len(edges) != 1 {
		t.Errorf("%d deployment/incident edges, want 1", len(edges))
	}

	counts, err := src.ProjectionSourceCounts(ctx, orgID)
	if err != nil {
		t.Fatalf("ProjectionSourceCounts: %v", err)
	}
	if counts[contractsv1.ContextFabricSubjectIncident] != 1 {
		t.Errorf("incident source count = %d, want 1", counts[contractsv1.ContextFabricSubjectIncident])
	}
}
