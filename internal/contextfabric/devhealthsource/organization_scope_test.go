package devhealthsource_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

// TestOnlyTheOrganizationEntityPopulatesProjectIDs is the structural half of
// the organization-scope collision proof: every OTHER entity and
// relationship ClickHouseProjectionSource produces uses repository-scoped
// authorization (RepositorySlugs), never ProjectIDs, so none of this
// package's own producers can ever collide with the reserved
// organization-scope namespace -- there is nothing else populating that
// field to collide with. Confirmed against a full snapshot batch covering
// every entity/relationship kind this source projects today.
func TestOnlyTheOrganizationEntityPopulatesProjectIDs(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	client := &fakeClient{tables: baseTables(at)}
	source, err := devhealthsource.NewClickHouseProjectionSource(client)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	batch, _, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName})
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}

	organizationEntities, otherEntitiesWithProjectIDs := 0, 0
	for _, entity := range batch.Entities {
		if len(entity.Authorization.ProjectIDs) == 0 {
			continue
		}
		if entity.Subject.Kind == contextfabric.SubjectOrganization {
			organizationEntities++
			if !devhealthsource.IsReservedAuthorizationScopeID(entity.Authorization.ProjectIDs[0]) {
				t.Fatalf("organization entity's own ProjectIDs value is not in the reserved namespace: %q", entity.Authorization.ProjectIDs[0])
			}
			continue
		}
		otherEntitiesWithProjectIDs++
		t.Errorf("non-organization entity %q (kind=%s) populates ProjectIDs, risking collision with the reserved organization-scope namespace: %+v",
			entity.Subject.CanonicalID, entity.Subject.Kind, entity.Authorization.ProjectIDs)
	}
	for _, relationship := range batch.Relationships {
		if len(relationship.Authorization.ProjectIDs) != 0 {
			t.Errorf("relationship %q populates ProjectIDs, risking collision with the reserved organization-scope namespace: %+v",
				relationship.RelationshipID, relationship.Authorization.ProjectIDs)
		}
	}
	if organizationEntities != 1 {
		t.Fatalf("expected exactly one organization entity in a full snapshot, got %d", organizationEntities)
	}
	if otherEntitiesWithProjectIDs != 0 {
		t.Fatalf("expected zero non-organization entities to populate ProjectIDs, got %d", otherEntitiesWithProjectIDs)
	}
}

// TestReservedAuthorizationScopeNamespaceIsUnambiguous proves the guard
// function itself: values built by organizationScopeID are recognized, and
// realistic non-reserved values (provider keys, UUIDs, repo slugs, and a
// value that merely contains the prefix as a substring rather than as its
// own prefix) are not.
func TestReservedAuthorizationScopeNamespaceIsUnambiguous(t *testing.T) {
	t.Parallel()
	orgID := "11111111-1111-1111-1111-111111111111"
	reserved := "acr-context-fabric:org-scope:" + orgID
	if !devhealthsource.IsReservedAuthorizationScopeID(reserved) {
		t.Fatalf("expected %q to be recognized as reserved", reserved)
	}
	nonReserved := []string{
		"", "WIDGET-101", "project_ask_dev", orgID, "linear_project_123",
		"owner/repo", "org-scope:" + orgID,
		"x-acr-context-fabric:org-scope:" + orgID, // prefix must anchor at the start
	}
	for _, id := range nonReserved {
		if devhealthsource.IsReservedAuthorizationScopeID(id) {
			t.Fatalf("expected %q not to be recognized as reserved", id)
		}
	}
	if !strings.HasPrefix(reserved, "acr-context-fabric:org-scope:") {
		t.Fatal("sanity: reserved fixture must actually carry the prefix under test")
	}
}
