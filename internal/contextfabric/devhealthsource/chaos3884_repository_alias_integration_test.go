package devhealthsource_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

// TestClickHouseProjectionSourceProjectsRepositoryBareNameAndProviderAlias
// is CHAOS-3884 Part A's live end-to-end proof: a repository row with a
// provider set projects an entity carrying BOTH the bare-name alias and
// the provider-qualified alias, alongside the unqualified/qualified
// canonical label -- the same live ClickHouse container pattern
// clickhouse_org_isolation_integration_test.go already establishes.
func TestClickHouseProjectionSourceProjectsRepositoryBareNameAndProviderAlias(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL(sourceSchemaTables...) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}

	repoID := "22222222-2222-2222-2222-222222222222"
	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		repoID, "org-alias", "full-chaos/dev-health-acr", "github", at); err != nil {
		t.Fatalf("seed repos: %v", err)
	}

	source, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	batch, available, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: "org-alias", Source: devhealthsource.SourceName})
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	if !available {
		t.Fatal("expected a batch to be available")
	}

	var found *contextfabric.EntityProjection
	for i := range batch.Entities {
		if batch.Entities[i].Subject.CanonicalID == "repository:"+repoID {
			found = &batch.Entities[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected the seeded repository to be projected: %+v", batch.Entities)
	}
	if found.Subject.Label != "full-chaos/dev-health-acr" {
		t.Fatalf("repository label = %q, want the unchanged canonical org-qualified slug", found.Subject.Label)
	}
	if len(found.Aliases) != 1 || found.Aliases[0] != "dev-health-acr" {
		t.Fatalf("repository Aliases = %v, want exactly [\"dev-health-acr\"] (the bare-name alias)", found.Aliases)
	}
	if len(found.ProviderAliases) != 1 || found.ProviderAliases[0] != "github:full-chaos/dev-health-acr" {
		t.Fatalf("repository ProviderAliases = %v, want exactly [\"github:full-chaos/dev-health-acr\"]", found.ProviderAliases)
	}
	if err := found.Validate(); err != nil {
		t.Fatalf("projected repository entity fails contract Validate(): %v", err)
	}
}
