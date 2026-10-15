package sourcerow_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/sourcerow"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// An incident ref resolves to the latest revision of the incident, through
// the mappings current in their latest revision: one row, one repository.
func TestIncidentSourceRowServesTheLatestRevision(t *testing.T) {
	ctx := context.Background()
	query, direct := startClickHouse(t, ctx)
	seedIntegration(t, ctx, direct)
	const (
		incidentID = "7d1c9e4a-2b6f-4a83-9e51-0c4f8a2d6b17"
		deletedID  = "7d1c9e4a-2b6f-4a83-9e51-0c4f8a2d6b18"
		serviceID  = "pagerduty:PX7Q2LM:service:PS4K9D2"
		earlier    = "'2026-01-01 00:00:00.000000'"
		later      = "'2026-01-02 00:00:00.000000'"
	)
	mapping := func(id, repoID, version string, active int) string {
		return fmt.Sprintf(`INSERT INTO operational_service_repository_mappings (org_id, source_version_at, id, relationship_provenance, relationship_confidence, service_id, repo_id, valid_from, is_active) VALUES ('%s', %s, '%s', 'native', 0.9, '%s', '%s', %s, %d)`,
			integrationOrg, version, id, serviceID, repoID, earlier, active)
	}
	incident := func(id, version, title string, deleted int) string {
		return fmt.Sprintf(`INSERT INTO operational_incidents (org_id, source_version_at, id, observed_at, last_synced, service_id, title, started_at, is_deleted) VALUES ('%s', %s, '%s', %s, %s, '%s', '%s', %s, %d)`,
			integrationOrg, version, id, earlier, version, serviceID, title, earlier, deleted)
	}
	for _, statement := range []string{
		mapping("mapping-api", grantedID, earlier, 1),
		mapping("mapping-secret", secretID, earlier, 1),
		mapping("mapping-secret", secretID, later, 0),
		incident(incidentID, earlier, "checkout errors", 0),
		incident(incidentID, later, "checkout errors resolved", 0),
		incident(deletedID, earlier, "duplicate page", 0),
		incident(deletedID, later, "duplicate page", 1),
	} {
		exec(t, ctx, direct, statement)
	}
	resolve, err := sourcerow.New(contextpacket.NewCatalogClickHouseRows(query), contextpacket.NewEvidenceResolver(contextpacket.EvidenceResolverOptions{}), admitAllSubjects{})
	if err != nil {
		t.Fatal(err)
	}
	orgWide := storage.Principal{OrgID: integrationOrg}
	kind := string(contractsv1.ContextFabricEvidenceEntityIncident)

	expanded, decision := resolve.ResolveSourceRow(ctx, orgWide, kind, incidentID)
	if decision.Reason != contextfabric.SourceRowServed {
		t.Fatalf("incident: %+v", decision)
	}
	if decision.Rows != 1 || decision.Repositories != 1 {
		t.Fatalf("incident: %d rows over %d repositories, want 1 over 1 (the other mapping is inactive in its latest revision): %+v", decision.Rows, decision.Repositories, decision)
	}
	if got := expanded.Evidence.Source.DisplayLabel; got != "checkout errors resolved" {
		t.Errorf("label %q, want the latest revision's title", got)
	}
	if got := expanded.Structured["repository"]; got != grantedRep {
		t.Errorf("repository %v, want %s", got, grantedRep)
	}

	if _, decision := resolve.ResolveSourceRow(ctx, orgWide, kind, deletedID); decision.Reason != contextfabric.SourceRowNoRow {
		t.Fatalf("an incident deleted in its latest revision was served: %+v", decision)
	}
}
